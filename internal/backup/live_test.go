package backup

import (
	"errors"
	"strings"
	"testing"

	"github.com/openglass/openglass/internal/snapshot/libvirt"
)

const blkBase = "Target   Source\n------------------------------------------\nvda      /img/base.qcow2\n"
const blkOver = "Target   Source\n------------------------------------------\nvda      /img/overlay\n"

// fakeProvider replays canned virsh outputs and records call order.
type fakeProvider struct {
	state  string // domstate answer
	blk    int    // domblklist call count
	calls  []string
	failOn map[string]error // op -> error ("suspend", "snapshot", ...)
}

func (f *fakeProvider) run(name string, args ...string) ([]byte, error) {
	op := args[0]
	f.calls = append(f.calls, op)
	if err, ok := f.failOn[op]; ok {
		return nil, err
	}
	switch op {
	case "domstate":
		return []byte(f.state + "\n"), nil
	case "domblklist":
		f.blk++
		if f.blk == 1 {
			return []byte(blkBase), nil
		}
		return []byte(blkOver), nil
	}
	return []byte(""), nil
}

func (f *fakeProvider) p() *libvirt.Provider { return &libvirt.Provider{Run: f.run} }

func ops(calls []string) string { return strings.Join(calls, ",") }

func TestFrozenParentHappyPath(t *testing.T) {
	f := &fakeProvider{state: "running", failOn: map[string]error{}}
	var gotParent string
	err := withFrozenParent(f.p(), "vm1", "og-x", func(parent string) error {
		gotParent = parent
		return nil
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if gotParent != "/img/base.qcow2" {
		t.Fatalf("fn got parent %q", gotParent)
	}
	want := "domstate,domblklist,suspend,snapshot-create-as,domblklist,blockcommit,snapshot-delete,resume"
	if ops(f.calls) != want {
		t.Fatalf("order:\n got %s\nwant %s", ops(f.calls), want)
	}
}

func TestFrozenParentResumesOnFnError(t *testing.T) {
	f := &fakeProvider{state: "running", failOn: map[string]error{}}
	boom := errors.New("boom")
	err := withFrozenParent(f.p(), "vm1", "og-x", func(parent string) error { return boom }, nil)
	if err != boom {
		t.Fatalf("want boom, got %v", err)
	}
	// resume must run, and the failed cycle unwinds (commit + metadata
	// delete) so no overlay is left behind.
	if ops(f.calls) != "domstate,domblklist,suspend,snapshot-create-as,blockcommit,snapshot-delete,resume" {
		t.Fatalf("resume/unwind wrong: %s", ops(f.calls))
	}
}

func TestFrozenParentSuspendFailure(t *testing.T) {
	f := &fakeProvider{state: "running", failOn: map[string]error{"suspend": errors.New("no")}}
	err := withFrozenParent(f.p(), "vm1", "og-x", func(parent string) error { return nil }, nil)
	if err == nil {
		t.Fatal("expected error")
	}
	if ops(f.calls) != "domstate,domblklist,suspend" {
		t.Fatalf("must stop after suspend failure, no resume: %s", ops(f.calls))
	}
}

func TestFrozenParentAlreadyPaused(t *testing.T) {
	f := &fakeProvider{state: "paused", failOn: map[string]error{}}
	err := withFrozenParent(f.p(), "vm1", "og-x", func(parent string) error { return nil }, nil)
	if err != nil {
		t.Fatal(err)
	}
	if ops(f.calls) != "domstate,domblklist,snapshot-create-as,domblklist,blockcommit,snapshot-delete" {
		t.Fatalf("paused VM must not be suspended/resumed: %s", ops(f.calls))
	}
}

func TestFrozenParentShutoff(t *testing.T) {
	f := &fakeProvider{state: "shut off", failOn: map[string]error{}}
	err := withFrozenParent(f.p(), "vm1", "og-x", func(parent string) error { return nil }, nil)
	if err == nil || !strings.Contains(err.Error(), "running") {
		t.Fatalf("want running-state error, got %v", err)
	}
	if ops(f.calls) != "domstate" {
		t.Fatalf("must stop after state check: %s", ops(f.calls))
	}
}

func TestFrozenParentPostRunsBeforeResume(t *testing.T) {
	f := &fakeProvider{state: "running", failOn: map[string]error{}}
	var order []string
	err := withFrozenParent(f.p(), "vm1", "og-x",
		func(parent string) error { order = append(order, "pre"); return nil },
		func() error { order = append(order, "post"); return nil })
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(order, ",") != "pre,post" {
		t.Fatalf("pre/post order: %v", order)
	}
	want := "domstate,domblklist,suspend,snapshot-create-as,domblklist,blockcommit,snapshot-delete,resume"
	if ops(f.calls) != want {
		t.Fatalf("order:\n got %s\nwant %s", ops(f.calls), want)
	}
}

func TestActiveImageParse(t *testing.T) {
	f := &fakeProvider{state: "running", failOn: map[string]error{}}
	got, err := f.p().ActiveImage("vm1", "vda")
	if err != nil || got != "/img/base.qcow2" {
		t.Fatalf("parse: %q %v", got, err)
	}
	if _, err := f.p().ActiveImage("vm1", "vdb"); err == nil {
		t.Fatal("expected missing-disk error")
	}
}


