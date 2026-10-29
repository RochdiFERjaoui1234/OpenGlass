# 06 — Distributed backups (controller + worker over NATS)

**Case:** run backups as jobs from anywhere: the controller publishes, a
worker executes, results and history land in Postgres.

```bash
# 1. bus and storage
nats-server -p 4222 &
# (Postgres with the catalog schema; DSN via CATALOG_DSN or the default
#  postgres://openglass:openglass@localhost:5432/openglass?sslmode=disable)

# 2. worker (does the work; needs the repo key + hypervisor access)
NATS_URL=nats://localhost:4222 ./worker &

# 3. one job = one controller run; it blocks for the result, then records it
./controller -vm testvm -mode full -src /tmp/vmdisk.qcow2 \
  -repo ~/repo -id job1 -nats nats://localhost:4222
# job job1: ok

# live incremental through the same path (no --src; add --live)
./controller -vm testvm -mode incremental -parent job1 -bitmap og-job1 \
  -live -repo ~/repo -id job2 -nats nats://localhost:4222
```

**What happened:** the job went out on `openglass.jobs`, the worker ran it
(same engine as the CLI, plus worker-measured start/finish times in the
result), the result came back on `openglass.results`, and the controller
wrote the `jobs` + `restore_points` rows plus `metrics_history` samples
(`job:<id>/status`, `vm:<vm>/last_point`).

**Reading it back:**

```sql
-- latest state per VM + real durations
SELECT v.name, j.status, j.finished_at - j.started_at AS duration
FROM jobs j JOIN vms v ON v.id = j.vm_id ORDER BY j.finished_at DESC LIMIT 20;
-- event history (trends, flaps, audits)
SELECT ts, key, value FROM metrics_history
WHERE source = 'vm:testvm' ORDER BY ts;
-- retention: schedule this, the table only grows
DELETE FROM metrics_history WHERE ts < now() - interval '90 days';
```

**Rules that bite:**
- The controller is one-shot per job (publish → wait → record → exit),
  not a daemon. One invocation per backup.
- The worker needs the same `sudo -n` NOPASSWD setup as the CLI — it
  shells out to `virsh`/`qemu-nbd` identically.
- Job ids double as restore-point ids; keep them unique per backup.
