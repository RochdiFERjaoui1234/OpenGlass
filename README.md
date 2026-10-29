# OpenGlass

Open-source image-level backup/replication platform in the spirit of Veeam
Backup & Replication, built on libvirt/QEMU, NBD, and a restic-style
content-addressable repository.

Step-by-step user walkthroughs live in [`docs/`](docs/) (full backup,
incrementals, live backups, instant recovery, file access, NATS jobs).

## Architecture

| Component | Path | Role |
|---|---|---|
| CLI | `cmd/cli` | `init` / `backup` / `restore` / `export` / `mount` / `unmount` / `files` / `snapshot` / `recover` — runs anywhere |
| Controller | `cmd/controller` | Publishes one job to NATS, waits for its result, records it in Postgres |
| Worker | `cmd/worker` + `internal/worker` | Pulls jobs from NATS, runs full/incremental/live backups, stamps result times |
| Queue | `internal/queue` | NATS bus: `openglass.jobs` / `openglass.results` (push-based; no polling) |
| Catalog | `internal/catalog` | Postgres: hosts, VMs, jobs, restore points, `metrics_history` event stream |
| Snapshot provider | `internal/snapshot/libvirt` | `virsh`/QMP/HMP wrapper: snapshots, blockcommit, bitmaps, suspend/resume |
| NBD transport | `internal/transport/nbd` | Fixed-newstyle client: structured replies, dirty-bitmap block status |
| Chunker | `internal/chunker` | Content-defined chunking (gear/FastCDC-style) |
| Repository | `internal/repository/local` | SHA-256 dedupe, zstd, AES-256-GCM, manifests, GC |
| Backup engine | `internal/backup` | NBD full (`RunDisk`) + span incrementals (`RunIncrementalNBD`) + live cycle (`live.go`) + chain replay (`materialize`) |

## Restore points

A full point is an ordered chunk list (`Manifest.Chunks`) over the
guest-logical disk. An incremental point stores only what changed since its
parent as **spans** (`Manifest.Spans = {offset, chunk hashes}`) with
`Parent` set — chains of any depth replay chronologically at restore time
(newer spans overwrite older ones, so overlapping dirt always converges).
Full and incremental share one address space, so any incremental restores
onto any ancestor full.

## CLI usage

```bash
# initialize a repository (generates repo key)
openglass init -repo /home/rochdy/repo

# full backup of a disk image
openglass backup -repo /home/rochdy/repo -vm testvm -src /tmp/vmdisk.img -id snap1

# full restore of a restore point
openglass restore -repo /home/rochdy/repo -id snap1 -dst /tmp/restored.img

# libvirt snapshot operations
openglass snapshot --vm testvm --action create
openglass snapshot --vm testvm --action delete --id testvm-openglass

# incremental: only blocks dirty in a QEMU dirty bitmap since the parent
# point (domain must be shut off; bitmap must exist in the image, e.g.
# created via qemu-storage-daemon + QMP block-dirty-bitmap-add persistent)
openglass backup -repo /home/rochdy/repo -vm testvm -src /tmp/vmdisk.qcow2 \
  -id snap2 --incremental --parent snap1 --bitmap cp1

# live full + incremental of a RUNNING domain (suspend/snapshot/commit
# cycle; guest paused only across the freeze window). Each cycle leaves a
# fresh bitmap og-<id>; pass the previous cycle's as --bitmap.
openglass backup -repo /home/rochdy/repo -vm testvm -id snap3 --live
openglass backup -repo /home/rochdy/repo -vm testvm -id snap4 --live \
  --incremental --parent snap3 --bitmap og-snap3
# file-level extraction from a raw ext4 restore point (debugfs, no mount)
openglass files -repo R -id P -path /etc/hostname -dst ./hostname

# materialize a restore point to a raw image (byte-identical)
openglass export -repo R -id P -dst ./disk.img

# instant recovery: boot a transient domain off a restore point
# (incrementals replay their spans automatically)
openglass recover -repo R -id P -name ogrectest

# read-only NBD mount of a restore point (Linux, needs unloaded nbd module:
# `sudo modprobe nbd` once); then unmount with: openglass unmount -mountpoint M
openglass mount -repo R -id P -mountpoint /mnt/og -fstype ext4
```

## Controller / worker usage (WSL)

