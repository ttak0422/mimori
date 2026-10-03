package mimori

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func TestProviderContractFixtures(t *testing.T) {
	b, err := os.ReadFile("testdata/hooks.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixtures []struct {
		Name, Provider, Kind, Session, Parent, Request string
		Payload                                        json.RawMessage
	}
	if err = json.Unmarshal(b, &fixtures); err != nil {
		t.Fatal(err)
	}
	for _, f := range fixtures {
		t.Run(f.Name, func(t *testing.T) {
			e, err := NormalizeHook(f.Provider, f.Payload, 1)
			if err != nil {
				t.Fatal(err)
			}
			if e.Kind != f.Kind || e.Session != f.Session || e.Parent != f.Parent || e.Request != f.Request {
				t.Fatalf("%+v", e)
			}
			b, _ := json.Marshal(e)
			if strings.Contains(string(b), "DO_NOT_STORE") {
				t.Fatal("private input copied")
			}
		})
	}
}
func TestUncorrelatedAttentionSurvivesUnrelatedActivity(t *testing.T) {
	root := event("r", "r", "idle", 1)
	root.Relation = "root"
	wait := event("w", "c", "attention_unknown", 1)
	wait.Relation = "child"
	wait.Parent = "r"
	wait.ParentGeneration = 1
	stop := event("stop", "c", "idle", 2)
	result := event("result", "c", "request_resolved", 3)
	result.Request = "unrelated"
	es := []Event{root, wait, stop, result}
	r := find(t, Reduce(es), "r")
	if r.Aggregate != "waiting" || r.UnresolvedExact || r.Unresolved != 0 {
		t.Fatalf("%+v", r)
	}
	clear := event("clear", "c", "attention_clear", 4)
	es = append(es, clear)
	r = find(t, Reduce(es), "r")
	if !r.UnresolvedExact || r.Aggregate == "waiting" {
		t.Fatalf("%+v", r)
	}
	// A stale sequenced uncertainty report cannot undo a verified clear.
	wait.ID = "late"
	es = append(es, wait)
	if !find(t, Reduce(es), "r").UnresolvedExact {
		t.Fatal("stale uncertainty resurrected")
	}
}

func TestClaudeRawTreeAndCorrelatedWaits(t *testing.T) {
	payloads := []string{
		`{"session_id":"root","hook_event_name":"SessionStart"}`,
		`{"session_id":"root","hook_event_name":"SubagentStart","agent_id":"a"}`,
		`{"session_id":"root","hook_event_name":"SubagentStart","agent_id":"b"}`,
		`{"session_id":"root","hook_event_name":"Elicitation","agent_id":"a","elicitation_id":"a1","mcp_server_name":"example"}`,
		`{"session_id":"root","hook_event_name":"Elicitation","agent_id":"b","elicitation_id":"b1","mcp_server_name":"example"}`,
		`{"session_id":"root","hook_event_name":"ElicitationResult","agent_id":"a","elicitation_id":"a1","mcp_server_name":"example"}`,
	}
	es := []Event{}
	for _, p := range payloads {
		e, err := NormalizeHook("claude", []byte(p), 1)
		if err != nil {
			t.Fatal(err)
		}
		es = append(es, e)
	}
	r := (Snapshot{"rev", Reduce(es)}).Query(Query{Version: 1})
	if len(r.Roots) != 1 || len(r.Unclassified) != 0 || r.Roots[0].Unresolved != 1 || !r.Roots[0].UnresolvedExact || r.Roots[0].Aggregate != "waiting" {
		t.Fatalf("%+v", r)
	}
}

func TestElicitationIDsAreServerScoped(t *testing.T) {
	var events []Event
	for _, server := range []string{"one", "two"} {
		p := `{"session_id":"r","hook_event_name":"Elicitation","elicitation_id":"same","mcp_server_name":"` + server + `"}`
		e, err := NormalizeHook("claude", []byte(p), 1)
		if err != nil {
			t.Fatal(err)
		}
		events = append(events, e)
	}
	e, err := NormalizeHook("claude", []byte(`{"session_id":"r","hook_event_name":"ElicitationResult","elicitation_id":"same","mcp_server_name":"one"}`), 1)
	if err != nil {
		t.Fatal(err)
	}
	events = append(events, e)
	s := Reduce(events)[0]
	if s.Unresolved != 1 || !s.UnresolvedExact {
		t.Fatal(s)
	}
}
