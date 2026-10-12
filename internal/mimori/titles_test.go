package mimori

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func strptr(s string) *string { return &s }

func TestClaudeTitlesRenameClearAndScope(t *testing.T) {
	var events []Event
	for i, payload := range []string{
		`{"session_id":"r","hook_event_name":"SessionStart","session_title":"first","prompt":"DO_NOT_STORE"}`,
		`{"session_id":"r","hook_event_name":"UserPromptSubmit","session_title":"renamed"}`,
		`{"session_id":"r","hook_event_name":"Stop","session_title":"ignored"}`,
		`{"session_id":"r","hook_event_name":"UserPromptSubmit","agent_id":"child","session_title":"parent-title"}`,
	} {
		e, err := NormalizeHook("claude", []byte(payload), 1)
		if err != nil {
			t.Fatal(err)
		}
		e.ObservedAt = time.Date(2026, 10, 12, 0, 0, i, 0, time.UTC).Format(time.RFC3339Nano)
		events = append(events, e)
	}
	row := find(t, Reduce(events), "r")
	if row.Title != "renamed" || row.Name != "" {
		t.Fatal(row)
	}
	child := find(t, Reduce(events), "agent:1:r:child")
	if child.Title != "" {
		t.Fatal("parent title leaked to child")
	}
	for _, raw := range []string{``, `,"session_title":""`, `,"session_title":null`} {
		e, err := NormalizeHook("claude", []byte(`{"session_id":"r","hook_event_name":"UserPromptSubmit"`+raw+`}`), 1)
		if err != nil || e.Title == nil {
			t.Fatalf("clear: %+v %v", e, err)
		}
		e.ObservedAt = "2026-10-12T00:01:00Z"
		if got := find(t, Reduce(append(events, e)), "r"); got.Title != "" {
			t.Fatal(got)
		}
	}
	b, _ := json.Marshal(events)
	if strings.Contains(string(b), "DO_NOT_STORE") {
		t.Fatal("copied prompt")
	}
}

func TestTitleOrderingAndLifecycleIsolation(t *testing.T) {
	start := event("start", "r", "idle", 1)
	start.Name = "task"
	start.Relation = "root"
	ended := event("end", "r", "ended", 2)
	first := event("a", "r", "title", 0)
	first.Title = strptr("first")
	first.ObservedAt = "2026-10-03T16:01:00Z"
	rename := first
	rename.ID = "b"
	rename.Title = strptr("renamed")
	rename.ObservedAt = "2026-10-03T16:02:00Z"
	clear := rename
	clear.ID = "c"
	clear.Title = strptr("")
	base := Reduce([]Event{start, ended})[0]
	for _, updates := range [][]Event{{first, rename, clear}, {clear, first, rename}} {
		events := append([]Event{start, ended}, updates...)
		got := Reduce(events)[0]
		if got.Title != "" || got.Name != "task" || got.State != base.State || got.Aggregate != base.Aggregate || got.EndedAt != base.EndedAt || got.LastEventAt != base.LastEventAt || got.Ordering != base.Ordering {
			t.Fatal(got)
		}
		cutoff, _ := time.Parse(time.RFC3339Nano, base.EndedAt)
		if len((Snapshot{"v", Reduce(events)}).queryAt(Query{Version: 1}, cutoff.Add(EndedRetention)).Roots) != 0 {
			t.Fatal("title extended retention")
		}
	}
	authoritative := rename
	authoritative.ID = "seq"
	authoritative.Seq = 4
	stale := first
	stale.Seq = 3
	stale.ObservedAt = "2026-10-03T17:00:00Z"
	if Reduce([]Event{start, authoritative, stale, clear})[0].Title != "renamed" {
		t.Fatal("title sequence rolled back")
	}
	wrong := rename
	wrong.Generation = 2
	if Reduce([]Event{start, wrong})[0].Generation != 1 || len(Reduce([]Event{rename})) != 0 {
		t.Fatal("metadata created generation")
	}
	resume := start
	resume.Generation = 2
	if got := Reduce([]Event{start, rename, resume, first})[0]; got.Title != "" || got.Generation != 2 {
		t.Fatal(got)
	}
}

func TestCodexTitleIdentityCandidates(t *testing.T) {
	root, err := NormalizeHook("codex", []byte(`{"session_id":"root-id","hook_event_name":"SessionStart"}`), 1)
	if err != nil || root.ThreadID != "root-id" {
		t.Fatal(root, err)
	}
	child, err := NormalizeHook("codex", []byte(`{"session_id":"root-id","agent_id":"thread-child","hook_event_name":"SubagentStart"}`), 1)
	if err != nil || child.ThreadID != "thread-child" || child.Parent != "root-id" || child.Session == child.ThreadID {
		t.Fatal(child, err)
	}
	other, err := NormalizeHook("codex", []byte(`{"session_id":"root-id","agent_id":"thread-child","hook_event_name":"UserPromptSubmit"}`), 1)
	if err != nil || other.ThreadID != "" || other.Title != nil {
		t.Fatal("child activity inferred root title", other, err)
	}
}