```bash
# start NATS
nats-server -p 4222 &

# start a worker
VIRSH_DEFAULT_CONNECT_URI=qemu:///system NATS_URL=nats://localhost:4222 ./worker &

# publish a job; controller blocks until the worker reports the result
./controller -vm testvm -mode full -src /tmp/vmdisk.img -repo /home/rochdy/repo -id job1 -nats nats://localhost:4222

# live incremental job (no --src; worker freezes the running domain).
# The read bitmap follows the og-<parent-id> convention: a live full mints
# it automatically; after a shutoff full, pre-create it once
# (qemu-storage-daemon + QMP block-dirty-bitmap-add <name> persistent:true).
./controller -vm testvm -mode incremental -parent job1 -bitmap og-job1 -live -repo /home/rochdy/repo -id job2 -nats nats://localhost:4222

# catalog rows land in Postgres:
psql "postgres://openglass:openglass@localhost:5432/openglass?sslmode=disable" \
  -c "SELECT id, vm_id, kind, status, finished_at - started_at AS duration FROM jobs;" \
  -c "SELECT id, vm_id, size FROM restore_points;" \
  -c "SELECT source, key, value FROM metrics_history ORDER BY ts DESC LIMIT 10;"
```

Environment:
- `sudo -n` NOPASSWD entries for `virsh`, `qemu-img`, `qemu-nbd`, `mount`,
  `umount`, `debugfs`, `bash` — all hypervisor/storage calls run through
  sudo (system libvirt needs root)
- `VIRSH_DEFAULT_CONNECT_URI` — libvirt connection (default `qemu:///system`)
- `NATS_URL` — NATS address
- `CATALOG_DSN` — Postgres DSN for the controller

## Incremental backups (implemented via NBD block status)

QEMU 6.2 (Ubuntu 22.04/WSL) lacks `qemu-img backup --incremental`, and the
`virsh backup-begin` pull path is empirically broken on this host
(`argument key 'filename' must not have null value`; QMP `block-export-add`
is also rejected on 6.2). OpenGlass therefore does incrementals itself:

1. Full backup = read-only `qemu-nbd` export, read in guest-logical space
   (`RunDisk`). Full and incremental share one address space.
2. Incremental = `qemu-nbd --bitmap=<name>` export + NBD structured-reply
   block status (`internal/transport/nbd`): only extents with
   `NBD_STATE_DIRTY` are read, chunked and stored as **spans**
   (`Manifest.Spans = {offset, chunk hashes}`) with `parent` set.
3. Restore/export of an incremental walks the parent chain, writes the
   full base, then seeks and overlays each span (`materialize`).

Verified end to end on WSL/QEMU 6.2 (`/home/rochdy/e2e-inc.sh`): full →
dirty write → incremental → `cmp restore(inc1) export(full2)` byte-equal.

## Live backups of running domains (`--live`)

File mode needs the domain shut off (qemu-nbd takes the image lock).
Live mode instead freezes the disk without stopping the guest:

1. `suspend` → disk-only snapshot (pre-snapshot image is frozen; the
   snapshot flushes persistent bitmaps into it) → read the frozen
   parent via `qemu-nbd --bitmap` → `blockcommit --active --pivot` back →
   delete snapshot metadata + overlay file → `resume`. Resume is deferred:
   it runs even when the backup fails, and a failed cycle unwinds the
   overlay via blockcommit.
2. The guest is paused across the whole freeze/commit window, so no write
   can slip between the bitmap and the export. Downtime is bounded by the
   dirty-data read plus commit (seconds for small deltas).

Bitmap lifecycle (monotonic names): QEMU 6.2 snapshot freezes leave
consumed persistent bitmaps readonly/inconsistent, so in-place rotation is
unreliable. Each live cycle therefore mints a fresh bitmap `og-<point-id>`
*after* the commit (still suspended — no write window) and reads the
previous cycle's (`--bitmap og-<parent-id>`, printed by the previous run).
The consumed bitmap is removed on a best-effort basis; stale ones cost
~100 bytes each and never affect correctness (spans replay chronologically).
Bootstrap after a shutoff full: pre-create `og-<full-id>` once, e.g. via
`qemu-storage-daemon` + QMP `block-dirty-bitmap-add ... persistent:true`.

Verified end to end (`/home/rochdy/live-goe2e.sh`): file full → shutoff
dirt → boot → live full → live incremental (1 span, rotation) →
`cmp restore(inc-live) export(full2)` byte-equal, chain collapsed, VM
running. Instant recovery (`/home/rochdy/recover-e2e.sh`) boots a
transient domain off the incremental point.

Current constraints (ceilings, not silent failures):
- Shutoff (file) incrementals need a pre-existing bitmap in the image
  (storage-daemon/QMP one-liner above).
