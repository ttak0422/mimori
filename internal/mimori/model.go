package mimori

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"
)

// Event is the privacy-filtered v1 ingestion contract. Sequence is assigned by
// one authoritative producer per session generation, never by wall-clock time.
type Event struct {
	Version          int    `json:"version"`
	ID               string `json:"event_id"`
	Provider         string `json:"provider"`
	Session          string `json:"session_id"`
	Generation       uint64 `json:"generation"`
	Seq              uint64 `json:"seq,omitempty"`
	Kind             string `json:"kind"`
	Relation         string `json:"relation,omitempty"`
	Parent           string `json:"parent_id,omitempty"`
	ParentGeneration uint64 `json:"parent_generation,omitempty"`
	Request          string `json:"request_id,omitempty"`
	CWD              string `json:"cwd,omitempty"`
	Name             string `json:"name,omitempty"`
	ObservedAt       string `json:"observed_at"`
}

func (e Event) Validate() error {
	if e.Version != 1 || e.ID == "" || e.Provider == "" || e.Session == "" || e.Generation == 0 {
		return errors.New("version=1, event_id, provider, session_id and positive generation required")
	}
	for _, v := range []string{e.ID, e.Provider, e.Session, e.Parent, e.Request, e.Name, e.CWD} {
		if len(v) > 4096 {
			return errors.New("field exceeds 4096 bytes")
		}
	}
	switch e.Kind {
	case "identity", "turn_start", "running", "idle", "ended", "request_open", "request_resolved":
	default:
		return errors.New("unsupported event kind")
	}
	switch e.Relation {
	case "", "unknown", "root":
		if e.Parent != "" {
			return errors.New("parent requires child relation")
		}
	case "child":
		if e.Parent == "" || e.Parent == e.Session || e.ParentGeneration == 0 {
			return errors.New("invalid parent identity")
		}
	default:
		return errors.New("invalid relation")
	}
	if (e.Kind == "request_open" || e.Kind == "request_resolved") && e.Request == "" {
		return errors.New("request_id required")
	}
	if _, err := time.Parse(time.RFC3339Nano, e.ObservedAt); err != nil {
		return errors.New("observed_at must be RFC3339")
	}
	return nil
}

type Session struct {
	Provider           string   `json:"provider"`
	ID                 string   `json:"session_id"`
	Generation         uint64   `json:"generation"`
	Relation           string   `json:"relation"`
	Parent             string   `json:"parent_id,omitempty"`
	ParentGeneration   uint64   `json:"parent_generation,omitempty"`
	Root               string   `json:"root_id,omitempty"`
	CWD                string   `json:"cwd,omitempty"`
	Name               string   `json:"name,omitempty"`
	State              string   `json:"state"`
	Aggregate          string   `json:"aggregate_state"`
	RunningDescendants int      `json:"running_descendants"`
	Unresolved         int      `json:"unresolved_requests"`
	Requests           []string `json:"request_ids"`
	LastEventAt        string   `json:"last_event_at"`
	Liveness           string   `json:"liveness"`
	Ordering           string   `json:"ordering"`
	Classification     string   `json:"classification"`
	stateSeq           uint64
	identitySeq        uint64
	requestSeq         map[string]uint64
	resolved           map[string]bool
	requests           map[string]bool
}

func key(provider, id string) string { return provider + "\x00" + id }

