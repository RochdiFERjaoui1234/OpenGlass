// Package backup implements the end-to-end engine: stream source bytes
// through CDC chunking into a local.Store, and reconstruct them back.
package backup

import (
	"fmt"
	"io"
	"os"
	"time"

	"github.com/openglass/openglass/internal/chunker"
	"github.com/openglass/openglass/internal/repository/local"
)

// Run backs up src into store as a new restore point for vm.
// Returns the manifest. Incremental behavior (dirty-bitmap readers) plugs
// in at the src reader level; MVP always reads the full stream.
func Run(store *local.Store, vm string, src io.Reader, id string) (local.Manifest, error) {
	data, err := io.ReadAll(src)
	if err != nil {
		return local.Manifest{}, err
	}
	m := local.Manifest{ID: id, VM: vm, Size: int64(len(data)), Created: time.Now().Unix()}
	for _, c := range chunker.Chunks(data, chunker.DefaultParams) {
		h, err := store.Put(c)
		if err != nil {
			return local.Manifest{}, err
		}
		m.Chunks = append(m.Chunks, h)
	}
	if err := store.PutManifest(m); err != nil {
		return local.Manifest{}, err
	}
	return m, nil
}

// Restore streams a full point's chunks into w. Incremental points
// contain only spans and need seeking — use RestoreFile or ExportImage.
func Restore(store *local.Store, id string, w io.Writer) (local.Manifest, error) {
	m, err := store.GetManifest(id)
	if err != nil {
		return local.Manifest{}, err
	}
	if m.Parent != "" {
		return local.Manifest{}, fmt.Errorf("point %s is incremental (parent %s); use RestoreFile to replay spans over the base", id, m.Parent)
	}
	return m, writeChunks(store, m, w)
}

// writeChunks streams m's ordered chunks into w.
func writeChunks(store *local.Store, m local.Manifest, w io.Writer) error {
	for _, h := range m.Chunks {
		c, err := store.Get(h)
		if err != nil {
			return err
		}
		if _, err := w.Write(c); err != nil {
			return err
		}
	}
	return nil
}

// materialize writes restore point m to seekable file f: the full base
// first (walking the parent chain), then each ancestor's dirty spans in
// chronological order, finally sized to the logical disk.
func materialize(store *local.Store, m local.Manifest, f *os.File) error {
	var chain []local.Manifest
	for cur := m; ; {
		chain = append([]local.Manifest{cur}, chain...)
		if cur.Parent == "" {
			break
		}
		var err error
		cur, err = store.GetManifest(cur.Parent)
		if err != nil {
			return fmt.Errorf("parent %s: %w", m.Parent, err)
		}
	}
	if err := writeChunks(store, chain[0], f); err != nil {
		return err
	}
	for _, pt := range chain[1:] {
		for _, sp := range pt.Spans {
			if _, err := f.Seek(sp.Off, io.SeekStart); err != nil {
				return err
			}
			for _, h := range sp.Chunks {
				c, err := store.Get(h)
				if err != nil {
					return err
				}
				if _, err := f.Write(c); err != nil {
					return err
				}
			}
		}
	}
	return f.Truncate(m.Size)
}

// RunIncremental is gone: disk incrementals use RunIncrementalNBD
// (dirty-bitmap spans over a full NBD base) in disk.go.

// ExportImage materializes a restore point's bytes into w. Incremental
// points need a seekable file (*os.File) so spans can be overlaid.
func ExportImage(store *local.Store, id string, w io.Writer) error {
	m, err := store.GetManifest(id)
	if err != nil {
		return err
	}
	if m.Parent == "" {
		return writeChunks(store, m, w)
	}
	f, ok := w.(*os.File)
	if !ok {
		return fmt.Errorf("incremental point %s requires export to a file (got %T)", id, w)
	}
	return materialize(store, m, f)
}

// Back up a file path.
func RunFile(store *local.Store, vm, path, id string) (local.Manifest, error) {
	f, err := os.Open(path)
	if err != nil {
		return local.Manifest{}, err
	}
	defer f.Close()
	return Run(store, vm, f, id)
}

// Restore to a file path (truncating); incremental points replay their
// spans over the full base automatically.
func RestoreFile(store *local.Store, id, path string) (local.Manifest, error) {
	m, err := store.GetManifest(id)
	if err != nil {
		return local.Manifest{}, err
	}
	f, err := os.Create(path)
	if err != nil {
		return local.Manifest{}, err
	}
	defer f.Close()
	if err := materialize(store, m, f); err != nil {
		return local.Manifest{}, err
	}
	return m, nil
}
