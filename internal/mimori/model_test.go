package mimori

import (
	"fmt"
	"testing"
)

func event(id, session, kind string, seq uint64) Event {
	return Event{Version: 1, ID: id, Provider: "test", Session: session, Generation: 1, Seq: seq, Kind: kind, ObservedAt: "2026-10-03T16:00:00Z"}
}
func find(t *testing.T, ss []Session, id string) Session {
	t.Helper()
	for _, s := range ss {
		if s.ID == id {
			return s
		}
	}
	t.Fatalf("missing %s", id)
	return Session{}
}
func TestTreeAndIndependentRoots(t *testing.T) {
	r := event("r", "r", "idle", 1)
	r.Relation = "root"
	r.CWD = "/same"
	r2 := event("r2", "r2", "idle", 1)
	r2.Relation = "root"
	r2.CWD = "/same"
	es := []Event{r, r2}
	for i := 0; i < 50; i++ {
		e := event(fmt.Sprint(i), fmt.Sprintf("c%d", i), "running", 1)
		e.Relation = "child"
		e.Parent = "r"
		e.ParentGeneration = 1
		es = append(es, e)
	}
	g := event("g", "g", "running", 1)
	g.Relation = "child"
	g.Parent = "c0"
	g.ParentGeneration = 1
	es = append(es, g, event("orphan", "orphan", "running", 1))
	ss := Reduce(es)
	root := find(t, ss, "r")
	if root.RunningDescendants != 51 || root.Aggregate != "running" {
		t.Fatalf("%+v", root)
	}
	if find(t, ss, "r2").RunningDescendants != 0 {
		t.Fatal("cwd merged")
	}
	q := (Snapshot{"rev", ss}).Query(Query{Version: 1})
	if len(q.Roots) != 2 || len(q.Unclassified) != 1 {
		t.Fatalf("%+v", q)
	}
}
func TestRequestsResolveIndependentlyAndOutOfOrder(t *testing.T) {
	r := event("r", "r", "idle", 1)
	r.Relation = "root"
	es := []Event{r}
	for _, id := range []string{"a", "b"} {
		e := event(id, id, "request_open", 1)
		e.Request = id
		e.Relation = "child"
		e.Parent = "r"
		e.ParentGeneration = 1
		es = append(es, e)
	}
	resolve := event("resolved", "a", "request_resolved", 2)
	resolve.Request = "a"
	es = append(es, resolve)
	root := find(t, Reduce(es), "r")
	if root.Unresolved != 1 || root.Aggregate != "waiting" {
		t.Fatalf("%+v", root)
	}
	late := event("late", "a", "request_open", 3)
	late.Request = "a"
	es = append(es, late)
	if find(t, Reduce(es), "a").Unresolved != 0 {
		t.Fatal("resolved ID reopened")
	}
}
func TestLifecycleAndGeneration(t *testing.T) {
	start := event("s", "s", "turn_start", 1)
	stop := event("stop", "s", "idle", 3)
	late := event("late", "s", "running", 2)
	ss := Reduce([]Event{stop, start, late})
	if ss[0].State != "idle" {
		t.Fatal(ss)
	}
	next := event("next", "s", "turn_start", 4)
	if Reduce([]Event{start, stop, next})[0].State != "running" {
		t.Fatal("stop killed session")
	}
	end := event("end", "s", "ended", 5)
	late = event("old", "s", "turn_start", 6)
	if Reduce([]Event{start, end, late})[0].State != "ended" {
		t.Fatal("resurrected")
	}
	newGen := event("new", "s", "turn_start", 1)
	newGen.Generation = 2
	ss = Reduce([]Event{start, end, newGen, late})
	if ss[0].Generation != 2 || ss[0].State != "running" {
		t.Fatal(ss)
	}
}
func TestLateParentAndCycle(t *testing.T) {
	c := event("c", "c", "running", 1)
	c.Relation = "child"
	c.Parent = "r"
	c.ParentGeneration = 1
	if Reduce([]Event{c})[0].Classification != "unclassified" {
		t.Fatal("lost orphan")
	}
	r := event("r", "r", "identity", 1)
	r.Relation = "root"
	if find(t, Reduce([]Event{c, r}), "c").Root != "r" {
		t.Fatal("late parent")
	}
	r.Relation = "child"
	r.Parent = "c"
	r.ParentGeneration = 1
	for _, s := range Reduce([]Event{c, r}) {
		if s.Classification != "cycle" {
			t.Fatal(s)
		}
	}
	r.Relation = "root"
	r.Parent = ""
	r.Generation = 2
	if find(t, Reduce([]Event{c, r}), "c").Root != "" {
		t.Fatal("attached to reused generation")
	}
}
func TestUnorderedStopNotReopenedByTool(t *testing.T) {
	s := event("s", "s", "idle", 0)
	r := event("r", "s", "running", 0)
	ss := Reduce([]Event{s, r})
	if ss[0].State != "idle" || ss[0].Ordering != "best_effort" || ss[0].Liveness != "unknown" {
		t.Fatal(ss)
	}
}
func BenchmarkSnapshotUnchanged(b *testing.B) {
	es := []Event{}
	for i := 0; i < 1000; i++ {
		e := event(fmt.Sprint(i), fmt.Sprint(i), "running", 1)
		e.Relation = "root"
		es = append(es, e)
	}
	s := Snapshot{"epoch:1000", Reduce(es)}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		s.Query(Query{Version: 1, Revision: "epoch:1000"})
	}
}

func TestMissingChildStateDoesNotBecomeIdle(t *testing.T) {
	r := event("r", "r", "idle", 1)
	r.Relation = "root"
	c := event("c", "c", "identity", 1)
	c.Relation = "child"
	c.Parent = "r"
	c.ParentGeneration = 1
	if find(t, Reduce([]Event{r, c}), "r").Aggregate != "unknown" {
		t.Fatal("missing observation became idle")
	}
}
func TestResolutionBeforeRequest(t *testing.T) {
	a := event("a", "s", "request_resolved", 2)
	a.Request = "r"
	b := event("b", "s", "request_open", 1)
	b.Request = "r"
	if Reduce([]Event{a, b})[0].Unresolved != 0 {
		t.Fatal("late open recreated resolved request")
	}
}