// Reduce is deterministic for authoritative sequences. Unsequenced hooks are
// explicitly best effort; terminal events are absorbing within a generation.
func Reduce(events []Event) []Session {
	m := map[string]*Session{}
	for _, e := range events {
		k := key(e.Provider, e.Session)
		s := m[k]
		if s != nil && s.Generation > e.Generation {
			continue
		}
		if s == nil || s.Generation < e.Generation {
			s = &Session{Provider: e.Provider, ID: e.Session, Generation: e.Generation, Relation: "unknown", State: "unknown", Liveness: "unknown", Ordering: "sequenced", requests: map[string]bool{}, resolved: map[string]bool{}, requestSeq: map[string]uint64{}}
			m[k] = s
		}
		if e.Seq == 0 {
			s.Ordering = "best_effort"
		}
		if s.LastEventAt == "" || after(e.ObservedAt, s.LastEventAt) {
			s.LastEventAt = e.ObservedAt
		}
		if e.Relation != "" && (e.Seq == 0 || e.Seq > s.identitySeq) {
			s.Relation = e.Relation
			s.Parent = e.Parent
			s.ParentGeneration = e.ParentGeneration
			s.identitySeq = e.Seq
		}
		if e.CWD != "" && s.CWD == "" {
			s.CWD = e.CWD
		}
		if e.Name != "" && s.Name == "" {
			s.Name = e.Name
		}
		if s.State == "ended" {
			continue
		}
		switch e.Kind {
		case "request_open", "request_resolved":
			// A resolved request ID is terminal: retries need a fresh request ID.
			if e.Kind == "request_resolved" {
				s.resolved[e.Request] = true
				delete(s.requests, e.Request)
			} else if !s.resolved[e.Request] {
				s.requests[e.Request] = true
			}
		case "ended":
			s.State = "ended"
			s.Liveness = "ended"
			s.requests = map[string]bool{}
		case "turn_start", "running", "idle":
			if e.Seq > 0 && e.Seq <= s.stateSeq {
				continue
			}
			if e.Seq == 0 && e.Kind == "running" && s.State == "idle" {
				continue
			}
			if e.Kind == "idle" {
				s.State = "idle"
			} else {
				s.State = "running"
			}
			s.stateSeq = e.Seq
		}
	}
	out := make([]Session, 0, len(m))
	for _, s := range m {
		s.Classification = "unclassified"
		visited := map[string]bool{}
		p := s
		for p != nil {
			pk := key(p.Provider, p.ID)
			if visited[pk] {
				s.Classification = "cycle"
				break
			}
			visited[pk] = true
			if p.Relation == "root" {
				s.Root = p.ID
				s.Classification = "resolved"
				break
			}
			if p.Relation != "child" {
				break
			}
			next := m[key(p.Provider, p.Parent)]
			if next == nil || next.Generation != p.ParentGeneration {
				break
			}
			p = next
		}
		s.Requests = []string{}
		for r := range s.requests {
			s.Requests = append(s.Requests, r)
		}
		sort.Strings(s.Requests)
		s.Unresolved = len(s.Requests)
		s.Aggregate = s.State
		if s.Unresolved > 0 {
			s.Aggregate = "waiting"
		}
	}
	unknownDescendants := map[string]bool{}
	// Aggregate each descendant into every known ancestor, including unresolved trees.
	for _, s := range m {
		visited := map[string]bool{key(s.Provider, s.ID): true}
		p := s
		for p.Relation == "child" {
			a := m[key(p.Provider, p.Parent)]
			if a == nil || a.Generation != p.ParentGeneration || visited[key(a.Provider, a.ID)] {
				break
			}
			visited[key(a.Provider, a.ID)] = true
			if s.State == "running" {
				a.RunningDescendants++
			}
			a.Unresolved += len(s.Requests)
			if s.State == "unknown" {
				unknownDescendants[key(a.Provider, a.ID)] = true
			}
			p = a
		}
	}
	for _, s := range m {
		if s.Unresolved > 0 {
			s.Aggregate = "waiting"
		} else if s.RunningDescendants > 0 {
			s.Aggregate = "running"
		} else if unknownDescendants[key(s.Provider, s.ID)] && s.State != "running" {
			s.Aggregate = "unknown"
		}
		out = append(out, *s)
	}
	sort.Slice(out, func(i, j int) bool { return key(out[i].Provider, out[i].ID) < key(out[j].Provider, out[j].ID) })
	return out
}
func after(a, b string) bool {
	x, _ := time.Parse(time.RFC3339Nano, a)
	y, _ := time.Parse(time.RFC3339Nano, b)
	return x.After(y)
}
func DecodeEvent(b []byte) (Event, error) {
	var e Event
	err := json.Unmarshal(b, &e)
	if err == nil {
		err = e.Validate()
	}
	return e, err
}
func revision(epoch string, n int) string { return fmt.Sprintf("%s:%d", epoch, n) }
