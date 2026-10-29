// NBD client (fixed newstyle): structured replies + metadata contexts.
// Used to pull incremental backup data out of a QEMU block export whose
// dirty-bitmap context (qemu:dirty-bitmap:<name>) describes changed regions.
package nbd

import (
	"bufio"
	"encoding/binary"
	"fmt"
	"io"
	"net"
)

const (
	gotMagic      = 0x49484156454F5054 // "IHAVEOPT"
	repMagic      = 0x0003e889045565a9 // option reply magic
	requestMagic  = 0x25609513
	simpleMagic   = 0x67446698
	structuredMag = 0x668e33ef

	optGo              = 7
	optStructuredReply = 8
	optSetMetaContext  = 10

	repAck         = 1
	repInfo        = 3
	repMetaContext = 4

	infoExport    = 0
	infoBlockSize = 3

	cmdRead        = 0
	cmdWrite       = 1
	cmdBlockStatus = 7

	replyFlagDone  = 1 << 0
	replyTypeError = 1 << 15

	typeOffsetData  = 1
	typeOffsetHole  = 2
	typeBlockStatus = 5
	typeNone        = 0
	stateDirty      = 1
	errRepBit       = 1 << 31
)

type Extent struct {
	Offset uint64
	Length uint32
	Flags  uint32
}

type Client struct {
	c      net.Conn
	br     *bufio.Reader
	Size   uint64
	cookie uint64
	ids    map[string]uint32 // metadata context name -> id
}

// DialNBD negotiates export and enables structured replies + block status.
func DialNBD(network, addr, export string) (*Client, error) {
	c, err := net.Dial(network, addr)
	if err != nil {
		return nil, err
	}
	cl := &Client{c: c, br: bufio.NewReader(c), ids: map[string]uint32{}}
	if err := cl.negotiate(export); err != nil {
		c.Close()
		return nil, err
	}
	return cl, nil
}

func (cl *Client) Close() error { return cl.c.Close() }

func (cl *Client) negotiate(export string) error {
	// greeting: 8 NBDMAGIC + 8 IHAVEOPT + 16-bit handshake flags
	g := make([]byte, 18)
	if _, err := io.ReadFull(cl.br, g); err != nil {
		return err
	}
	if string(g[:8]) != "NBDMAGIC" || binary.BigEndian.Uint64(g[8:16]) != gotMagic {
		return fmt.Errorf("bad greeting %x", g[:16])
	}
	// client flags: NBD_FLAG_C_FIXED_NEWSTYLE
	if err := binary.Write(cl.c, binary.BigEndian, uint32(1)); err != nil {
		return err
	}
	// structured replies must come first: block status requires them
	if err := cl.opt(optStructuredReply, nil, nil); err != nil {
		return err
	}
	return nil
}

// SetMetaContext selects metadata contexts (queries like
// "qemu:dirty-bitmap:cp1" or "base:allocation"); returns name -> id.
func (cl *Client) SetMetaContext(export string, queries ...string) (map[string]uint32, error) {
	var p []byte
	p = appendU32(p, uint32(len(export)))
	p = append(p, export...)
	p = appendU32(p, uint32(len(queries)))
	for _, q := range queries {
		p = appendU32(p, uint32(len(q)))
		p = append(p, q...)
	}
	got := map[string]uint32{}
	err := cl.opt(optSetMetaContext, p, func(typ uint32, payload []byte) error {
		if typ != repMetaContext || len(payload) < 4 {
			return nil
		}
		id := binary.BigEndian.Uint32(payload[0:4])
		got[string(payload[4:])] = id
		return nil
	})
	if err != nil {
		return nil, err
	}
	for k, v := range got {
		cl.ids[k] = v
	}
	return got, nil
}

// EnterTransmission sends OPT_GO and records the export size.
func (cl *Client) EnterTransmission(export string) error {
	var p []byte
	p = appendU32(p, uint32(len(export)))
	p = append(p, export...)
	p = append(p, 0, 1) // u16: one info request
	p = append(p, 0, infoExport)
	return cl.opt(optGo, p, func(typ uint32, payload []byte) error {
		if typ != repInfo || len(payload) < 12 {
			return nil
		}
		if binary.BigEndian.Uint16(payload[0:2]) == infoExport {
			cl.Size = binary.BigEndian.Uint64(payload[2:10])
		}
		return nil
	})
}

