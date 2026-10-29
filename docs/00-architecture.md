# 00 — Architecture

How OpenGlass fits together: processes, data flow, on-disk formats, and
the load-bearing decisions. For hands-on use see the `01`–`06`
walkthroughs; for API-shape details see `../README.md`.

## Big picture

```
                        NATS                          Postgres
              openglass.jobs │ openglass.results      ┌─────────────┐
                             ▼                        │ hosts, vms  │
┌────────────┐  publish   ┌──────────┐  subscribe  ┌──▶│ jobs,       │
│ controller │ ──────────▶│   bus    │◀────────── │   │ restore_    │
│ (one-shot) │            └──────────┘   worker(s) │   │ points,     │
└────────────┘                                │     │   │ metrics_    │
                                              │     │   └─────────────┘
                                              ▼     │         ▲
                                    ┌──────────────┐│         │ record()
                                    │ backup engine│┘         │
                                    └──────────────┘    ┌─────┴──────┐
                                              │         │ controller │
                                              ▼         │ (records   │
                                    ┌──────────────────┐│  its job's │
                                    │ content-addressed││  result)    │
                                    │ repository (dir) │└────────────┘
                                    └──────────────────┘
```

Three planes, deliberately separated:

- **Control plane** — NATS subjects carry jobs one way, results back.
  Push-based throughout: workers subscribe, the controller publishes one
  job and blocks for its result. No polling, no scheduler (yet).
- **Data plane** — QEMU image → read-only `qemu-nbd` export → NBD client
  → 1 MiB read windows → content-defined chunks → zstd + AES-256-GCM →
  files named by SHA-256. Same pipeline for full, incremental, and live.
- **State plane** — Postgres catalog for relational state (`hosts`,
  `vms`, `jobs`, `restore_points`) plus the append-only
  `metrics_history(ts, source, key, value)` event stream.

## Processes

| Binary | Lifetime | Role |
|---|---|---|
| `cli` | one-shot | All user commands; embeds the backup engine directly |
| `controller` | one-shot **per job** | Publishes one `Job`, waits for its `Result`, writes catalog rows. Not a daemon — there is no long-lived convergence process yet |
| `worker` | daemon | Subscribes to jobs, executes via `internal/worker`, publishes results with worker-measured start/finish times |
| `nbdtest` / `nbdprobe` | dev tools | Manual NBD export interrogation (contexts, extents, reads, writes). Not part of the product path |

`internal/worker.Execute` is transport-agnostic: it takes a `queue.Job`
and returns a `queue.Result`, so the CLI and the NATS worker share the
exact same backup code paths (`RunDisk` vs `RunDiskLive`, etc.).

## Packages (`internal/`)

- `backup` — the engine. `disk.go`: `backupFull` / `backupSpans` over an
  NBD export (`RunDisk`, `RunIncrementalNBD` are shutoff-mode wrappers).
  `live.go`: `withFrozenParent` (suspend → snapshot → read → commit →
  resume) + `RunDiskLive` / `RunIncrementalLive`. `backup.go`:
  `materialize` (base + chronological span overlay), `ExportImage`.
- `transport/nbd` — fixed-newstyle NBD client (`client.go`: handshake,
  structured replies, `SetMetaContext`, `DirtyExtents`, `ReadAt`,
  `WriteAt`) and a thin `qemu-nbd` serve helper (`nbd.go`).
- `snapshot` — `Provider` interface (`CreateSnapshot`, `ChangedBlocks`,
  `Read`, `DeleteSnapshot`); `snapshot/libvirt` implements it by shelling
  out to `virsh` (no cgo) and adds the live-cycle verbs (suspend/resume,
  disk-only snapshot, blockcommit, QMP bitmap ops, node parsing).
- `repository/local` — `Store`: `Put`/`Get` (compress+encrypt,
  dedupe-by-hash), manifests, `Prune` (two-phase GC that pins span chunks).
