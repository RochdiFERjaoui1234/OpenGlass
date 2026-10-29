package main

import (
	"fmt"
	"os"

	"github.com/openglass/openglass/internal/transport/nbd"
)

func main() {
	if len(os.Args) < 3 {
		fmt.Println("usage: nbdtest <socket> <export> [bitmap]")
		os.Exit(2)
	}
	bitmap := "cp1"
	if len(os.Args) > 3 {
		bitmap = os.Args[3]
	}
	cl, err := nbd.DialNBD("unix", os.Args[1], os.Args[2])
	if err != nil {
		fmt.Fprintln(os.Stderr, "dial:", err)
		os.Exit(1)
	}
	defer cl.Close()

	got, err := cl.SetMetaContext(os.Args[2], "qemu:dirty-bitmap:"+bitmap, "base:allocation")
	if err != nil {
		fmt.Fprintln(os.Stderr, "set-meta:", err)
		os.Exit(1)
	}
	fmt.Printf("selected contexts: %v\n", got)

	if err := cl.EnterTransmission(os.Args[2]); err != nil {
		fmt.Fprintln(os.Stderr, "go:", err)
		os.Exit(1)
	}
	fmt.Printf("export size: %d\n", cl.Size)

	if len(os.Args) > 4 {
		var off uint64
		fmt.Sscanf(os.Args[4], "%d", &off)
		buf := make([]byte, 4096)
		for i := range buf {
			buf[i] = byte(i)
		}
		if err := cl.WriteAt(off, buf); err != nil {
			fmt.Fprintln(os.Stderr, "write:", err)
			os.Exit(1)
		}
		fmt.Printf("wrote 4096 bytes at %#x\n", off)
	}

	exts, err := cl.DirtyExtents("qemu:dirty-bitmap:" + bitmap)
	if err != nil {
		fmt.Fprintln(os.Stderr, "dirty extents:", err)
		os.Exit(1)
	}
	fmt.Printf("dirty extents: %d\n", len(exts))
	for i, e := range exts {
		if i >= 10 {
			fmt.Printf("... (%d more)\n", len(exts)-10)
			break
		}
		fmt.Printf("  off=%#x len=%d flags=%#x\n", e.Offset, e.Length, e.Flags)
	}

	if cl.Size > 0 {
		data, err := cl.ReadAt(4096, 64)
		if err != nil {
			fmt.Fprintln(os.Stderr, "read:", err)
			os.Exit(1)
		}
		fmt.Printf("read[4096:4160]: %x\n", data)
		if data[0] != 0 || data[1] != 1 || data[2] != 2 {
			fmt.Println("FAIL: pattern mismatch")
			os.Exit(1)
		}
	}
	fmt.Println("OK")
}
