// Live backup of running domains: suspend -> disk-only snapshot -> read the
// frozen parent -> blockcommit back -> resume. The guest is paused only
// across the freeze/commit window, so no write can slip between the bitmap
// and the export; downtime is bounded by the dirty-data read plus commit.
//
// Verified on QEMU 6.2/libvirt 8.0: the frozen parent opens read-only in
// qemu-nbd while the guest runs on the overlay, and the checkpoint bitmap
// (flushed into the parent at snapshot time) reports the dirt.
package backup

import (
	"fmt"
	"os"

	"github.com/openglass/openglass/internal/repository/local"
	"github.com/openglass/openglass/internal/snapshot/libvirt"
)

const liveDisk = "vda" // ponytail: single-disk VMs; multi-disk needs per-disk specs

// RunDiskLive backs up the full guest-logical disk of a RUNNING domain,
// leaving a fresh empty bitmap og-<id> for the next incremental cycle.
func RunDiskLive(store *local.Store, p *libvirt.Provider, vm, id string) (local.Manifest, error) {
	node, err := liveNode(p, vm)
	if err != nil {
		return local.Manifest{}, err
	}
	var m local.Manifest
	err = withFrozenParent(p, vm, "og-"+id,
		func(parent string) error {
			var err error
			m, err = backupFull(store, vm, id, parent)
			return err
		},
		func() error {
			return p.BitmapAdd(vm, node, "og-"+id, true)
		})
	if err != nil {
		return local.Manifest{}, err
	}
	return m, nil
}

// RunIncrementalLive backs up the ranges dirty in bitmap since parentID from
// a RUNNING domain, and leaves a fresh empty bitmap og-<id> for the next
// cycle. Convention: pass bitmap og-<parentID> (printed by the previous
// cycle); the consumed bitmap is removed on a best-effort basis.
//
// Why monotonic names: QEMU 6.2 snapshot freezes leave consumed persistent
// bitmaps readonly/inconsistent, so in-place remove+add rotation is
// unreliable. Reading works in every flag state and spans replay
// chronologically, so a new bitmap per cycle is always correct; stale ones
// cost ~100 bytes each and are removed when removable.
func RunIncrementalLive(store *local.Store, p *libvirt.Provider, vm, parentID, bitmap, id string) (local.Manifest, error) {
	if parentID == "" {
		return local.Manifest{}, fmt.Errorf("incremental requires parent")
	}
	if bitmap == "" {
		return local.Manifest{}, fmt.Errorf("incremental requires dirty bitmap name")
	}
	if _, err := store.GetManifest(parentID); err != nil {
		return local.Manifest{}, fmt.Errorf("parent %s: %w", parentID, err)
	}
	node, err := liveNode(p, vm)
	if err != nil {
		return local.Manifest{}, err
	}
	// ensure the read bitmap exists (first cycle after a file full needs it
	// pre-created; a fresh one simply yields an empty delta).
	if err := p.BitmapAdd(vm, node, bitmap, true); err != nil {
		return local.Manifest{}, err
	}
	var m local.Manifest
	err = withFrozenParent(p, vm, "og-"+id,
		func(parent string) error {
			var err error
			m, err = backupSpans(store, vm, parentID, id, parent, bitmap)
			if err != nil {
				return err
			}
			_ = p.BitmapRemove(vm, node, bitmap) // best-effort GC
			return nil
		},
		func() error {
			return p.BitmapAdd(vm, node, "og-"+id, true)
		})
	if err != nil {
		return local.Manifest{}, err
	}
	return m, nil
}

// liveNode checks the domain is up and returns its disk node name.
func liveNode(p *libvirt.Provider, vm string) (string, error) {
	st, err := p.DomState(vm)
	if err != nil {
		return "", err
	}
	if st != "running" && st != "paused" {
		return "", fmt.Errorf("live backup requires a running domain (state %s)", st)
	}
	return p.DiskNode(vm)
}

// withFrozenParent suspends vm (unless already paused), snapshots its disk,
// runs pre on the frozen parent image path, commits the overlay back,
// deletes the snapshot metadata plus overlay file, runs post (still
// suspended, e.g. to arm the next cycle's bitmap with no write window),
// and resumes. Resume is deferred and runs on every path after a suspend
// we performed. post may be nil.
func withFrozenParent(p *libvirt.Provider, vm, snap string, pre func(parent string) error, post func() error) error {
	st, err := p.DomState(vm)
	if err != nil {
		return err
	}
	if st != "running" && st != "paused" {
		return fmt.Errorf("live backup requires a running domain (state %s)", st)
	}
	parent0, err := p.ActiveImage(vm, liveDisk)
	if err != nil {
		return err
	}
	ours := st == "running"
	if ours {
		if err := p.Suspend(vm); err != nil {
			return err
		}
		defer func() {
			if ours {
				ours = false
				_ = p.Resume(vm)
			}
		}()
	}
	if err := p.SnapshotDiskOnly(vm, snap); err != nil {
		return err
	}
	if err := pre(parent0); err != nil {
		// best-effort unwind: collapse the overlay so a failed backup
		// leaves the domain clean (never remove files here — on a failed
		// commit the overlay may still hold unmerged data).
		_ = p.BlockcommitActive(vm, liveDisk)
		_ = p.SnapshotDeleteMetadata(vm, snap)
		return err
	}
	overlay, err := p.ActiveImage(vm, liveDisk) // overlay == active image now
	if err != nil {
		return err
	}
	if err := p.BlockcommitActive(vm, liveDisk); err != nil {
		return err
	}
	if err := p.SnapshotDeleteMetadata(vm, snap); err != nil {
		return err
	}
	_ = os.Remove(overlay) // best-effort: pivot usually leaves the file
	if post != nil {
		if err := post(); err != nil {
			return err
		}
	}
	if ours {
		if err := p.Resume(vm); err != nil {
			return err
		}
		ours = false
	}
	return nil
}
