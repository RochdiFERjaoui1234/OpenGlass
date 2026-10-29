# 01 — Full backup and restore (shut-off domain)

**Case:** nightly full backup of a VM disk while the domain is shut off,
then prove the restore is byte-identical.

**You need:** a qcow2 (or raw) image, VM shut off (qemu-nbd takes the
image lock, so a running domain fails here — that's what
`03-live-backup.md` is for).

```bash
# 1. one-time repo (generates the encryption key)
openglass init -repo ~/repo

# 2. back up the whole guest-logical disk
openglass backup -repo ~/repo -vm testvm -src /tmp/vmdisk.qcow2 -id snap1
# backed up /tmp/vmdisk.qcow2 as snap1 (10485760 bytes, 10 chunks)

# 3. restore it elsewhere (output is the raw guest disk, not the qcow2 file)
openglass restore -repo ~/repo -id snap1 -dst /tmp/restored.img

# 4. prove determinism: export the same point again, compare
openglass export -repo ~/repo -id snap1 -dst /tmp/exported.img
cmp /tmp/restored.img /tmp/exported.img && echo IDENTICAL
```

**What happened:** the disk was read over a read-only `qemu-nbd` export,
split into content-defined chunks, deduplicated, compressed, encrypted,
and recorded as manifest `snap1`. Restore concatenates the chunks back.

**If it fails:** `dial export ... connection refused` usually means the
image is locked by a running domain (shut it off) or unreadable by root
(check ownership — the export runs under `sudo -n`).
