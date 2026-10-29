# 03 — Live backup of a running domain

**Case:** back up a VM you cannot shut off. The guest pauses only across a
short freeze window (seconds for small deltas).

**You need:** the domain running on libvirt (`qemu:///system`), single
disk on `vda`.

```bash
# 1. live full — no bitmap needed, cycle mints og-snap3 for next time
openglass backup -repo ~/repo -vm testvm -id snap3 --live
# live backup of testvm as snap3 (10485760 bytes, 10 chunks)

# 2. live incremental — reads the previous cycle's bitmap ...
openglass backup -repo ~/repo -vm testvm -id snap4 --live \
  --incremental --parent snap3 --bitmap og-snap3
# live incremental backup of testvm as snap4 (10485760 bytes, 1 dirty spans,
#   parent snap3; next bitmap og-snap4)

# 3. ... and so on: --parent snap4 --bitmap og-snap4, etc.
```

**What happened each cycle:** `suspend` → disk-only snapshot (freezes the
parent; flushes bitmaps into it) → read the frozen parent over NBD →
`blockcommit --active --pivot` back → delete snapshot metadata + overlay →
mint fresh bitmap `og-<id>` (still suspended: no write window) → `resume`.
Resume is deferred — it runs even if the backup fails, and a failed cycle
unwinds the overlay via blockcommit.

**Rules that bite:**
- Always pass the bitmap the *previous* cycle printed (`og-<parent-id>`).
  After a shutoff full there is no bitmap yet — pre-create `og-<full-id>`
  once (see `02`, step 1, with that name).
- QEMU 6.2 freezes consumed bitmaps readonly, so cycles use a fresh name
  each time instead of reusing one. Stale bitmaps (~100 B each) are removed
  on a best-effort basis and never affect correctness.
- Paused domains are left paused; shut-off domains are refused (use file
  mode, `01`/`02`, for those).
