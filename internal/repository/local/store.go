// Package local implements a content-addressable repository on local disk.
//
// Layout:
//
//	repository/
//	  chunks/<hash-prefix>/<hash>   # compressed + encrypted chunk data
//	  snapshots/<restore-point-id>  # JSON manifest: ordered chunk hashes + metadata
//	  index/chunks.json             # hash -> location index (MVP: full map)
package local

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"sync"

	"github.com/klauspost/compress/zstd"
)

type Store struct {
	dir  string
	key  []byte // 32-byte AES-256 key
	mu   sync.Mutex
	enc  *zstd.Encoder
	dec  *zstd.Decoder
}

// Span is a dirty region of an incremental restore point: chunk hashes
// stored sequentially starting at Off.
type Span struct {
	Off    int64    `json:"off"`
	Chunks []string `json:"chunks"`
}

type Manifest struct {
	ID      string   `json:"id"`
	VM      string   `json:"vm"`
	Parent  string   `json:"parent,omitempty"` // previous restore point
	Chunks  []string `json:"chunks"`           // ordered chunk hashes (full points)
	Spans   []Span   `json:"spans,omitempty"`  // dirty regions (incremental points)
	Size    int64    `json:"size"`             // logical bytes
	Created int64    `json:"created"`          // unix seconds
}

func Open(dir string, key []byte) (*Store, error) {
	if len(key) != 32 {
		return nil, errors.New("key must be 32 bytes (AES-256)")
	}
	enc, err := zstd.NewWriter(nil)
	if err != nil {
		return nil, err
	}
	dec, err := zstd.NewReader(nil)
	if err != nil {
		return nil, err
	}
	for _, sub := range []string{"chunks", "snapshots", "index"} {
		if err := os.MkdirAll(filepath.Join(dir, sub), 0o755); err != nil {
			return nil, err
		}
	}
	return &Store{dir: dir, key: append([]byte(nil), key...), enc: enc, dec: dec}, nil
}

func Hash(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func chunkPath(dir, h string) string {
	return filepath.Join(dir, "chunks", h[:2], h)
}

// Has reports whether a chunk hash exists in the store.
func (s *Store) Has(h string) bool {
	_, err := os.Stat(chunkPath(s.dir, h))
	return err == nil
}

// Put compresses, encrypts and stores b; returns its content hash.
// Existing chunks are skipped (global dedupe).
func (s *Store) Put(b []byte) (string, error) {
	h := Hash(b)
	p := chunkPath(s.dir, h)
	if _, err := os.Stat(p); err == nil {
		return h, nil
	}
	comp := s.enc.EncodeAll(b, nil)
	ct, err := s.seal(comp)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return "", err
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, ct, 0o600); err != nil {
		return "", err
	}
	if err := os.Rename(tmp, p); err != nil {
		return "", err
	}
	return h, nil
}

// Get fetches, decrypts and decompresses a chunk.
func (s *Store) Get(h string) ([]byte, error) {
	ct, err := os.ReadFile(chunkPath(s.dir, h))
	if err != nil {
		return nil, err
	}
	comp, err := s.open(ct)
	if err != nil {
		return nil, err
	}
	return s.dec.DecodeAll(comp, nil)
}

func (s *Store) seal(plain []byte) ([]byte, error) {
	blk, err := aes.NewCipher(s.key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(blk)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}
	return append(nonce, gcm.Seal(nil, nonce, plain, nil)...), nil
}

func (s *Store) open(ct []byte) ([]byte, error) {
	blk, err := aes.NewCipher(s.key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(blk)
	if err != nil {
		return nil, err
	}
	ns := gcm.NonceSize()
	if len(ct) < ns {
		return nil, errors.New("short ciphertext")
	}
	return gcm.Open(nil, ct[:ns], ct[ns:], nil)
}

// PutManifest writes a restore-point manifest.
func (s *Store) PutManifest(m Manifest) error {
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(s.dir, "snapshots", m.ID+".json"), b, 0o600)
}

// GetManifest reads a manifest by ID.
func (s *Store) GetManifest(id string) (Manifest, error) {
	b, err := os.ReadFile(filepath.Join(s.dir, "snapshots", id+".json"))
	if err != nil {
		return Manifest{}, err
	}
	var m Manifest
	return m, json.Unmarshal(b, &m)
}

// ListManifests returns all manifest IDs.
func (s *Store) ListManifests() ([]string, error) {
	es, err := os.ReadDir(filepath.Join(s.dir, "snapshots"))
	if err != nil {
		return nil, err
	}
	var ids []string
	for _, e := range es {
		if filepath.Ext(e.Name()) == ".json" {
			ids = append(ids, e.Name()[:len(e.Name())-5])
		}
	}
	sort.Strings(ids)
	return ids, nil
}

// Prune removes manifests not in keep, then sweeps unreferenced chunks
// using a global lock file (two-phase GC à la restic).
func (s *Store) Prune(keep map[string]bool) (int, error) {
	pin := map[string]bool{}
	ids, err := s.ListManifests()
	if err != nil {
		return 0, err
	}
	for _, id := range ids {
		if keep[id] {
			m, err := s.GetManifest(id)
			if err != nil {
				return 0, err
			}
			for _, h := range m.Chunks {
				pin[h] = true
			}
			for _, sp := range m.Spans {
				for _, h := range sp.Chunks {
					pin[h] = true
				}
			}
			continue
		}
		if err := os.Remove(filepath.Join(s.dir, "snapshots", id+".json")); err != nil {
			return 0, err
		}
	}
	removed := 0
	err = filepath.Walk(filepath.Join(s.dir, "chunks"), func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		if !pin[filepath.Base(p)] {
			if err := os.Remove(p); err != nil {
				return err
			}
			removed++
		}
		return nil
	})
	return removed, err
}
