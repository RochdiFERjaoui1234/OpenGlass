// Package nbd streams a QEMU disk/snapshot over NBD (Network Block Device),
// the open fallback transport for reading VM data (analogue of Veeam's NFC).
package nbd

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"time"
)

// Serve exports img read-only at unix socket sock via qemu-nbd.
func Serve(ctx context.Context, img, sock string) (*exec.Cmd, error) {
	cmd := exec.CommandContext(ctx, "qemu-nbd", "--read-only", "--socket", sock, img)
	if out, err := cmd.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("qemu-nbd: %w: %s", err, out)
	}
	return cmd, nil
}

// WaitReady polls until the NBD export accepts connections.
func WaitReady(sock string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(sock); err == nil {
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("qemu-nbd socket %s not ready after %s", sock, timeout)
}
