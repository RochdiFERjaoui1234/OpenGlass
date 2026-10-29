package backup

import (
	"crypto/rand"
	"path/filepath"
	"strings"
	"testing"

	"github.com/openglass/openglass/internal/repository/local"
)

func TestRunIncrementalNBDValidations(t *testing.T) {
	dir := t.TempDir()
	key := make([]byte, 32)
	rand.Read(key)
	store, err := local.Open(filepath.Join(dir, "repo"), key)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := RunIncrementalNBD(store, "vm1", "", "img", "bm", "id1"); err == nil || !strings.Contains(err.Error(), "parent") {
		t.Fatalf("want parent error, got %v", err)
	}
	if _, err := RunIncrementalNBD(store, "vm1", "missing", "img", "", "id1"); err == nil || !strings.Contains(err.Error(), "bitmap") {
		t.Fatalf("want bitmap error, got %v", err)
	}
	full := local.Manifest{ID: "full1", VM: "vm1", Size: 4096}
	if err := store.PutManifest(full); err != nil {
		t.Fatal(err)
	}
	inc := local.Manifest{ID: "inc1", VM: "vm1", Parent: "full1", Size: 4096}
	if err := store.PutManifest(inc); err != nil {
		t.Fatal(err)
	}
	// chaining onto an incremental is allowed; only the live export can
	// fail (no qemu-nbd/image here — must mention "export")
	if _, err := RunIncrementalNBD(store, "vm1", "inc1", "img", "bm", "id1"); err == nil || !strings.Contains(err.Error(), "export") {
		t.Fatalf("want export error, got %v", err)
	}

	// missing parent blocks restore with a clear error (checked pre-dial)
	inc.Parent = "gone"
	if err := store.PutManifest(inc); err != nil {
		t.Fatal(err)
	}
	if _, err := RestoreFile(store, "inc1", filepath.Join(dir, "out.raw")); err == nil || !strings.Contains(err.Error(), "parent") {
		t.Fatalf("want parent error, got %v", err)
	}
}
