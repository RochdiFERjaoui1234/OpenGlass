// Package worker executes backup jobs: full/incremental disk reads over
// NBD -> chunk/dedupe -> manifest.
package worker

import (
	"encoding/hex"
	"os"
	"path/filepath"
	"time"

	"github.com/openglass/openglass/internal/backup"
	"github.com/openglass/openglass/internal/queue"
	"github.com/openglass/openglass/internal/repository/local"
	"github.com/openglass/openglass/internal/snapshot/libvirt"
)

// Execute runs one job against a real (or fake) provider/store,
// stamping worker-measured start/finish times on the result.
func Execute(j queue.Job) queue.Result {
	start := time.Now()
	r := execute(j)
	r.Started = start
	r.Finished = time.Now()
	return r
}

func execute(j queue.Job) queue.Result {
	r := queue.Result{JobID: j.ID}
	key, err := os.ReadFile(filepath.Join(j.RepoDir, "key"))
	if err != nil {
		return queue.Result{JobID: j.ID, Status: "error", Message: err.Error()}
	}
	k, err := hex.DecodeString(string(key))
	if err != nil {
		return queue.Result{JobID: j.ID, Status: "error", Message: "bad key"}
	}
	store, err := local.Open(j.RepoDir, k)
	if err != nil {
		return queue.Result{JobID: j.ID, Status: "error", Message: err.Error()}
	}
	// live jobs address the domain (no Src); file jobs need Src (checked below).
	if j.Mode == "incremental" {
		if j.Parent == "" {
			return queue.Result{JobID: j.ID, Status: "error", Message: "incremental requires parent"}
		}
		bitmap := j.Bitmap
		if bitmap == "" {
			bitmap = libvirt.DirtyBitmap
		}
		var m local.Manifest
		var err error
		if j.Live {
			m, err = backup.RunIncrementalLive(store, libvirt.New(), j.VM, j.Parent, bitmap, j.ID)
		} else {
			if j.Src == "" {
				return queue.Result{JobID: j.ID, Status: "error", Message: "job requires src (disk image path)"}
			}
			m, err = backup.RunIncrementalNBD(store, j.VM, j.Parent, j.Src, bitmap, j.ID)
		}
		if err != nil {
			return queue.Result{JobID: j.ID, Status: "error", Message: err.Error()}
		}
		r.Status, r.PointID, r.Size = "ok", m.ID, m.Size
		return r
	}
	if j.Live {
		m, err := backup.RunDiskLive(store, libvirt.New(), j.VM, j.ID)
		if err != nil {
			return queue.Result{JobID: j.ID, Status: "error", Message: err.Error()}
		}
		r.Status, r.PointID, r.Size = "ok", m.ID, m.Size
		return r
	}
	if j.Src == "" {
		return queue.Result{JobID: j.ID, Status: "error", Message: "job requires src (disk image path)"}
	}
	m, err := backup.RunDisk(store, j.VM, j.Src, j.ID)
	if err != nil {
		return queue.Result{JobID: j.ID, Status: "error", Message: err.Error()}
	}
	r.Status, r.PointID, r.Size = "ok", m.ID, m.Size
	return r
}
