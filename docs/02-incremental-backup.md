# 02 — Incremental backup (shut-off domain)

**Case:** hourly incrementals of a shut-off disk — store only the blocks
that changed since the parent point.

**You need:** the parent full point (see `01`), and a persistent dirty
bitmap in the image that was tracking while writes happened.

```bash
# 1. create the bitmap once (storage-daemon + QMP; image must be unlocked)
setsid qemu-storage-daemon \
  --blockdev driver=file,node-name=f,filename=/tmp/vmdisk.qcow2 \
  --blockdev driver=qcow2,node-name=n,file=f \
  --chardev socket,id=m,path=/tmp/qmp.sock,server=on,wait=off \
  --monitor chardev=m,mode=control </dev/null >/tmp/qsd.log 2>&1 &
sleep 1
printf '%s\n' '{"execute":"qmp_capabilities"}' \
  '{"execute":"block-dirty-bitmap-add","arguments":{"node":"n","name":"cp1","persistent":true}}' \
  '{"execute":"quit"}' | timeout 3 nc -U /tmp/qmp.sock || true
sleep 1

# 2. let writes happen (guest runs, or write via a writable qemu-nbd export)

# 3. shut the domain off, then back up only the dirty ranges
openglass backup -repo ~/repo -vm testvm -src /tmp/vmdisk.qcow2 \
  -id snap2 --incremental --parent snap1 --bitmap cp1
# incremental backup of testvm as snap2 (10485760 bytes, 1 dirty spans, parent snap1)

# 4. restore replays the spans over the base automatically
openglass restore -repo ~/repo -id snap2 -dst /tmp/merged.img
```

**What happened:** the export was opened with `--bitmap=cp1`, block status
reported the dirty extents, and only those ranges were chunked and stored
as `Spans` on manifest `snap2`. Restore writes the base, then seeks and
overlays each span. Chains of any depth work; overlapping dirt converges
because replay is chronological.

**Rules that bite:**
- `--bitmap` must exist in the image or the export fails with "not found".
- The bitmap is *not* cleared for you — after a successful cycle, drop it
  (`block-dirty-bitmap-remove` via QMP, or start a new name) or the next
  incremental re-reads old dirt (still correct, just bigger).
- Disk size must equal the parent's, or the backup refuses.
