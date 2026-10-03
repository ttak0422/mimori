package mimori

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestValidatorCannotCrossQueryScope(t *testing.T) {
	r := event("r", "r", "idle", 1)
	r.Relation = "root"
	s := Snapshot{"epoch:1", Reduce([]Event{r})}
	all := s.Query(Query{Version: 1})
	q := Query{Version: 1, Provider: "test", Revision: all.Revision}
	filtered := s.Query(q)
	if filtered.Unchanged || !filtered.Complete || filtered.Revision == all.Revision {
		t.Fatal(filtered)
	}
	q.Revision = filtered.Revision
	if !s.Query(q).Unchanged {
		t.Fatal("same scope missed unchanged")
	}
	detail := s.Query(Query{Version: 1, Provider: "test", Session: "r", Revision: filtered.Revision})
	if detail.Unchanged || detail.Session == nil {
		t.Fatal(detail)
	}
	missing := s.Query(Query{Version: 1, Provider: "test", Session: "missing", Revision: all.Revision})
	if missing.ErrorCode != "not_found" {
		t.Fatal(missing)
	}
	empty := s.Query(Query{Version: 1, Provider: "missing"})
	if !empty.Complete || empty.Error != "" || len(empty.Roots) != 0 {
		t.Fatal(empty)
	}
	if s.Query(Query{Version: 2}).ErrorCode != "unsupported_version" {
		t.Fatal("version accepted")
	}
}
func TestResponseLimitNeverReturnsPartialSnapshot(t *testing.T) {
	r := Response{Version: 1, Revision: "v", Complete: true}
	for i := 0; i < 1100; i++ {
		r.Roots = append(r.Roots, Session{ID: fmt.Sprint(i), Name: strings.Repeat("n", 4096)})
	}
	var wire bytes.Buffer
	if err := writeResponse(&wire, r); err != nil {
		t.Fatal(err)
	}
	var got Response
	if err := json.Unmarshal(wire.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.ErrorCode != "response_too_large" || got.Complete || len(got.Roots) != 0 || wire.Len() > 1024 {
		t.Fatalf("partial or oversized response: %d %+v", wire.Len(), got)
	}
}
func TestFieldSeparatorCannotCollideIdentity(t *testing.T) {
	e := event("e", "s", "idle", 1)
	e.Provider = "p\x00q"
	if e.Validate() == nil {
		t.Fatal("NUL provider accepted")
	}
}
