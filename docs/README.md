# OpenGlass walkthroughs

Step-by-step guides for real tasks. Each file is self-contained: start at
the top, copy-paste downwards. Main reference stays in `../README.md`.

| File | You will… |
|---|---|
| `00-architecture.md` | Understand processes, data flow, formats, and load-bearing decisions |
| `01-full-backup-restore.md` | Back up a shut-off disk image and restore it byte-identical |
| `02-incremental-backup.md` | Back up only changed blocks via a QEMU dirty bitmap |
| `03-live-backup.md` | Back up a RUNNING domain (full + incremental) with ~seconds of pause |
| `04-instant-recovery.md` | Boot a throwaway VM straight off a restore point |
| `05-file-access.md` | Pull a single file out, or mount a point read-only |
| `06-controller-worker.md` | Run backups as NATS jobs and query the catalog + history |

Prerequisites for all of them (one time):

- Linux host (or WSL) with `qemu-img`, `qemu-nbd`, `virsh` (for live/recover)
- `sudo -n` NOPASSWD entries for `virsh qemu-img qemu-nbd mount umount
  debugfs bash` (system libvirt needs root)
- `openglass` binary built (`go build ./...`)
