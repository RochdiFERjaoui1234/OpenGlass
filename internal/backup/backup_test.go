package backup

import (
	"bytes"
	"crypto/rand"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/openglass/openglass/internal/repository/local"
)

func TestBackupRestoreRoundTrip(t *testing.T) {
	dir := t.TempDir()
	key := make([]byte, 32)
	rand.Read(key)
	store, err := local.Open(filepath.Join(dir, "repo"), key)
	if err != nil {
		t.Fatal(err)
	}
	src := filepath.Join(dir, "disk.img")
	data := make([]byte, 3<<20)
	rand.Read(data)
	os.WriteFile(src, data, 0o600)

	if _, err := RunFile(store, "vm1", src, "snap1"); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(dir, "restored.img")
	if _, err := RestoreFile(store, "snap1", dst); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(dst)
	if !bytes.Equal(got, data) {
		t.Fatal("restore mismatch")
	}

	var buf bytes.Buffer
	if err := ExportImage(store, "snap1", &buf); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(buf.Bytes(), data) {
		t.Fatal("export mismatch")
	}
}

// TestIncrementalRestoreReplaysSpans builds a full point plus a synthetic
// incremental (spans over dirty ranges) and checks RestoreFile overlays
// them onto the base, and ExportImage does the same through *os.File.
func TestIncrementalRestoreReplaysSpans(t *testing.T) {
	dir := t.TempDir()
	key := make([]byte, 32)
	rand.Read(key)
	store, err := local.Open(filepath.Join(dir, "repo"), key)
	if err != nil {
		t.Fatal(err)
	}
	base := make([]byte, 1<<20)
	rand.Read(base)
	src := filepath.Join(dir, "disk.img")
	os.WriteFile(src, base, 0o600)
	if _, err := RunFile(store, "vm1", src, "full1"); err != nil {
		t.Fatal(err)
	}

	// expected image after two dirty spans (overlapping the base)
	want := append([]byte(nil), base...)
	span1 := []byte("changed-region-one-aaaaaaaa")
	copy(want[1000:], span1)
	span2 := []byte("changed-region-two-bbbbbbbb")
	copy(want[900000:], span2)

	h1, err := store.Put(span1)
	if err != nil {
		t.Fatal(err)
	}
	h2, err := store.Put(span2)
	if err != nil {
		t.Fatal(err)
	}
	spans := []local.Span{
		{Off: 1000, Chunks: []string{h1}},
		{Off: 900000, Chunks: []string{h2}},
	}
	inc := local.Manifest{ID: "inc1", VM: "vm1", Parent: "full1", Size: int64(len(base)), Spans: spans, Created: time.Now().Unix()}
	if err := store.PutManifest(inc); err != nil {
		t.Fatal(err)
	}

	dst := filepath.Join(dir, "merged.img")
	if _, err := RestoreFile(store, "inc1", dst); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(dst)
	if !bytes.Equal(got, want) {
		t.Fatalf("span replay mismatch: len got=%d want=%d", len(got), len(want))
	}

	// ExportImage must take the same path for incremental points.
	out := filepath.Join(dir, "exported.img")
	f, err := os.Create(out)
	if err != nil {
		t.Fatal(err)
	}
	if err := ExportImage(store, "inc1", f); err != nil {
		t.Fatal(err)
	}
	f.Close()
	got, _ = os.ReadFile(out)
	if !bytes.Equal(got, want) {
		t.Fatal("export span replay mismatch")
	}

	// writer-based Restore must still refuse incrementals (no seeking).
	if _, err := Restore(store, "inc1", &bytes.Buffer{}); err == nil {
		t.Fatal("expected Restore to refuse incremental")
	}

	// chain-over-incremental: a newer span at the same offset must win,
	// older unrelated spans stay.
	span3 := []byte("newer-region-one-ccccccccc")
	h3, err := store.Put(span3)
	if err != nil {
		t.Fatal(err)
	}
	inc2 := local.Manifest{ID: "inc2", VM: "vm1", Parent: "inc1", Size: int64(len(base)),
		Spans: []local.Span{{Off: 1000, Chunks: []string{h3}}}, Created: time.Now().Unix()}
	if err := store.PutManifest(inc2); err != nil {
		t.Fatal(err)
	}
	want2 := append([]byte(nil), want...)
	copy(want2[1000:], span3)
	dst2 := filepath.Join(dir, "merged2.img")
	if _, err := RestoreFile(store, "inc2", dst2); err != nil {
		t.Fatal(err)
	}
	got2, _ := os.ReadFile(dst2)
	if !bytes.Equal(got2, want2) {
		t.Fatal("chain span replay mismatch")
	}
}
