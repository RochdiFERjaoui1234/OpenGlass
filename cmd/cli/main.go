package main

import (
	"crypto/rand"
	"encoding/hex"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/openglass/openglass/internal/backup"
	"github.com/openglass/openglass/internal/repository/local"
	"github.com/openglass/openglass/internal/snapshot/libvirt"
)

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	switch os.Args[1] {
	case "init":
		initCmd(os.Args[2:])
	case "backup":
		backupCmd(os.Args[2:])
	case "restore":
		restoreCmd(os.Args[2:])
	case "snapshot":
		snapshotCmd(os.Args[2:])
	case "export":
		exportCmd(os.Args[2:])
	case "mount":
		mountCmd(os.Args[2:])
	case "unmount":
		unmountCmd(os.Args[2:])
	case "files":
		filesCmd(os.Args[2:])
	case "recover":
		recoverCmd(os.Args[2:])
	default:
		usage()
	}
}

func usage() {
	fmt.Println("usage: openglass <init|backup|restore|snapshot|export|mount|unmount|files|recover> [flags]")
	os.Exit(2)
}

func openStore(repoDir string) (*local.Store, error) {
	keyPath := filepath.Join(repoDir, "key")
	kb, err := os.ReadFile(keyPath)
	if err != nil {
		return nil, fmt.Errorf("missing repo key at %s (run: openglass init)", keyPath)
	}
	k, err := hex.DecodeString(string(kb))
	if err != nil {
		return nil, fmt.Errorf("bad repo key: %w", err)
	}
	return local.Open(repoDir, k)
}

func initCmd(args []string) {
	fs := flag.NewFlagSet("init", flag.ExitOnError)
	repo := fs.String("repo", "repo", "repository directory")
	fs.Parse(args)
	os.MkdirAll(*repo, 0o700)
	k := make([]byte, 32)
	rand.Read(k)
	os.WriteFile(filepath.Join(*repo, "key"), []byte(hex.EncodeToString(k)), 0o600)
	fmt.Println("initialized repository at", *repo)
}

func backupCmd(args []string) {
	fs := flag.NewFlagSet("backup", flag.ExitOnError)
	repo := fs.String("repo", "repo", "repository directory")
	vm := fs.String("vm", "", "vm name")
	src := fs.String("src", "", "disk image path (file mode; domain must be shut off)")
	id := fs.String("id", "", "restore point id")
	incremental := fs.Bool("incremental", false, "incremental via QEMU dirty bitmap (NBD block status)")
	parent := fs.String("parent", "", "parent restore point id (with --incremental)")
	bitmap := fs.String("bitmap", "", "dirty bitmap to read (live cycles print the next one as og-<id>)")
	live := fs.Bool("live", false, "running domain: suspend/snapshot/commit cycle (no --src)")
	fs.Parse(args)
	if *vm == "" || *id == "" {
		fmt.Fprintln(os.Stderr, "backup requires --vm, --id")
		os.Exit(2)
	}
	if !*live && *src == "" {
		fmt.Fprintln(os.Stderr, "backup requires --src (or --live)")
		os.Exit(2)
	}
	s, err := openStore(*repo)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if *incremental {
		if *parent == "" || *bitmap == "" {
			fmt.Fprintln(os.Stderr, "--incremental requires --parent and --bitmap")
			os.Exit(2)
		}
		if *live {
			m, err := backup.RunIncrementalLive(s, libvirt.New(), *vm, *parent, *bitmap, *id)
			if err != nil {
				fmt.Fprintln(os.Stderr, err)
				os.Exit(1)
			}
			fmt.Printf("live incremental backup of %s as %s (%d bytes, %d dirty spans, parent %s; next bitmap og-%s)\n", *vm, m.ID, m.Size, len(m.Spans), m.Parent, *id)
			return
		}
		m, err := backup.RunIncrementalNBD(s, *vm, *parent, *src, *bitmap, *id)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		fmt.Printf("incremental backup of %s as %s (%d bytes, %d dirty spans, parent %s)\n", *vm, m.ID, m.Size, len(m.Spans), m.Parent)
		return
	}
	if *live {
		m, err := backup.RunDiskLive(s, libvirt.New(), *vm, *id)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		fmt.Printf("live backup of %s as %s (%d bytes, %d chunks)\n", *vm, m.ID, m.Size, len(m.Chunks))
		return
	}
	m, err := backup.RunDisk(s, *vm, *src, *id)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Printf("backed up %s as %s (%d bytes, %d chunks)\n", *src, m.ID, m.Size, len(m.Chunks))
}

