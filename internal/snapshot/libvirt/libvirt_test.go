package libvirt

import (
	"errors"
	"strings"
	"testing"
)

// TestDirtyBitmapHMPFlow verifies the HMP wiring for creating and clearing
// the persistent dirty bitmap on a running domain.
func TestDirtyBitmapHMPFlow(t *testing.T) {
	var calls []string
	p := &Provider{Run: func(name string, args ...string) ([]byte, error) {
		calls = append(calls, name+" "+args[0])
		switch {
		case name == "virsh" && len(args) >= 4 && args[3] == "block-dirty-bitmap-add node0 "+DirtyBitmap+" persistent":
			return []byte("{}"), nil
		case name == "virsh" && len(args) >= 4 && args[3] == "block-dirty-bitmap-clear node0 "+DirtyBitmap:
			return []byte("{}"), nil
		}
		return nil, errors.New("unexpected: " + name + " " + args[0])
	}}
	if err := p.EnsureDirtyBitmap("vm1"); err != nil {
		t.Fatal(err)
	}
	if err := p.ClearDirtyBitmap("vm1"); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 2 {
		t.Fatalf("calls: %v", calls)
	}
	if calls[0] != "virsh qemu-monitor-command" || calls[1] != "virsh qemu-monitor-command" {
		t.Fatalf("order: %v", calls)
	}
}

func qmpProvider(resp string, err error) *Provider {
	return &Provider{Run: func(name string, args ...string) ([]byte, error) {
		return []byte(resp), err
	}}
}

func TestQMPParsesErrorResponses(t *testing.T) {
	ok := qmpProvider(`{"return": {}, "id": "libvirt-1"}`, nil)
	if err := ok.QMP("vm1", `{"execute":"x"}`); err != nil {
		t.Fatal(err)
	}
	bad := qmpProvider(`{"error": {"class": "GenericError", "desc": "Bitmap 'b' already exists"}}`, nil)
	err := bad.QMP("vm1", `{"execute":"x"}`)
	if err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("want desc in error, got %v", err)
	}
	if err := qmpProvider(`not json`, nil).QMP("vm1", `{}`); err == nil {
		t.Fatal("expected bad-response error")
	}
}

func TestBitmapAddRemoveIdempotent(t *testing.T) {
	exists := qmpProvider(`{"error": {"class": "GenericError", "desc": "Bitmap 'b' already exists"}}`, nil)
	if err := exists.BitmapAdd("vm1", "node0", "b", true); err != nil {
		t.Fatalf("add must ignore exists: %v", err)
	}
	missing := qmpProvider(`{"error": {"class": "GenericError", "desc": "Bitmap 'b' not found"}}`, nil)
	if err := missing.BitmapRemove("vm1", "node0", "b"); err != nil {
		t.Fatalf("remove must ignore not-found: %v", err)
	}
	other := qmpProvider(`{"error": {"class": "GenericError", "desc": "boom"}}`, nil)
	if err := other.BitmapAdd("vm1", "node0", "b", true); err == nil {
		t.Fatal("expected real errors to propagate")
	}
}

func TestDiskNodeParse(t *testing.T) {
	p := qmpProvider("libvirt-1-format: /img/x.qcow2 (qcow2)\n    Attached to: foo\n", nil)
	got, err := p.DiskNode("vm1")
	if err != nil || got != "libvirt-1-format" {
		t.Fatalf("parse: %q %v", got, err)
	}
	if _, err := qmpProvider("\n", nil).DiskNode("vm1"); err == nil {
		t.Fatal("expected empty-output error")
	}
}
