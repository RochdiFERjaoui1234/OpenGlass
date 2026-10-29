// Package libvirt implements snapshot.Provider by shelling out to
// virsh/qemu-img (MVP choice: no cgo, debuggable, matches the spec).
package libvirt

import (
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"strings"

	"github.com/openglass/openglass/internal/snapshot"
)

type Provider struct {
	// Run executes a command; replaceable in tests.
	Run func(name string, args ...string) ([]byte, error)
}

func New() *Provider {
	return &Provider{Run: func(name string, args ...string) ([]byte, error) {
		// system libvirt needs root; NOPASSWD sudoers entry required
		// (same assumption as the NBD export path in backup/disk.go).
		if name == "virsh" || name == "qemu-img" || name == "qemu-nbd" {
			args = append([]string{"-n", name}, args...)
			name = "sudo"
		}
		cmd := exec.Command(name, args...)
		return cmd.CombinedOutput()
	}}
}

func (p *Provider) CreateSnapshot(vm string) (string, error) {
	id := vm + "-openglass"
	out, err := p.Run("virsh", "snapshot-create-as", vm, id, "--disk-only", "--atomic")
	if err != nil {
		return "", fmt.Errorf("virsh snapshot-create-as: %w: %s", err, out)
	}
	return id, nil
}

// ChangedBlocks reports dirty ranges from the previous checkpoint.
// Uses qemu-img's dirty-bitmap inspection of the overlay; MVP returns
// a single full-disk range unless a prior checkpoint exists.
func (p *Provider) ChangedBlocks(vm, sinceID string) ([]snapshot.Range, error) {
	if sinceID == "" {
		return nil, nil // nil slice means "full read"
	}
	out, err := p.Run("qemu-img", "check", "--dirty-bitmaps", "--output=json", vm)
	if err != nil {
		return nil, fmt.Errorf("qemu-img check: %w: %s", err, out)
	}
	_ = out
	// TODO: parse qemu-img JSON for dirty ranges; for MVP, full read.
	return nil, nil
}

func (p *Provider) Read(vm, snapshotID string) (io.ReadCloser, error) {
	out, err := p.Run("virsh", "domblklist", vm, "--details")
	if err != nil {
		return nil, fmt.Errorf("virsh domblklist: %w: %s", err, out)
	}
	_ = out
	return nil, fmt.Errorf("use transport/nbd to stream disk data (MVP)")
}

func (p *Provider) DeleteSnapshot(vm, id string) error {
	out, err := p.Run("virsh", "snapshot-delete", vm, id, "--metadata")
	if err != nil {
		return fmt.Errorf("virsh snapshot-delete: %w: %s", err, out)
	}
	return nil
}


// DirtyBitmap is the persistent bitmap name openglass relies on.
const DirtyBitmap = "openglass"

// EnsureDirtyBitmap adds the persistent per-disk dirty bitmap (idempotent-ish:
// ignores the "already exists" error from QEMU).
func (p *Provider) EnsureDirtyBitmap(vm string) error {
	out, err := p.Run("virsh", "qemu-monitor-command", vm, "--hmp",
		fmt.Sprintf("block-dirty-bitmap-add node0 %s persistent", DirtyBitmap))
	if err != nil && !strings.Contains(string(out), "already exists") {
		return fmt.Errorf("bitmap-add: %w: %s", err, out)
	}
	return nil
}

// ClearDirtyBitmap resets the bitmap after a successful incremental backup.
func (p *Provider) ClearDirtyBitmap(vm string) error {
	out, err := p.Run("virsh", "qemu-monitor-command", vm, "--hmp",
		fmt.Sprintf("block-dirty-bitmap-clear node0 %s", DirtyBitmap))
	if err != nil {
		return fmt.Errorf("bitmap-clear: %w: %s", err, out)
	}
	return nil
}

// DomState returns the domain state string ("running", "paused", ...).
func (p *Provider) DomState(vm string) (string, error) {
	out, err := p.Run("virsh", "domstate", vm)
	if err != nil {
		return "", fmt.Errorf("virsh domstate: %w: %s", err, out)
	}
	return strings.TrimSpace(string(out)), nil
}

// Suspend pauses the domain (for a consistent snapshot window).
func (p *Provider) Suspend(vm string) error {
	out, err := p.Run("virsh", "suspend", vm)
	if err != nil {
		return fmt.Errorf("virsh suspend: %w: %s", err, out)
	}
	return nil
}

