-- OpenGlass catalog schema (Postgres)
CREATE TABLE hosts (
  id          BIGSERIAL PRIMARY KEY,
  name        TEXT NOT NULL UNIQUE,
  endpoint    TEXT NOT NULL,
  created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE vms (
  id          BIGSERIAL PRIMARY KEY,
  host_id     BIGINT NOT NULL REFERENCES hosts(id) ON DELETE CASCADE,
  name        TEXT NOT NULL,
  UNIQUE (host_id, name)
);

CREATE TABLE restore_points (
  id          TEXT PRIMARY KEY,
  vm_id       BIGINT NOT NULL REFERENCES vms(id) ON DELETE CASCADE,
  size        BIGINT NOT NULL,
  parent_id   TEXT REFERENCES restore_points(id),
  created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE jobs (
  id            BIGSERIAL PRIMARY KEY,
  vm_id         BIGINT NOT NULL REFERENCES vms(id) ON DELETE CASCADE,
  kind          TEXT NOT NULL CHECK (kind IN ('backup','restore','prune')),
  status        TEXT NOT NULL DEFAULT 'queued',
  started_at    TIMESTAMPTZ,
  finished_at   TIMESTAMPTZ,
  error         TEXT
);

-- Append-only event stream for everything the relational tables don't
-- keep: status transitions, per-source samples. Timestamped on write
-- (receive time — no worker clock-skew). Consumers: ad-hoc history
-- queries now, an optional /metrics exposition later. RETENTION: rows are
-- never updated; prune on a schedule or this table grows forever, e.g.:
--   DELETE FROM metrics_history WHERE ts < now() - interval '90 days';
CREATE TABLE metrics_history (
  ts            TIMESTAMPTZ NOT NULL DEFAULT now(),
  source        TEXT NOT NULL,   -- e.g. 'job:<id>', 'vm:<name>'
  key           TEXT NOT NULL,   -- e.g. 'status', 'last_point'
  value         TEXT NOT NULL
);
CREATE INDEX ON metrics_history (source, ts);
