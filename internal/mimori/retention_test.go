package mimori

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

func TestEndedRetentionBoundaryAndValidator(t *testing.T) {
	e := event("end", "r", "ended", 2)
	e.Relation = "root"
	end, _ := time.Parse(time.RFC3339Nano, e.ObservedAt)
	s := Snapshot{"epoch:1", Reduce([]Event{e})}
	q := Query{Version: 1}
	before := s.queryAt(q, end.Add(EndedRetention-time.Nanosecond))
	if len(before.Roots) != 1 {
		t.Fatal(before)
	}
	q.Revision = before.Revision
	if !s.queryAt(q, end.Add(time.Minute)).Unchanged {
		t.Fatal("stable view invalidated")
	}
	after := s.queryAt(q, end.Add(EndedRetention))
	if after.Unchanged || len(after.Roots) != 0 || !after.Complete || after.Revision == before.Revision {
		t.Fatal(after)
	}
	q.Revision = after.Revision
	if !s.queryAt(q, end.Add(time.Hour)).Unchanged {
		t.Fatal("expired view unstable")
	}
	detail := s.queryAt(Query{Version: 1, Provider: "test", Session: "r", Revision: before.Revision}, end.Add(EndedRetention))
	if detail.ErrorCode != "not_found" {
		t.Fatal(detail)
	}
	if len(s.Sessions) != 1 {
		t.Fatal("snapshot mutated")
	}
}

func TestRetentionBeforeResponseLimit(t *testing.T) {
	now := time.Date(2026, 10, 3, 17, 0, 0, 0, time.UTC)
	var es []Event
	for i := 0; i < 1200; i++ {
		e := event(fmt.Sprint(i), fmt.Sprint(i), "ended", 1)
		e.Relation = "root"
		e.Name = strings.Repeat("n", 4096)
		es = append(es, e)
	}
	for _, state := range []string{"idle", "running", "identity"} {
		e := event(state, state, state, 1)
		e.Relation = "root"
		es = append(es, e)
	}
	r := (Snapshot{"epoch", Reduce(es)}).queryAt(Query{Version: 1}, now)
	if len(r.Roots) != 3 {
		t.Fatalf("history occupies query: %d", len(r.Roots))
	}
	var wire bytes.Buffer
	if err := writeResponse(&wire, r); err != nil || bytes.Contains(wire.Bytes(), []byte("response_too_large")) {
		t.Fatalf("expired history consumed frame limit: %v %s", err, wire.String())
	}
}

func TestRetentionDelayedEventsAndResume(t *testing.T) {
	e := event("end", "r", "ended", 2)
	e.Relation = "root"
	later := event("late", "r", "identity", 3)
	later.ObservedAt = "2026-10-03T17:00:00Z"
	duplicate := e
	duplicate.ID = "duplicate"
	duplicate.ObservedAt = later.ObservedAt
	now, _ := time.Parse(time.RFC3339Nano, later.ObservedAt)
	for _, es := range [][]Event{{e, later, duplicate}, {duplicate, later, e}} {
		ss := Reduce(es)
		if ss[0].EndedAt != e.ObservedAt {
			t.Fatal(ss)
		}
		if len((Snapshot{"epoch", ss}).queryAt(Query{Version: 1}, now).Roots) != 0 {
			t.Fatal("late event extends retention")
		}
	}
	resume := event("resume", "r", "running", 1)
	resume.Generation = 2
	resume.Relation = "root"
	ss := Reduce([]Event{e, resume, duplicate})
	r := (Snapshot{"epoch", ss}).queryAt(Query{Version: 1}, now)
	if len(r.Roots) != 1 || r.Roots[0].State != "running" || r.Roots[0].EndedAt != "" {
		t.Fatal(r)
	}
}

func TestRetentionPreservesAncestorsAndGeneration(t *testing.T) {
	now := time.Date(2026, 10, 3, 17, 0, 0, 0, time.UTC)
	for _, kind := range []string{"running", "idle", "identity", "request_open", "attention_unknown", "ended"} {
		t.Run(kind, func(t *testing.T) {
			root := event("root", "r", "ended", 1)
			root.Relation = "root"
			mid := event("mid", "m", "ended", 1)
			mid.Relation = "child"
			mid.Parent = "r"
			mid.ParentGeneration = 1
			child := event("child", "c", kind, 1)
			child.Relation = "child"
			child.Parent = "m"
			child.ParentGeneration = 1
			child.Request = "req"
			if kind == "ended" {
				child.ObservedAt = now.Format(time.RFC3339Nano)
			}
			s := Snapshot{"epoch", Reduce([]Event{root, mid, child})}
			r := s.queryAt(Query{Version: 1}, now)
			if len(r.Roots) != 1 || len(s.visibleAt(now).Sessions) != 3 {
				t.Fatal("lost ancestor", r)
			}
			if kind == "ended" && len(s.queryAt(Query{Version: 1}, now.Add(EndedRetention)).Roots) != 0 {
				t.Fatal("fully ended tree retained")
			}
			child.ParentGeneration = 2
			s.Sessions = Reduce([]Event{root, mid, child})
			r = s.queryAt(Query{Version: 1}, now)
			if len(r.Roots) != 0 || len(r.Unclassified) != 1 {
				t.Fatal("crossed parent generation", r)
			}
		})
	}
}

// Exercise the real socket with no ingest after startup, then reopen storage to
// prove view expiry did not purge the durable event.
func TestSocketRetentionWithoutNewEvents(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "mm-retention-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	e := event("end", "r", "ended", 1)
	e.Relation = "root"
	e.ObservedAt = time.Now().Add(-EndedRetention + 2*time.Second).UTC().Format(time.RFC3339Nano)
	if err := Enqueue(dir, e); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	ready := make(chan struct{})
	done := make(chan error, 1)
	go func() { done <- ServeReady(ctx, dir, func(s string) { t.Log(s) }, func() { close(ready) }) }()
	stopped := false
	defer func() {
		if !stopped {
			cancel()
			<-done
		}
	}()
	select {
	case <-ready:
	case err := <-done:
		stopped = true
		cancel()
		t.Fatal(err)
	case <-time.After(5 * time.Second):
		t.Fatal("startup timeout")
	}
	before, err := Fetch(dir, Query{Version: 1})
	if err != nil || len(before.Roots) != 1 {
		t.Fatalf("before: %+v %v", before, err)
	}
	q := Query{Version: 1, Revision: before.Revision}
	same, err := Fetch(dir, q)
	if err != nil || !same.Unchanged {
		t.Fatalf("cache: %+v %v", same, err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		got, err := Fetch(dir, q)
		if err != nil {
			t.Fatal(err)
		}
		if !got.Unchanged {
			if len(got.Roots) != 0 || !got.Complete || got.Revision == before.Revision {
				t.Fatal(got)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("time alone did not expire view")
		}
		time.Sleep(20 * time.Millisecond)
	}
	cancel()
	err = <-done
	stopped = true
	if err != nil {
		t.Fatal(err)
	}
	store, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if len(store.events) != 1 || store.events[0].ID != e.ID {
		t.Fatal("durable history removed")
	}
	if len((Snapshot{"restart", Reduce(store.events)}).Query(Query{Version: 1}).Roots) != 0 {
		t.Fatal("replay resurrected history")
	}
}