// Resume unpauses the domain.
func (p *Provider) Resume(vm string) error {
	out, err := p.Run("virsh", "resume", vm)
	if err != nil {
		return fmt.Errorf("virsh resume: %w: %s", err, out)
	}
	return nil
}

// SnapshotDiskOnly takes a disk-only snapshot; the pre-snapshot image is
// frozen and safe to export while the guest runs on the new overlay.
func (p *Provider) SnapshotDiskOnly(vm, snap string) error {
	out, err := p.Run("virsh", "snapshot-create-as", vm, snap, "--disk-only", "--atomic")
	if err != nil {
		return fmt.Errorf("virsh snapshot-create-as: %w: %s", err, out)
	}
	return nil
}

// SnapshotDeleteMetadata removes snapshot metadata after a blockcommit pivot.
func (p *Provider) SnapshotDeleteMetadata(vm, snap string) error {
	out, err := p.Run("virsh", "snapshot-delete", vm, snap, "--metadata")
	if err != nil {
		return fmt.Errorf("virsh snapshot-delete: %w: %s", err, out)
	}
	return nil
}

// BlockcommitActive merges the overlay back into the active image and pivots.
func (p *Provider) BlockcommitActive(vm, disk string) error {
	out, err := p.Run("virsh", "blockcommit", vm, disk, "--active", "--pivot", "--wait")
	if err != nil {
		return fmt.Errorf("virsh blockcommit: %w: %s", err, out)
	}
	return nil
}

// ActiveImage returns the current source path of disk (e.g. "vda").
func (p *Provider) ActiveImage(vm, disk string) (string, error) {
	out, err := p.Run("virsh", "domblklist", vm)
	if err != nil {
		return "", fmt.Errorf("virsh domblklist: %w: %s", err, out)
	}
	for _, line := range strings.Split(string(out), "\n") {
		f := strings.Fields(line)
		if len(f) >= 2 && f[0] == disk {
			return f[1], nil
		}
	}
	return "", fmt.Errorf("disk %s not found in domblklist: %s", disk, out)
}

// QMP sends a raw QMP command to the domain and errors if the response
// carries one.
func (p *Provider) QMP(vm, cmdJSON string) error {
	out, err := p.Run("virsh", "qemu-monitor-command", vm, "--pretty", cmdJSON)
	if err != nil {
		return fmt.Errorf("qmp: %w: %s", err, out)
	}
	var resp struct {
		Error *struct {
			Class string `json:"class"`
			Desc  string `json:"desc"`
		} `json:"error"`
	}
	if err := json.Unmarshal(out, &resp); err != nil {
		return fmt.Errorf("qmp bad response: %w: %s", err, out)
	}
	if resp.Error != nil {
		return fmt.Errorf("qmp %s: %s", resp.Error.Class, resp.Error.Desc)
	}
	return nil
}

// DiskNode returns the top block node name of the domain's first disk
// (parsed from HMP `info block`). ponytail: single-disk VMs; the first
// entry is the vda format node.
func (p *Provider) DiskNode(vm string) (string, error) {
	out, err := p.Run("virsh", "qemu-monitor-command", vm, "--hmp", "info block")
	if err != nil {
		return "", fmt.Errorf("info block: %w: %s", err, out)
	}
	line, _, _ := strings.Cut(string(out), "\n")
	name, _, _ := strings.Cut(line, ":")
	if name = strings.TrimSpace(name); name == "" {
		return "", fmt.Errorf("no block node in: %q", out)
	}
	return name, nil
}

// BitmapAdd creates a dirty bitmap on node (persistent = survives QEMU
// restarts via the image file). Ignores "already exists".
func (p *Provider) BitmapAdd(vm, node, name string, persistent bool) error {
	persist := "false"
	if persistent {
		persist = "true"
	}
	cmd := fmt.Sprintf(`{"execute":"block-dirty-bitmap-add","arguments":{"node":%q,"name":%q,"persistent":%s}}`,
		node, name, persist)
	if err := p.QMP(vm, cmd); err != nil && !strings.Contains(err.Error(), "already exists") {
		return err
	}
	return nil
}

// BitmapRemove deletes a dirty bitmap from node. Ignores "not found".
func (p *Provider) BitmapRemove(vm, node, name string) error {
	cmd := fmt.Sprintf(`{"execute":"block-dirty-bitmap-remove","arguments":{"node":%q,"name":%q}}`,
		node, name)
	if err := p.QMP(vm, cmd); err != nil && !strings.Contains(err.Error(), "not found") {
		return err
	}
	return nil
}

var _ snapshot.Provider = (*Provider)(nil)