func exportCmd(args []string) {
	fs := flag.NewFlagSet("export", flag.ExitOnError)
	repo := fs.String("repo", "repo", "repository directory")
	id := fs.String("id", "", "restore point id")
	dst := fs.String("dst", "", "output image path")
	fs.Parse(args)
	if *id == "" || *dst == "" {
		fmt.Fprintln(os.Stderr, "export requires --id --dst")
		os.Exit(2)
	}
	s, err := openStore(*repo)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	f, err := os.Create(*dst)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer f.Close()
	if err := backup.ExportImage(s, *id, f); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Println("exported", *id, "to", *dst)
}

func mountCmd(args []string) {
	fs := flag.NewFlagSet("mount", flag.ExitOnError)
	repo := fs.String("repo", "repo", "repository directory")
	id := fs.String("id", "", "restore point id")
	mp := fs.String("mountpoint", "", "where to mount (read-only)")
	fstype := fs.String("fstype", "ext4", "filesystem type")
	fs.Parse(args)
	if *id == "" || *mp == "" {
		fmt.Fprintln(os.Stderr, "mount requires --id --mountpoint")
		os.Exit(2)
	}
	s, err := openStore(*repo)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	tmp := filepath.Join(os.TempDir(), "openglass-mount-"+*id+".raw")
	f, err := os.Create(tmp)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if err := backup.ExportImage(s, *id, f); err != nil {
		f.Close()
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	f.Close()
	run := func(name string, a ...string) {
		cmd := exec.Command(name, a...)
		cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
		if err := cmd.Run(); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	}
	run("sudo", "-n", "qemu-nbd", "--read-only", "-c", "/dev/nbd0", tmp)
	os.MkdirAll(*mp, 0o755)
	run("sudo", "-n", "mount", "-o", "ro", "-t", *fstype, "/dev/nbd0", *mp)
	fmt.Printf("mounted %s (read-only, %s) at %s; unmount with: openglass unmount -mountpoint %s\n", *id, *fstype, *mp, *mp)
}

func unmountCmd(args []string) {
	fs := flag.NewFlagSet("unmount", flag.ExitOnError)
	mp := fs.String("mountpoint", "", "mountpoint to unmount")
	fs.Parse(args)
	if *mp == "" {
		fmt.Fprintln(os.Stderr, "unmount requires --mountpoint")
		os.Exit(2)
	}
	run := func(name string, a ...string) {
		cmd := exec.Command(name, a...)
		cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
		_ = cmd.Run()
	}
	run("sudo", "-n", "umount", *mp)
	run("sudo", "-n", "qemu-nbd", "-d", "/dev/nbd0")
	fmt.Println("unmounted")
}

// filesCmd extracts a file from a raw ext4 restore point via debugfs
// (no loop mount needed). Image must be raw ext4; convert qcow2 first.
func filesCmd(args []string) {
	fs := flag.NewFlagSet("files", flag.ExitOnError)
	repo := fs.String("repo", "repo", "repository directory")
	id := fs.String("id", "", "restore point id")
	path := fs.String("path", "", "file path inside the image")
	dst := fs.String("dst", "", "output file path")
	fs.Parse(args)
	if *id == "" || *path == "" || *dst == "" {
		fmt.Fprintln(os.Stderr, "files requires --id --path --dst")
		os.Exit(2)
	}
	s, err := openStore(*repo)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	tmp := filepath.Join(os.TempDir(), "openglass-files-"+*id+".raw")
	f, err := os.Create(tmp)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if err := backup.ExportImage(s, *id, f); err != nil {
		f.Close()
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	f.Close()
	out, err := exec.Command("sudo", "-n", "debugfs", "-R", "cat "+*path, tmp).Output()
	if err != nil {
		fmt.Fprintln(os.Stderr, "debugfs:", err)
		os.Exit(1)
	}
	if err := os.WriteFile(*dst, out, 0o600); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	os.Remove(tmp)
	fmt.Printf("extracted %s from %s to %s (%d bytes)\n", *path, *id, *dst, len(out))
}

func snapshotCmd(args []string) {
	fs := flag.NewFlagSet("snapshot", flag.ExitOnError)
	vm := fs.String("vm", "", "vm name")
	action := fs.String("action", "", "create|delete")
	id := fs.String("id", "", "snapshot id (delete)")
	fs.Parse(args)
	p := libvirt.New()
	switch *action {
	case "create":
		s, err := p.CreateSnapshot(*vm)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		fmt.Println("created snapshot:", s)
	case "delete":
		if *id == "" {
			fmt.Fprintln(os.Stderr, "delete requires --id")
			os.Exit(2)
		}
		if err := p.DeleteSnapshot(*vm, *id); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		fmt.Println("deleted snapshot:", *id)
	default:
		fmt.Fprintln(os.Stderr, "snapshot requires --action create|delete")
		os.Exit(2)
	}
}

func restoreCmd(args []string) {
	fs := flag.NewFlagSet("restore", flag.ExitOnError)
	repo := fs.String("repo", "repo", "repository directory")
	id := fs.String("id", "", "restore point id")
	dst := fs.String("dst", "", "destination file path")
	fs.Parse(args)
	if *id == "" || *dst == "" {
		fmt.Fprintln(os.Stderr, "restore requires --id, --dst")
		os.Exit(2)
	}
	s, err := openStore(*repo)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	m, err := backup.RestoreFile(s, *id, *dst)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Printf("restored %s (%d bytes) to %s\n", m.ID, m.Size, *dst)
}

// recoverCmd boots a transient domain off a restore point (instant
// recovery): materialize the point (spans replayed for incrementals) to a
// raw file and `virsh create` a throwaway VM on it, read-only.
func recoverCmd(args []string) {
	fs := flag.NewFlagSet("recover", flag.ExitOnError)
	repo := fs.String("repo", "repo", "repository directory")
	id := fs.String("id", "", "restore point id")
	name := fs.String("name", "", "recovered domain name (default og-recover-<id>)")
	mem := fs.String("mem", "512", "memory in MiB")
	fs.Parse(args)
	if *id == "" {
		fmt.Fprintln(os.Stderr, "recover requires --id")
		os.Exit(2)
	}
	if *name == "" {
		*name = "og-recover-" + *id
	}
	s, err := openStore(*repo)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	img := filepath.Join(os.TempDir(), "og-recover-"+*id+".raw")
	f, err := os.Create(img)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if err := backup.ExportImage(s, *id, f); err != nil {
		f.Close()
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	f.Close()
	xml := fmt.Sprintf(`<domain type='kvm'>
  <name>%s</name>
  <memory unit='MiB'>%s</memory>
  <vcpu>1</vcpu>
  <os><type arch='x86_64'>hvm</type><boot dev='hd'/></os>
  <devices>
    <emulator>/usr/bin/qemu-system-x86_64</emulator>
    <disk type='file' device='disk'>
      <driver name='qemu' type='raw'/>
      <source file='%s'/>
      <target dev='vda' bus='virtio'/>
      <readonly/>
    </disk>
  </devices>
</domain>`, *name, *mem, img)
	xmlPath := img + ".xml"
	if err := os.WriteFile(xmlPath, []byte(xml), 0o600); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	out, err := exec.Command("sudo", "-n", "virsh", "create", xmlPath).CombinedOutput()
	if err != nil {
		fmt.Fprintf(os.Stderr, "virsh create: %s\n", out)
		os.Exit(1)
	}
	fmt.Printf("recovered %s as transient domain %s from %s\nstop with: sudo virsh destroy %s; clean up with: rm %s %s\n",
		*id, *name, img, *name, img, xmlPath)
}
