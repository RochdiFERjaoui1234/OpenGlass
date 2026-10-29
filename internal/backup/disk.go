// Disk backup/restore over NBD: full backups read the guest-logical disk
// via a read-only qemu-nbd export; incrementals read only the ranges a
// QEMU dirty bitmap reports as changed (NBD block status), stored as spans.
// Full and incremental therefore share one address space (guest logical),
// so an incremental can be replayed over its base at restore time.
package backup

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/openglass/openglass/internal/chunker"
	"github.com/openglass/openglass/internal/repository/local"
	"github.com/openglass/openglass/internal/transport/nbd"
)

const (
	readWindow  = 1 << 20 // NBD read window for large sequential pulls
	exportTries = 50      // * 100ms socket/dial wait for qemu-nbd
)

// RunDisk backs up the full guest-logical disk image (domain must be shut
// off so qemu-nbd can open it) as an NBD virtual read.
func RunDisk(store *local.Store, vm, image, id string) (local.Manifest, error) {
	return backupFull(store, vm, id, image)
}

// backupFull reads the whole export at image into a full manifest.
// The caller must guarantee the image is quiesced (shut off, or frozen by
// a live snapshot — see live.go).
func backupFull(store *local.Store, vm, id, image string) (local.Manifest, error) {
	cl, stop, err := dialExport(image, "")
	if err != nil {
		return local.Manifest{}, err
	}
	defer stop()
	defer cl.Close()
	if err := cl.EnterTransmission(""); err != nil {
		return local.Manifest{}, fmt.Errorf("enter transmission: %w", err)
	}

	m := local.Manifest{ID: id, VM: vm, Size: int64(cl.Size), Created: time.Now().Unix()}
	var buf []byte
	for off := uint64(0); off < cl.Size; {
		n := uint64(readWindow)
		if off+n > cl.Size {
			n = cl.Size - off
		}
		buf, err = cl.ReadAt(off, uint32(n))
		if err != nil {
			return local.Manifest{}, fmt.Errorf("read at %d: %w", off, err)
		}
		for _, c := range chunker.Chunks(buf, chunker.DefaultParams) {
			h, err := store.Put(c)
			if err != nil {
				return local.Manifest{}, err
			}
			m.Chunks = append(m.Chunks, h)
		}
		off += n
	}
	if err := store.PutManifest(m); err != nil {
		return local.Manifest{}, err
	}
	return m, nil
}

// RunIncrementalNBD backs up only the ranges dirty in <bitmap> since
// parentID (domain shut off; bitmap must exist in the image).
func RunIncrementalNBD(store *local.Store, vm, parentID, image, bitmap, id string) (local.Manifest, error) {
	if parentID == "" {
		return local.Manifest{}, fmt.Errorf("incremental requires parent")
	}
	if bitmap == "" {
		return local.Manifest{}, fmt.Errorf("incremental requires dirty bitmap name")
	}
	return backupSpans(store, vm, parentID, id, image, bitmap)
}

// backupSpans exports image with dirty bitmap and stores the changed ranges
// as spans on an incremental manifest. The caller must guarantee the image
// is quiesced (shut off, or frozen by a live snapshot — see live.go).
func backupSpans(store *local.Store, vm, parentID, id, image, bitmap string) (local.Manifest, error) {
	parent, err := store.GetManifest(parentID)
	if err != nil {
		return local.Manifest{}, fmt.Errorf("parent %s: %w", parentID, err)
	}
	// inc-over-incremental is fine: spans are full data (not diffs), and
	// materialize replays every ancestor's spans in chronological order, so
	// overlapping dirty ranges converge to the newest bytes either way.

	ctx := "qemu:dirty-bitmap:" + bitmap
	cl, stop, err := dialExport(image, bitmap)
	if err != nil {
		return local.Manifest{}, err
	}
	defer stop()
	defer cl.Close()

	if _, err := cl.SetMetaContext("", ctx); err != nil {
		return local.Manifest{}, fmt.Errorf("select %s: %w", ctx, err)
	}
	if err := cl.EnterTransmission(""); err != nil {
		return local.Manifest{}, fmt.Errorf("enter transmission: %w", err)
	}
	if cl.Size != uint64(parent.Size) {
		return local.Manifest{}, fmt.Errorf("disk size changed since parent (%d != %d)", cl.Size, parent.Size)
	}
	extents, err := cl.DirtyExtents(ctx)
	if err != nil {
		return local.Manifest{}, fmt.Errorf("block status: %w", err)
	}

	m := local.Manifest{ID: id, VM: vm, Parent: parentID, Size: int64(cl.Size), Created: time.Now().Unix()}
	for _, e := range extents {
		end := e.Offset + uint64(e.Length)
		var hashes []string
		for pos := e.Offset; pos < end; {
			n := uint64(readWindow)
			if pos+n > end {
				n = end - pos
			}
			// ponytail: chunking per window splits CDC boundaries at window
			// edges; correctness unaffected (chunks concatenate back), only
			// cross-backup dedupe rate dips at edges. Feed a streaming chunker
			// if dedupe ratio matters.
			buf, err := cl.ReadAt(pos, uint32(n))
			if err != nil {
				return local.Manifest{}, fmt.Errorf("read dirty at %d: %w", pos, err)
			}
			for _, c := range chunker.Chunks(buf, chunker.DefaultParams) {
				h, err := store.Put(c)
				if err != nil {
					return local.Manifest{}, err
				}
				hashes = append(hashes, h)
			}
			pos += n
		}
		m.Spans = append(m.Spans, local.Span{Off: int64(e.Offset), Chunks: hashes})
	}
	if err := store.PutManifest(m); err != nil {
		return local.Manifest{}, err
	}
	return m, nil
}

// dialExport starts a read-only qemu-nbd export of image (with dirty
// bitmap if bitmap != "") on a unix socket and dials it. Returns the
// client and a stop func that kills the export and removes the socket.
func dialExport(image, bitmap string) (*nbd.Client, func(), error) {
	sock := fmt.Sprintf("/tmp/og-nbd-%d.sock", os.Getpid())
	os.Remove(sock)
	// no --format: qemu-nbd probes qcow2/raw itself (same as the mount path);
	// forcing qcow2 would break raw disks.
	args := []string{"-r", "--socket=" + sock, image}
	if bitmap != "" {
		args = append(args, "--bitmap="+bitmap)
	}
	// root-owned images need sudo; umask 0 so the user can connect.
	script := "umask 0000; exec qemu-nbd"
	for _, a := range args {
		script += " '" + shellQuote(a) + "'"
	}
	cmd := exec.Command("sudo", "-n", "bash", "-c", script)
	if err := cmd.Start(); err != nil {
		return nil, nil, fmt.Errorf("start qemu-nbd export: %w", err)
	}
	stop := func() {
		// qemu-nbd exits by itself once the client disconnects; a clean
		// exit flushes data and persistent bitmaps. SIGKILL would leave
		// bitmaps inconsistent (loaded readonly next time), so it is only
		// the fallback for a stuck server.
		done := make(chan struct{})
		go func() { cmd.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			cmd.Process.Kill()
			cmd.Wait()
		}
		os.Remove(sock)
	}
	var cl *nbd.Client
	var err error
	for i := 0; i < exportTries; i++ {
		if cl, err = nbd.DialNBD("unix", sock, ""); err == nil {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if err != nil {
		stop()
		return nil, nil, fmt.Errorf("dial export %s: %w", image, err)
	}
	return cl, stop, nil
}

func shellQuote(s string) string {
	return strings.ReplaceAll(s, "'", `'\''`)
}