- QMP `nbd-server-start`/`nbd-server-add` exports against a live guest are
  rejected on 6.2 (write-permission conflict / readonly+bitmap restriction);
  the snapshot cycle above is the supported live path.
- Single-disk (`vda`) VMs; multi-disk needs per-disk specs.

## Feature #1 — file-level restore (implemented via debugfs)

`openglass files -repo R -id P -path /etc/hostname -dst out` extracts a file
from a raw ext4 restore point without mounting. Implemented:

1. `backup.ExportImage(store, pointID, w)` streams the manifest's chunks out
   (byte-identical to the original image — SHA-256 verified on WSL).
2. CLI writes it to a temp raw file and runs `debugfs -R "cat <path>"` on it
   (`sudo` allowed via `NOPASSWD` sudoers entry). No loop mount needed.
3. Limitation: image must contain a raw ext4 filesystem — disk backups are
   guest-logical raw reads over NBD, so that is the normal case; legacy
   qcow2-container points need `qemu-img convert -O raw` first.
   Windows/xattr/journal-fail modes are v2.

## Feature #5 — NBD instant recovery + mount (implemented)

`openglass mount -repo R -id P -mountpoint M -fstype ext4` exports the
restore point's bytes through `ExportImage`, attaches them to `/dev/nbd0`
read-only via `qemu-nbd`, and mounts them. `openglass unmount -mountpoint M`
reverses it. Verified on WSL: mounted ext4, read known files, clean detach.

Later candidates: boot directly against an NBD export with
`qemu -drive file=nbd:unix:<sock>,snapshot=on` (no materialized file) and
FUSE browsing without root. Incremental points already replay over their
base at restore/export time. Sequence note: `ExportImage` is shared by #1,
#5 and #6, so all three CLIs were built on it.

## Feature #6 — instant recovery (implemented)

`openglass recover -repo R -id P [-name N]` materializes the point
(incremental spans replayed) to a raw file and `virsh create`s a transient
domain on it (read-only virtio disk). Stop with `virsh destroy N`, clean up
the raw file + XML afterwards. Verified on WSL: transient domain booted off
an incremental point, then destroyed.

## Metrics history (implemented, v1)

Each controller invocation records its job's result in Postgres, but
relational rows only hold latest state. Alongside them it now appends to an
event stream — same data, persisted as samples instead of overwritten:

- `metrics_history(ts, source, key, value)` — `ts` defaults to receive time
  (no worker clock-skew); index on `(source, ts)`.
- Each recorded job writes `job:<id>/status` and, on success,
  `vm:<vm>/last_point`. Job durations come from worker-measured
  `started/finished` in the result and live in `jobs` (no text casts needed):

```sql
-- duration trend per VM
SELECT date_trunc('day', finished_at) AS day, avg(finished_at - started_at)
FROM jobs WHERE status = 'ok' GROUP BY 1 ORDER BY 1;
-- history of one job / VM
SELECT ts, key, value FROM metrics_history
WHERE source = 'vm:testvm' ORDER BY ts;
```

Retention (mandatory — this table only grows):
`DELETE FROM metrics_history WHERE ts < now() - interval '90 days';`
on a schedule. Deliberately push-based (workers already report over NATS;
no pull/scraping layer), with the same timestamped-sample shape a future
`/metrics` endpoint could expose. Worker heartbeats + flapping detection
are the next increment, not this one.

## Test coverage

- `internal/chunker` — reconstruction, boundaries, shift-resistance
- `internal/repository/local` — round-trip, dedupe, manifest prune/GC,
  span-chunk pinning for incremental points
- `internal/backup` — backup↔restore byte-equality, incremental span
  replay incl. 3-level chains (RestoreFile + ExportImage), validation
  errors, live freeze/thaw choreography (fake virsh: order, resume-on-error,
  unwind, post-before-resume)
- `internal/snapshot/libvirt` — faked virsh HMP bitmap flow, QMP response
  parsing, bitmap add/remove idempotency, node parsing
- NBD engine (`RunDisk` / `RunIncrementalNBD` / live) — full end-to-end on
  WSL: `bash /home/rochdy/e2e-inc.sh` (file path) and
  `bash /home/rochdy/live-goe2e.sh` (live full + incremental + rotation,
  byte-compare gate); `bash /home/rochdy/recover-e2e.sh` boots a transient
  domain off the incremental point; needs qemu-nbd/sudo, so not part of
  `go test`
- Metrics (`prove-metrics.sh`) — worker → NATS → controller → real
  durations in `jobs` plus history rows; needs NATS/Postgres/virsh
- Catalog — (planned) integration test against local Postgres
#   O p e n G l a s s  
 