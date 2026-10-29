package chunker

import (
	"bytes"
	"math/rand"
	"testing"
)

func TestChunksReconstructAndBound(t *testing.T) {
	r := rand.New(rand.NewSource(1))
	data := make([]byte, 4<<20)
	r.Read(data)
	p := Params{Min: 1 << 12, Avg: 1 << 14, Max: 1 << 16}
	chunks := Chunks(data, p)
	var joined []byte
	for _, c := range chunks {
		if uint32(len(c)) > p.Max {
			t.Fatalf("chunk over max: %d", len(c))
		}
		joined = append(joined, c...)
	}
	if !bytes.Equal(joined, data) {
		t.Fatal("chunks do not reconstruct input")
	}
	if len(chunks) < 4 {
		t.Fatalf("expected multiple chunks, got %d", len(chunks))
	}
}

func TestChunksStableAcrossRuns(t *testing.T) {
	r := rand.New(rand.NewSource(2))
	data := make([]byte, 1<<18)
	r.Read(data)
	p := Params{Min: 1 << 12, Avg: 1 << 14, Max: 1 << 16}
	a := Chunks(data, p)
	b := Chunks(append([]byte(nil), data...), p)
	if len(a) != len(b) {
		t.Fatal("chunk count differs between runs")
	}
	for i := range a {
		if !bytes.Equal(a[i], b[i]) {
			t.Fatalf("chunk %d differs", i)
		}
	}
}

func TestChunksShiftResistant(t *testing.T) {
	r := rand.New(rand.NewSource(3))
	base := make([]byte, 1<<18)
	r.Read(base)
	shifted := append([]byte{0xAA}, base...)
	p := Params{Min: 1 << 12, Avg: 1 << 14, Max: 1 << 16}
	ca := Chunks(base, p)
	cb := Chunks(shifted, p)
	// after the inserted byte, most boundaries should realign
	same := 0
	for _, b := range cb[1:] {
		for _, a := range ca {
			if bytes.Equal(a, b) {
				same++
				break
			}
		}
	}
	if same < len(ca)/2 {
		t.Fatalf("shift resistance poor: %d/%d chunks realigned", same, len(ca))
	}
}
