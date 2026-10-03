package mimori

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func tempState(t *testing.T) string {
	t.Helper()
	d, err := os.MkdirTemp("/tmp", "mm-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(d) })
	if err := EnsureDir(d); err != nil {
		t.Fatal(err)
	}
	return d
}
func TestDurableReplayAndConflict(t *testing.T) {
	d := tempState(t)
	e := event("1", "r", "running", 1)
	if err := Enqueue(d, e); err != nil {
		t.Fatal(err)
	}
	s, err := OpenStore(d)
	if err != nil {
		t.Fatal(err)
	}
	if !s.Drain(d, func(s string) { t.Log(s) }) {
		t.Fatal("not drained")
	}
	if _, err = s.Apply(e); err != nil {
		t.Fatal(err)
	}
	e.Kind = "idle"
	if _, err = s.Apply(e); err == nil {
		t.Fatal("ID conflict accepted")
	}
	s.Close()
	s, err = OpenStore(d)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if len(s.events) != 1 {
		t.Fatal("lost persisted state")
	}
	if err = Enqueue(d, s.events[0]); err != nil {
		t.Fatal(err)
	}
	if s.Drain(d, func(s string) { t.Log(s) }) {
		t.Fatal("replay changed revision")
	}
	st, _ := os.Stat(filepath.Join(d, "state.db"))
	if st.Mode().Perm() != 0600 {
		t.Fatal(st.Mode())
	}
}
func TestConcurrentSpoolAndCorruption(t *testing.T) {
	d := tempState(t)
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			e := event(NewID(), NewID(), "running", 1)
			if err := Enqueue(d, e); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	os.WriteFile(filepath.Join(d, "spool", "bad.json"), []byte("broken"), 0600)
	s, err := OpenStore(d)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	s.Drain(d, func(s string) { t.Log(s) })
	if len(s.events) != 32 {
		t.Fatalf("got %d", len(s.events))
	}
	q, _ := os.ReadDir(filepath.Join(d, "quarantine"))
	if len(q) != 1 {
		t.Fatal(q)
	}
}
func TestPermissionsAndCapacity(t *testing.T) {
	d := tempState(t)
	os.Chmod(d, 0755)
	if EnsureDir(d) == nil {
		t.Fatal("insecure directory accepted")
	}
	os.Chmod(d, 0700)
	for i := 0; i < MaxSpool; i++ {
		if err := os.WriteFile(filepath.Join(d, "spool", NewID()), nil, 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := Enqueue(d, event("x", "x", "idle", 1)); err == nil || !strings.Contains(err.Error(), "spool full") {
		t.Fatal(err)
	}
}
func TestSocketEndToEnd(t *testing.T) {
	d := tempState(t)
	e := event("root", "root", "running", 1)
	e.Relation = "root"
	if err := Enqueue(d, e); err != nil {
		t.Fatal(err)
	}
	start := func() (context.CancelFunc, chan error) {
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() { done <- Serve(ctx, d, func(s string) { t.Log(s) }) }()
		return cancel, done
	}
	cancel, done := start()
	defer cancel()
	wait := func() Response {
		t.Helper()
		until := time.Now().Add(5 * time.Second)
		for time.Now().Before(until) {
			r, err := Fetch(d, Query{Version: 1})
			if err == nil {
				return r
			}
			time.Sleep(10 * time.Millisecond)
		}
		select {
		case err := <-done:
			t.Fatalf("daemon failed: %v", err)
		default:
			t.Fatal("daemon unavailable")
		}
		return Response{}
	}
	r := wait()
	if len(r.Roots) != 1 {
		t.Fatal(r)
	}
	if err := Serve(context.Background(), d, func(s string) { t.Log(s) }); err == nil {
		t.Fatal("second daemon accepted")
	}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			x, err := Fetch(d, Query{Version: 1, Revision: r.Revision})
			if err != nil || !x.Unchanged {
				t.Errorf("%+v %v", x, err)
			}
		}()
	}
	wg.Wait()
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	cancel2, done2 := start()
	defer cancel2()
	r2 := wait()
	if r.Revision == r2.Revision || len(r2.Roots) != 1 {
		t.Fatal("restart failed to resync")
	}
	cancel2()
	if err := <-done2; err != nil {
		t.Fatal(err)
	}
}
func TestHookPrivacyAndIdentity(t *testing.T) {
	for _, provider := range []string{"claude", "codex"} {
		b := []byte(`{"session_id":"parent","hook_event_name":"SubagentStart","agent_id":"child","prompt":"SECRET","transcript_path":"SECRET","tool_input":{"secret":"SECRET"}}`)
		e, err := NormalizeHook(provider, b, 1)
		if err != nil {
			t.Fatal(err)
		}
		out, _ := json.Marshal(e)
		if strings.Contains(string(out), "SECRET") || e.Session == "parent" || e.Parent != "parent" {
			t.Fatal(string(out))
		}
		e, err = NormalizeHook(provider, []byte(`{"session_id":"parent","hook_event_name":"Stop"}`), 1)
		if err != nil || e.Kind != "idle" {
			t.Fatal(e, err)
		}
		if _, err = NormalizeHook(provider, []byte(`{"session_id":"p","hook_event_name":"PermissionRequest"}`), 1); err == nil {
			t.Fatal("invented request ID")
		}
	}
}

func TestConflictsAndSymlinksQuarantined(t *testing.T) {
	d := tempState(t)
	s, err := OpenStore(d)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	e := event("one", "s", "idle", 1)
	if _, err = s.Apply(e); err != nil {
		t.Fatal(err)
	}
	e.ID = "two"
	if err = Enqueue(d, e); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "outside")
	os.WriteFile(outside, []byte("private"), 0600)
	if err = os.Symlink(outside, filepath.Join(d, "spool", "link.json")); err != nil {
		t.Fatal(err)
	}
	s.Drain(d, func(s string) { t.Log(s) })
	q, _ := os.ReadDir(filepath.Join(d, "quarantine"))
	if len(q) != 2 {
		t.Fatalf("quarantine count %d", len(q))
	}
	b, _ := os.ReadFile(outside)
	if string(b) != "private" {
		t.Fatal("followed symlink")
	}
}
