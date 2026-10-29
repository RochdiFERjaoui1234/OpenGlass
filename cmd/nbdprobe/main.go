package main

import (
	"fmt"
	"net"
	"os"
	"time"
)

func main() {
	c, err := net.Dial("unix", os.Args[1])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer c.Close()
	var all []byte
	buf := make([]byte, 256)
	for {
		c.SetReadDeadline(time.Now().Add(700 * time.Millisecond))
		n, err := c.Read(buf)
		all = append(all, buf[:n]...)
		if err != nil {
			break
		}
	}
	fmt.Printf("total %d bytes: %x\n", len(all), all)
	// now send client flags and dump whatever comes
	c.Write([]byte{0, 0, 0, 1})
	for {
		c.SetReadDeadline(time.Now().Add(700 * time.Millisecond))
		n, err := c.Read(buf)
		if n > 0 {
			fmt.Printf("after flags +%d bytes: %x\n", n, buf[:n])
		}
		if err != nil {
			break
		}
	}
	// send option header with magic, OPT_GO with empty export name, one info req
	magic := []byte{0x49, 0x48, 0x41, 0x56, 0x45, 0x4f, 0x50, 0x54}
	opt := []byte{0, 0, 0, 7} // OPT_GO
	// payload: namelen=0, nreqs=1, req=NBD_INFO_EXPORT(1)
	payload := []byte{0, 0, 0, 0, 0, 1, 0, 1}
	l := []byte{0, 0, 0, byte(len(payload))}
	c.Write(append(append(append(magic, opt...), l...), payload...))
	for {
		c.SetReadDeadline(time.Now().Add(700 * time.Millisecond))
		n, err := c.Read(buf)
		if n > 0 {
			fmt.Printf("after optgo +%d bytes: %x\n", n, buf[:n])
		}
		if err != nil {
			break
		}
	}
}