// opt sends one option and reads replies until ACK/error.
func (cl *Client) opt(option uint32, payload []byte, onReply func(typ uint32, payload []byte) error) error {
	var hdr [16]byte // magic u64, option u32, length u32 (request header)
	binary.BigEndian.PutUint64(hdr[0:8], gotMagic)
	binary.BigEndian.PutUint32(hdr[8:12], option)
	binary.BigEndian.PutUint32(hdr[12:16], uint32(len(payload)))
	buf := append(hdr[:], payload...)
	if _, err := cl.c.Write(buf); err != nil {
		return err
	}
	for {
		var r [20]byte
		if _, err := io.ReadFull(cl.br, r[:]); err != nil {
			return err
		}
		if binary.BigEndian.Uint64(r[0:8]) != repMagic {
			return fmt.Errorf("bad option reply magic %x", r[0:8])
		}
		typ := binary.BigEndian.Uint32(r[12:16])
		l := binary.BigEndian.Uint32(r[16:20])
		pl := make([]byte, l)
		if _, err := io.ReadFull(cl.br, pl); err != nil {
			return err
		}
		if typ&errRepBit != 0 {
			return fmt.Errorf("option %d rejected: type=%d %q", option, typ, pl)
		}
		if typ == repAck {
			return nil
		}
		if onReply != nil {
			if err := onReply(typ, pl); err != nil {
				return err
			}
		}
	}
}

// DirtyExtents returns extents marked dirty in the given qemu dirty-bitmap
// context. Extent flag bit 0 (NBD_STATE_DIRTY) set means changed.
func (cl *Client) DirtyExtents(contextName string) ([]Extent, error) {
	id, ok := cl.ids[contextName]
	if !ok {
		return nil, fmt.Errorf("context %q not selected (have %v)", contextName, cl.ids)
	}
	var out []Extent
	var pos uint64
	for pos < cl.Size {
		chunks, err := cl.blockStatus(pos, cl.Size-pos)
		if err != nil {
			return nil, err
		}
		var advanced bool
		for _, ch := range chunks {
			if ch.id != id {
				continue
			}
			for _, e := range ch.extents {
				if e.Flags&stateDirty != 0 {
					out = append(out, Extent{Offset: pos, Length: e.Length, Flags: e.Flags})
				}
				pos += uint64(e.Length)
				advanced = true
			}
		}
		if !advanced {
			return nil, fmt.Errorf("block status made no progress at %d", pos)
		}
	}
	return out, nil
}

type statusChunk struct {
	id      uint32
	extents []Extent // Offset unset here; Length+Flags from server
}

func (cl *Client) blockStatus(offset, length uint64) ([]statusChunk, error) {
	if length > 0xffffffff {
		length = 0xffffffff
	}
	hdr := cl.reqHeader(cmdBlockStatus, 0)
	binary.BigEndian.PutUint64(hdr[16:24], offset)
	binary.BigEndian.PutUint32(hdr[24:28], uint32(length))
	if _, err := cl.c.Write(hdr); err != nil {
		return nil, err
	}
	chunks, _, err := cl.readStructured()
	return chunks, err
}

// WriteAt writes data at off (used to dirty-test exports; backup flow is read-only).
func (cl *Client) WriteAt(off uint64, data []byte) error {
	hdr := cl.reqHeader(cmdWrite, 0)
	binary.BigEndian.PutUint64(hdr[16:24], off)
	binary.BigEndian.PutUint32(hdr[24:28], uint32(len(data)))
	if _, err := cl.c.Write(append(hdr, data...)); err != nil {
		return err
	}
	peek, err := cl.br.Peek(4)
	if err != nil {
		return err
	}
	if binary.BigEndian.Uint32(peek) == simpleMagic {
		var r [16]byte // magic u32, error u32, cookie u64
		if _, err := io.ReadFull(cl.br, r[:]); err != nil {
			return err
		}
		if e := binary.BigEndian.Uint32(r[4:8]); e != 0 {
			return fmt.Errorf("write error %d", e)
		}
		return nil
	}
	_, _, err = cl.readStructured()
	return err
}

