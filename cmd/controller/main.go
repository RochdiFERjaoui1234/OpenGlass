package main

import (
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/openglass/openglass/internal/catalog"
	"github.com/openglass/openglass/internal/queue"
)

// record persists the job outcome in the Postgres catalog (best-effort).
func record(id, mode, vm string, r queue.Result) {
	dsn := os.Getenv("CATALOG_DSN")
	if dsn == "" {
		dsn = "postgres://openglass:openglass@localhost:5432/openglass?sslmode=disable"
	}
	db, err := catalog.Open(dsn)
	if err != nil {
		fmt.Fprintln(os.Stderr, "catalog:", err)
		return
	}
	defer db.Close()
	h, err := db.EnsureHost("local", "qemu:///system")
	if err != nil {
		fmt.Fprintln(os.Stderr, "catalog:", err)
		return
	}
	v, err := db.EnsureVM(h, vm)
	if err != nil {
		fmt.Fprintln(os.Stderr, "catalog:", err)
		return
	}
	status := "ok"
	if r.Status != "ok" {
		status = "error"
	}
	// worker-measured times; fall back to receive time for old results.
	started, finished := r.Started, r.Finished
	if started.IsZero() || finished.IsZero() {
		started, finished = time.Now(), time.Now()
	}
	if _, err := db.RecordJob(h, v, "backup", status, started, finished, r.Message); err != nil {
		fmt.Fprintln(os.Stderr, "catalog:", err)
		return
	}
	if err := db.RecordMetric("job:"+id, "status", status); err != nil {
		fmt.Fprintln(os.Stderr, "catalog:", err)
	}
	if r.Status == "ok" && r.PointID != "" {
		if err := db.RecordPoint(r.PointID, v, r.Size, ""); err != nil {
			fmt.Fprintln(os.Stderr, "catalog:", err)
		}
		if err := db.RecordMetric("vm:"+vm, "last_point", r.PointID); err != nil {
			fmt.Fprintln(os.Stderr, "catalog:", err)
		}
	}
}

func main() {
	vm := flag.String("vm", "", "vm name")
	mode := flag.String("mode", "full", "full|incremental")
	src := flag.String("src", "", "disk image path (full)")
	parent := flag.String("parent", "", "parent point id (incremental)")
	bitmap := flag.String("bitmap", "", "dirty bitmap name (incremental)")
	live := flag.Bool("live", false, "running domain via snapshot cycle (no --src needed)")
	repo := flag.String("repo", "", "repository dir")
	id := flag.String("id", "", "restore point id")
	url := flag.String("nats", os.Getenv("NATS_URL"), "nats url")
	flag.Parse()
	if *vm == "" || *id == "" || *repo == "" {
		fmt.Fprintln(os.Stderr, "controller requires --vm --id --repo")
		os.Exit(2)
	}
	bus, err := queue.Connect(*url)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer bus.Close()
	done := make(chan queue.Result, 1)
	bus.OnResult(func(r queue.Result) {
		if r.JobID == *id {
			done <- r
		}
	})
	if err := bus.PublishJob(queue.Job{ID: *id, VM: *vm, Mode: *mode, Src: *src, Parent: *parent, Bitmap: *bitmap, Live: *live, RepoDir: *repo}); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	select {
	case r := <-done:
		fmt.Printf("job %s: %s %s\n", r.JobID, r.Status, r.Message)
		record(*id, *mode, *vm, r)
		if r.Status != "ok" {
			os.Exit(1)
		}
	case <-time.After(10 * time.Minute):
		fmt.Fprintln(os.Stderr, "timeout waiting for result")
		os.Exit(1)
	}
}