- `chunker` — content-defined chunking over each read window.
- `queue` — `Job` / `Result` JSON over NATS.
- `catalog` — Postgres access: ensure-host/VM, record job/point/metric.

## Flows

**Full (shutoff).** `dialExport` starts `qemu-nbd -r` (sudo, world-readable
socket) → `EnterTransmission` → read virtual size in 1 MiB windows →
chunk → store → manifest with ordered `Chunks`.

**Incremental (shutoff).** Same, plus `--bitmap=<name>` on the export and
`SetMetaContext("qemu:dirty-bitmap:<name>")` → `DirtyExtents` → read only
dirty ranges → manifest with `Parent` + `Spans`.

**Live (running).** `DiskNode` + ensure bitmap → `withFrozenParent`:
suspend (skipped if already paused — and then no resume either) →
snapshot → read frozen parent (full or spans) → best-effort remove of the
consumed bitmap → blockcommit → snapshot-metadata delete → overlay file
remove → mint fresh `og-<id>` bitmap (still suspended: no write window) →
resume (deferred, always runs after a suspend we did; failures unwind the
overlay first).

**Restore / export / recover / mount / files.** All funnel through
`materialize` (full base, then each ancestor's spans in order, truncate to
logical size) or `ExportImage` (streaming for full points, file-backed for
incrementals since spans need seeks).

## On-disk formats

Repository layout:

```
repo/
  key                        # hex-encoded AES-256 key (0600)
  chunks/<aa>/<sha256>       # zstd + AES-GCM blobs (0600)
  snapshots/<point-id>.json  # manifest
```

Manifest (simplified):

```json
{"id":"snap4","vm":"testvm","parent":"snap3","size":10485760,
 "chunks":["..."],
 "spans":[{"off":0,"chunks":["..."]}],
 "created":1729999999}
```

Full points fill `chunks`, leave `spans` empty; incrementals do the
opposite. `size` is always the guest-logical disk size.

Catalog tables: `hosts`, `vms`, `restore_points`, `jobs`
(`started_at`/`finished_at` are worker-measured), `metrics_history`.

## Load-bearing decisions

1. **Guest-logical address space.** Fulls read virtual disk bytes, not
   qcow2 file bytes — the only way dirty-bitmap offsets (guest space) can
   overlay a base. Consequence: backups of qcow2 files restore as raw
   guest disks.
2. **Spans are data, not diffs.** Overlapping dirt across cycles replays
   correctly by construction; a repeated range only costs dedupe, never
   correctness. This is what makes inc-over-inc and best-effort bitmap GC
   safe.
3. **Monotonic bitmaps (`og-<id>`).** QEMU 6.2 freezes consumed persistent
   bitmaps readonly/inconsistent, so bitmaps are minted per cycle, never
   reused. Stale ones (~100 B) linger harmlessly when unremovable.
4. **Suspended, not stopped.** The live window pauses the guest instead of
   shutting it down; the bitmap minted post-commit closes the write gap.
5. **Clean qemu-nbd shutdown.** The export process is `Wait`ed on after
   client disconnect (it self-exits and flushes); SIGKILL is only the
   fallback — kills leave persistent bitmaps inconsistent/readonly.
6. **Push, not pull.** Workers report over NATS on their own schedule;
   history and liveness derive from that stream. No scraping layer.
7. **sudo for hypervisor access.** All `virsh`/`qemu-nbd` calls run under
   `sudo -n` (system libvirt needs root; user-session libvirt can't see
   system domains — a real bug class here, now unified).

## Constraints (ceilings)

- Single-disk (`vda`) VMs; QEMU 6.2/libvirt 8.0 API surface
  (`block-export-add` and pull `backup-begin` don't work there).
- Controller is one-shot per job; no scheduler, no worker heartbeats yet
  (liveness = job results + history only).
- `metrics_history` needs scheduled retention (`90 days` in README).
- NATS without auth, repo key as a local file — single-host trust model.