func (cl *Client) ReadAt(off uint64, length uint32) ([]byte, error) {
	hdr := cl.reqHeader(cmdRead, 0)
	binary.BigEndian.PutUint64(hdr[16:24], off)
	binary.BigEndian.PutUint32(hdr[24:28], length)
	if _, err := cl.c.Write(hdr); err != nil {
		return nil, err
	}
	_, raws, err := cl.readStructured()
	if err != nil {
		return nil, err
	}
	data := make([]byte, length)
	for _, b := range raws {
		copy(data[b.off-off:], b.data)
	}
	return data, nil
}

type rawChunk struct {
	off  uint64
	data []byte
}

func (cl *Client) reqHeader(cmd, flags uint16) []byte {
	cl.cookie++
	h := make([]byte, 28)
	binary.BigEndian.PutUint32(h[0:4], requestMagic)
	binary.BigEndian.PutUint16(h[4:6], flags)
	binary.BigEndian.PutUint16(h[6:8], cmd)
	binary.BigEndian.PutUint64(h[8:16], cl.cookie)
	return h
}

// readStructured reads structured reply chunks until DONE.
func (cl *Client) readStructured() ([]statusChunk, []rawChunk, error) {
	var chunks []statusChunk
	var raws []rawChunk
	for {
		var h [20]byte // magic u32, flags u16, type u16, cookie u64, len u32
		if _, err := io.ReadFull(cl.br, h[:]); err != nil {
			return nil, nil, err
		}
		if binary.BigEndian.Uint32(h[0:4]) != structuredMag {
			return nil, nil, fmt.Errorf("bad structured magic %x", h[0:4])
		}
		flags := binary.BigEndian.Uint16(h[4:6])
		typ := binary.BigEndian.Uint16(h[6:8])
		l := binary.BigEndian.Uint32(h[16:20])
		pl := make([]byte, l)
		if _, err := io.ReadFull(cl.br, pl); err != nil {
			return nil, nil, err
		}
		switch {
		case typ&replyTypeError != 0:
			var emsg string
			if len(pl) >= 6 {
				elen := binary.BigEndian.Uint16(pl[4:6])
				if int(elen) <= len(pl)-6 {
					emsg = string(pl[6 : 6+elen])
				}
			}
			return nil, nil, fmt.Errorf("nbd error chunk type=%d: %s", typ, emsg)
		case typ == typeBlockStatus:
			if len(pl) < 4 {
				return nil, nil, fmt.Errorf("short block status payload")
			}
			id := binary.BigEndian.Uint32(pl[0:4])
			var es []Extent
			for i := 4; i+8 <= len(pl); i += 8 {
				es = append(es, Extent{
					Length: binary.BigEndian.Uint32(pl[i : i+4]),
					Flags:  binary.BigEndian.Uint32(pl[i+4 : i+8]),
				})
			}
			chunks = append(chunks, statusChunk{id: id, extents: es})
		case typ == typeOffsetData:
			if len(pl) < 8 {
				return nil, nil, fmt.Errorf("short data chunk")
			}
			raws = append(raws, rawChunk{off: binary.BigEndian.Uint64(pl[0:8]), data: pl[8:]})
		case typ == typeOffsetHole:
			if len(pl) < 12 {
				return nil, nil, fmt.Errorf("short hole chunk")
			}
			off := binary.BigEndian.Uint64(pl[0:8])
			sz := binary.BigEndian.Uint32(pl[8:12])
			raws = append(raws, rawChunk{off: off, data: make([]byte, sz)})
		case typ == typeNone:
			// final success marker
		}
		if flags&replyFlagDone != 0 {
			return chunks, raws, nil
		}
	}
}

func appendU32(b []byte, v uint32) []byte {
	var t [4]byte
	binary.BigEndian.PutUint32(t[:], v)
	return append(b, t[:]...)
}

