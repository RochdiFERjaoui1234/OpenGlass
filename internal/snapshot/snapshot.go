// Package snapshot defines the hypervisor snapshot provider interface
// (the open-source analogue of Veeam's VADP/CBT plugin).
package snapshot

import "io"

type Provider interface {
	// CreateSnapshot quiesces and snapshots vm, returns the snapshot ID.
	CreateSnapshot(vm string) (id string, err error)
	// ChangedBlocks lists block ranges dirtied since sinceID (nil = full disk).
	ChangedBlocks(vm, sinceID string) ([]Range, error)
	// Read opens a stream of the vm disk at snapshotID.
	Read(vm, snapshotID string) (io.ReadCloser, error)
	// DeleteSnapshot merges/releases the snapshot.
	DeleteSnapshot(vm, id string) error
}

// Range is a byte-offset span in the virtual disk.
type Range struct{ Offset, Length int64 }
