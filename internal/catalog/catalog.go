// Package catalog is the Postgres-backed registry for hosts, VMs, jobs,
// and restore points (spec §3.4). Chunk indexes stay in the repo.
package catalog

import (
	"database/sql"
	"fmt"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

type DB struct{ q *sql.DB }

func Open(dsn string) (*DB, error) {
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return nil, err
	}
	if err := db.Ping(); err != nil {
		return nil, fmt.Errorf("catalog ping: %w", err)
	}
	return &DB{q: db}, nil
}

func (d *DB) Close() error { return d.q.Close() }

// EnsureHost inserts the host row if new, returns its id.
func (d *DB) EnsureHost(name, endpoint string) (int64, error) {
	var id int64
	err := d.q.QueryRow(`INSERT INTO hosts(name, endpoint) VALUES($1,$2)
		ON CONFLICT (name) DO UPDATE SET endpoint=EXCLUDED.endpoint RETURNING id`, name, endpoint).Scan(&id)
	return id, err
}

// EnsureVM inserts the VM row if new, returns its id.
func (d *DB) EnsureVM(hostID int64, name string) (int64, error) {
	var id int64
	err := d.q.QueryRow(`INSERT INTO vms(host_id, name) VALUES($1,$2)
		ON CONFLICT (host_id, name) DO NOTHING RETURNING id`, hostID, name).Scan(&id)
	if err == sql.ErrNoRows {
		return d.vmID(hostID, name)
	}
	return id, err
}

func (d *DB) vmID(hostID int64, name string) (int64, error) {
	var id int64
	err := d.q.QueryRow(`SELECT id FROM vms WHERE host_id=$1 AND name=$2`, hostID, name).Scan(&id)
	return id, err
}

// RecordJob inserts a finished job row.
func (d *DB) RecordJob(hostID, vmID int64, kind, status string, started, finished time.Time, errMsg string) (int64, error) {
	var id int64
	err := d.q.QueryRow(`INSERT INTO jobs(vm_id, kind, status, started_at, finished_at, error)
		VALUES($1,$2,$3,$4,$5,NULLIF($6,'')) RETURNING id`, vmID, kind, status, started, finished, errMsg).Scan(&id)
	return id, err
}

// RecordPoint inserts a restore-point row.
func (d *DB) RecordPoint(id string, vmID int64, size int64, parent string) error {
	_, err := d.q.Exec(`INSERT INTO restore_points(id, vm_id, size, parent_id) VALUES($1,$2,$3,NULLIF($4,''))`,
		id, vmID, size, parent)
	return err
}

// RecordMetric appends one timestamped sample to the history stream.
// ts defaults to receive time (now()) — callers don't pass clocks.
func (d *DB) RecordMetric(source, key, value string) error {
	_, err := d.q.Exec(`INSERT INTO metrics_history(source, key, value) VALUES($1,$2,$3)`,
		source, key, value)
	return err
}
