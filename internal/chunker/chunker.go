// Package chunker splits byte streams into content-defined chunks (CDC).
package chunker

import (
	"math/bits"
	"math/rand"
)

// Params controls chunk size targets (bytes, powers of two recommended).
type Params struct {
	Min, Avg, Max uint32
}

var DefaultParams = Params{Min: 1 << 19, Avg: 1 << 20, Max: 1 << 23}

var gear [256]uint64

func init() {
	r := rand.New(rand.NewSource(0x6F70656E676C6173)) // fixed seed => stable boundaries across runs
	for i := range gear {
		gear[i] = r.Uint64()
	}
}

// Chunks yields chunk byte slices over data in a single pass.
func Chunks(data []byte, p Params) [][]byte {
	var out [][]byte
	start := 0
	n := uint64(0)
	for i := 0; i < len(data); i++ {
		n = (n << 1) + gear[data[i]]
		size := uint32(i - start + 1)
		if size < p.Min {
			continue
		}
		mask := uint64(p.Avg) - 1
		if size >= p.Avg {
			// normalize boundary density around the target average
			mask = mask >> bits.LeadingZeros64(uint64(size)/uint64(p.Avg))
		}
		if n&mask == 0 || size >= p.Max {
			out = append(out, data[start:i+1])
			start = i + 1
			n = 0
		}
	}
	if start < len(data) {
		out = append(out, data[start:])
	}
	return out
}
