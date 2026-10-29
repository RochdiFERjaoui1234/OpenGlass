package local

import (
	"bytes"
	"crypto/rand"
	"testing"
)

func key(t *testing.T) []byte {
	k := make([]byte, 32)
	rand.Read(k)
	return k
}

func TestPutGetRoundTrip(t *testing.T) {
	s, err := Open(t.TempDir(), key(t))
	if err != nil {
		t.Fatal(err)
	}
	data := make([]byte, 1<<20)
	rand.Read(data)
	h, err := s.Put(data)
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.Get(h)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, data) {
		t.Fatal("round trip mismatch")
	}
}

func TestDedupeSkipsExisting(t *testing.T) {
	s, _ := Open(t.TempDir(), key(t))
	data := []byte("hello world")
	h1, _ := s.Put(data)
	h2, _ := s.Put(data)
	if h1 != h2 {
		t.Fatal("same content, different hash")
	}
}

func TestManifestAndPrune(t *testing.T) {
	s, _ := Open(t.TempDir(), key(t))
	a := []byte("chunk-a")
	b := []byte("chunk-b")
	ha, _ := s.Put(a)
	hb, _ := s.Put(b)
	m := Manifest{ID: "p1", VM: "vm1", Chunks: []string{ha, hb}, Size: int64(len(a) + len(b))}
	if err := s.PutManifest(m); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetManifest("p1")
	if err != nil || len(got.Chunks) != 2 {
		t.Fatalf("manifest: %v %v", got, err)
	}
	hC, _ := s.Put([]byte("orphan"))
	removed, err := s.Prune(map[string]bool{"p1": true})
	if err != nil {
		t.Fatal(err)
	}
	if removed != 0 && !s.Has(ha) || !s.Has(hb) {
		t.Fatal("prune removed live chunks")
	}
	if s.Has(hC) {
		t.Fatal("orphan chunk not swept")
	}
	if removed != 1 {
		t.Fatalf("expected 1 removed, got %d", removed)
	}
}

// TestPrunePinsSpanChunks: incremental points reference chunks through
// spans, not Chunks; Prune must pin both or GC eats dirty data.
func TestPrunePinsSpanChunks(t *testing.T) {
	s, _ := Open(t.TempDir(), key(t))
	base, _ := s.Put([]byte("base-data"))
	dirty, _ := s.Put([]byte("dirty-data"))
	if err := s.PutManifest(Manifest{ID: "full1", VM: "vm1", Chunks: []string{base}, Size: 9}); err != nil {
		t.Fatal(err)
	}
	if err := s.PutManifest(Manifest{ID: "inc1", VM: "vm1", Parent: "full1", Size: 9,
		Spans: []Span{{Off: 4, Chunks: []string{dirty}}}}); err != nil {
		t.Fatal(err)
	}
	removed, err := s.Prune(map[string]bool{"full1": true, "inc1": true})
	if err != nil {
		t.Fatal(err)
	}
	if removed != 0 || !s.Has(dirty) {
		t.Fatalf("span chunk swept (removed=%d)", removed)
	}
	// drop the incremental: its span chunk becomes garbage
	removed, err = s.Prune(map[string]bool{"full1": true})
	if err != nil {
		t.Fatal(err)
	}
	if removed != 1 || s.Has(dirty) {
		t.Fatalf("span chunk not swept after dropping inc (removed=%d)", removed)
	}
}
