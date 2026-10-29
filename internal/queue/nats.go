// Package queue carries backup/restore jobs between controller and workers
// over NATS (spec §3.8, pull-based consumers).
package queue

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/nats-io/nats.go"
)

const (
	JobsSubject    = "openglass.jobs"
	ResultsSubject = "openglass.results"
)

type Job struct {
	ID      string `json:"id"`
	VM      string `json:"vm"`
	Mode    string `json:"mode"` // "full" | "incremental"
	Src     string `json:"src"`  // disk image path (file mode; unused when Live)
	Parent  string `json:"parent,omitempty"`
	Bitmap  string `json:"bitmap,omitempty"` // dirty bitmap (incremental)
	Live    bool   `json:"live,omitempty"`   // running domain via snapshot cycle
	RepoDir string `json:"repo_dir"`
}

type Result struct {
	JobID    string    `json:"job_id"`
	Status   string    `json:"status"` // "ok" | "error"
	Message  string    `json:"message,omitempty"`
	PointID  string    `json:"point_id,omitempty"`
	Size     int64     `json:"size,omitempty"`
	Started  time.Time `json:"started,omitempty"`  // worker-measured start
	Finished time.Time `json:"finished,omitempty"` // worker-measured finish
}

type Bus struct{ nc *nats.Conn }

func Connect(url string) (*Bus, error) {
	if url == "" {
		url = nats.DefaultURL
	}
	nc, err := nats.Connect(url)
	if err != nil {
		return nil, fmt.Errorf("nats connect: %w", err)
	}
	return &Bus{nc: nc}, nil
}

func (b *Bus) Close() { b.nc.Close() }

func (b *Bus) PublishJob(j Job) error {
	raw, err := json.Marshal(j)
	if err != nil {
		return err
	}
	return b.nc.Publish(JobsSubject, raw)
}

func (b *Bus) PublishResult(r Result) error {
	raw, err := json.Marshal(r)
	if err != nil {
		return err
	}
	return b.nc.Publish(ResultsSubject, raw)
}

// OnJob delivers jobs to fn; blocks until ctx-cancel/quit by caller closing.
func (b *Bus) OnJob(fn func(Job) Result) error {
	_, err := b.nc.Subscribe(JobsSubject, func(m *nats.Msg) {
		var j Job
		if json.Unmarshal(m.Data, &j) != nil {
			return
		}
		b.PublishResult(fn(j))
	})
	return err
}

// OnResult delivers results to fn.
func (b *Bus) OnResult(fn func(Result)) error {
	_, err := b.nc.Subscribe(ResultsSubject, func(m *nats.Msg) {
		var r Result
		if json.Unmarshal(m.Data, &r) == nil {
			fn(r)
		}
	})
	return err
}
