# 05 — Getting files out (extract / export / mount)

**Case:** you don't need the whole disk — one config file, a full raw copy,
or a browsable filesystem.

```bash
# single file, no mount (raw ext4 points; uses debugfs under the hood)
openglass files -repo ~/repo -id snap1 -path /etc/hostname -dst ./hostname

# whole point as a raw image (byte-identical, incrementals replayed)
openglass export -repo ~/repo -id snap2 -dst ./disk.img

# read-only mount (needs the nbd module once: sudo modprobe nbd)
openglass mount -repo ~/repo -id snap1 -mountpoint /mnt/og -fstype ext4
ls /mnt/og
openglass unmount -mountpoint /mnt/og
```

**What happened:** `files` and `mount` both materialize through
`ExportImage` (incremental-aware) into a temp raw file — `files` runs
`debugfs cat` on it, `mount` attaches it to `/dev/nbd0` read-only and
mounts that. `export` is the same materialization written to your path.

**Rules that bite:**
- `files`/`mount` expect a filesystem image (ext4); a point holding
  partition tables or other OSes needs manual `qemu-nbd`/`kpartx` work (v2).
- Only one `/dev/nbd0` mount at a time; `unmount` detaches it.
- `files` cleans its temp file; `mount` leaves
  `/tmp/openglass-mount-<id>.raw` behind — remove it yourself after unmount.
- `export -dst` is yours from the start.