// Anonymous real WebSocket-over-Unix fixture. No provider process or files.
func titleServer(t *testing.T, handle func(string) any) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "mm-title-")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "app.sock")
	listener, err := net.Listen("unix", path)
	if err != nil {
		os.RemoveAll(dir)
		t.Fatal(err)
	}
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		for {
			var q struct {
				ID     int    `json:"id"`
				Method string `json:"method"`
				Params struct {
					ThreadID     string `json:"threadId"`
					IncludeTurns bool   `json:"includeTurns"`
				} `json:"params"`
			}
			if conn.ReadJSON(&q) != nil {
				return
			}
			switch q.Method {
			case "initialized":
				continue
			case "initialize":
				_ = conn.WriteJSON(map[string]any{"id": q.ID, "result": map[string]string{"userAgent": "fixture"}})
			case "thread/read":
				if q.Params.IncludeTurns {
					t.Error("requested conversation")
				}
				_ = conn.WriteJSON(map[string]any{"id": q.ID, "result": handle(q.Params.ThreadID)})
			default:
				t.Errorf("unexpected method %s", q.Method)
				return
			}
		}
	})}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { _ = server.Close(); _ = os.RemoveAll(dir) })
	return path
}
func TestCodexMetadataContractAndMinimization(t *testing.T) {
	path := titleServer(t, func(id string) any {
		thread := map[string]any{"id": id, "sessionId": "r", "name": "Renamed", "turns": []any{}, "preview": "DO_NOT_STORE"}
		switch id {
		case "clear":
			thread["name"] = nil
		case "wrong-id":
			thread["id"] = "other"
		case "wrong-session":
			thread["sessionId"] = "other"
		case "old":
			delete(thread, "name")
		case "turns":
			thread["turns"] = []string{"DO_NOT_STORE"}
		}
		return map[string]any{"thread": thread}
	})
	rows := []Session{{Provider: "codex", ID: "r", ThreadID: "r", Generation: 1}}
	for _, id := range []string{"child", "clear", "wrong-id", "wrong-session", "old", "turns"} {
		rows = append(rows, Session{Provider: "codex", ID: "observed-" + id, Parent: "r", ThreadID: id, Generation: 1})
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	got, err := fetchCodexTitles(ctx, path, rows)
	if err != nil || len(got) != 3 || got[sessionTitleKey(rows[0])] != "Renamed" || got[sessionTitleKey(rows[1])] != "Renamed" {
		t.Fatal(got, err)
	}
	if value, ok := got[sessionTitleKey(rows[2])]; !ok || value != "" {
		t.Fatal("null did not clear")
	}
	b, _ := json.Marshal(gotValues(got))
	if strings.Contains(string(b), "DO_NOT_STORE") {
		t.Fatal("preview copied")
	}
}
func gotValues(m map[titleKey]string) []string {
	var out []string
	for _, v := range m {
		out = append(out, v)
	}
	return out
}

func TestTitleCacheDemandBoundsRevisionAndExpiry(t *testing.T) {
	cache := newTitleCache("/fixture.sock")
	var mu sync.Mutex
	var batches [][]Session
	value := "First"
	cache.fetch = func(_ context.Context, _ string, rows []Session) (map[titleKey]string, error) {
		mu.Lock()
		defer mu.Unlock()
		batches = append(batches, rows)
		result := map[titleKey]string{}
		for _, row := range rows {
			result[sessionTitleKey(row)] = value
		}
		return result, nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	now := time.Now()
	s := Snapshot{Revision: "base"}
	for i := 0; i < 40; i++ {
		s.Sessions = append(s.Sessions, Session{Provider: "codex", ID: string(rune('a' + i)), ThreadID: string(rune('a' + i)), Generation: 1, State: "idle", Aggregate: "idle", Relation: "root", Root: string(rune('a' + i)), Classification: "resolved"})
	}
	original := append([]Session(nil), s.Sessions...)
	before, _ := cache.view(ctx, s, now)
	cache.close()
	first, _ := cache.view(ctx, s, now)
	mu.Lock()
	count := len(batches)
	size := len(batches[0])
	mu.Unlock()
	if count != 1 || size != titleBatch || first.Sessions[0].Title != "First" || first.Revision == before.Revision {
		t.Fatal("batch/cache revision", count, size)
	}
	if !first.Query(Query{Version: 1, Revision: first.Query(Query{Version: 1}).Revision}).Unchanged {
		t.Fatal("unstable validator")
	}
	mu.Lock()
	value = "Second"
	mu.Unlock()
	_, _ = cache.view(ctx, s, now.Add(titleInterval))
	cache.close()
	mu.Lock()
	if batches[0][0].ID == batches[1][0].ID {
		t.Fatal("cursor starved later rows")
	}
	mu.Unlock()
	// Revisit the first batch to observe another process's rename without events.
	cache.mu.Lock()
	cache.cursor = 0
	cache.mu.Unlock()
	_, _ = cache.view(ctx, s, now.Add(2*titleInterval))
	cache.close()
	second, _ := cache.view(ctx, s, now.Add(2*titleInterval))
	if second.Sessions[0].Title != "Second" || first.Revision == second.Revision {
		t.Fatal("rename not reflected")
	}
	mu.Lock()
	value = ""
	mu.Unlock()
	cache.mu.Lock()
	cache.cursor = 0
	cache.mu.Unlock()
	_, _ = cache.view(ctx, s, now.Add(3*titleInterval))
	cache.close()
	cleared, _ := cache.view(ctx, s, now.Add(3*titleInterval))
	if cleared.Sessions[0].Title != "" || cleared.Revision == second.Revision {
		t.Fatal("clear not reflected")
	}
	if !reflect.DeepEqual(s.Sessions, original) {
		t.Fatal("mutated collector state")
	}
	cancel()
	expired, _ := cache.view(ctx, s, now.Add(time.Hour))
	if expired.Sessions[16].Title != "" {
		t.Fatal("stale metadata did not expire")
	}
}

func TestTitleCacheUnavailableGenerationAndBoundedIO(t *testing.T) {
	s := Snapshot{"v", []Session{{Provider: "codex", ID: "r", ThreadID: "r", Generation: 1, State: "idle", Aggregate: "idle"}}}
	cache := newTitleCache("/does/not/exist")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	_, _ = cache.view(ctx, s, time.Now())
	cache.close()
	view, status := cache.view(ctx, s, time.Now())
	if status != "unavailable" || view.Sessions[0].Title != "" {
		t.Fatal(view, status)
	}
	// A previous incarnation must never inherit cached metadata.
	cache.mu.Lock()
	cache.entries[sessionTitleKey(s.Sessions[0])] = cachedTitle{"old", time.Now()}
	cache.mu.Unlock()
	s.Sessions[0].Generation = 2
	view, _ = cache.view(ctx, s, time.Now())
	if view.Sessions[0].Title != "" {
		t.Fatal("cross-generation title")
	}
	// A server response larger than the frame limit is dropped with a generic,
	// payload-free error rather than reaching a diagnostic/log.
	path := titleServer(t, func(id string) any {
		return map[string]any{"thread": map[string]any{"id": id, "sessionId": "r", "name": "DO_NOT_LOG" + strings.Repeat("x", 300*1024)}}
	})
	results, err := fetchCodexTitles(ctx, path, s.Sessions)
	if err == nil || len(results) != 0 || strings.Contains(err.Error(), "DO_NOT_LOG") {
		t.Fatal("unbounded or leaking error")
	}
}

func TestDaemonTitleRefreshAndLifecycle(t *testing.T) {
	var mu sync.Mutex
	name := "first"
	path := titleServer(t, func(id string) any {
		mu.Lock()
		defer mu.Unlock()
		return map[string]any{"thread": map[string]any{"id": id, "sessionId": id, "name": name, "turns": []any{}, "preview": "PRIVATE_PROMPT"}}
	})
	t.Setenv("MIMORI_CODEX_SOCKET", path)
	dir, err := os.MkdirTemp("/tmp", "mm-title-query-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	e := event("r", "r", "idle", 1)
	e.Provider = "codex"
	e.ThreadID = "r"
	e.Relation = "root"
	e.Name = "task"
	if err := Enqueue(dir, e); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	ready := make(chan struct{})
	done := make(chan error, 1)
	go func() { done <- ServeReady(ctx, dir, func(s string) { t.Error(s) }, func() { close(ready) }) }()
	defer func() {
		cancel()
		if err := <-done; err != nil {
			t.Error(err)
		}
	}()
	select {
	case <-ready:
	case <-time.After(3 * time.Second):
		t.Fatal("startup")
	}
	before, err := Fetch(dir, Query{Version: 1})
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	var after Response
	for {
		after, err = Fetch(dir, Query{Version: 1, Revision: before.Revision})
		if err != nil {
			t.Fatal(err)
		}
		if len(after.Roots) == 1 && after.Roots[0].Title == "first" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("title never reached cached queries", after)
		}
		time.Sleep(10 * time.Millisecond)
	}
	row := after.Roots[0]
	if row.Name != "task" || row.State != "idle" || row.LastEventAt != e.ObservedAt || row.EndedAt != "" {
		t.Fatal("metadata changed lifecycle", row)
	}
	same, err := Fetch(dir, Query{Version: 1, Revision: after.Revision})
	if err != nil || !same.Unchanged {
		t.Fatal(same, err)
	}
	detail, err := Fetch(dir, Query{Version: 1, Provider: "codex", Session: "r"})
	if err != nil || detail.Session.Title != "first" {
		t.Fatal(detail, err)
	}
	b, _ := json.Marshal(after)
	if strings.Contains(string(b), "PRIVATE_PROMPT") {
		t.Fatal("preview reached client")
	}
}
