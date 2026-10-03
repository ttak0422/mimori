package mimori

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// NormalizeHook intentionally decodes an allowlist. Prompt, tool input/output,
// transcript paths and assistant messages are never copied into the spool.
func NormalizeHook(provider string, b []byte, generation uint64) (Event, error) {
	var h struct {
		Session string `json:"session_id"`
		Event   string `json:"hook_event_name"`
		CWD     string `json:"cwd"`
		Agent   string `json:"agent_id"`
		Tool    string `json:"tool_use_id"`
		Request string `json:"request_id"`
	}
	if provider != "claude" && provider != "codex" {
		return Event{}, errors.New("hook provider must be claude or codex")
	}
	if err := json.Unmarshal(b, &h); err != nil {
		return Event{}, errors.New("invalid hook JSON")
	}
	e := Event{Version: 1, ID: NewID(), Provider: provider, Session: h.Session, Generation: generation, ObservedAt: time.Now().UTC().Format(time.RFC3339Nano), CWD: h.CWD}
	switch h.Event {
	case "SessionStart":
		e.Kind = "identity" // No proof of root identity in common hook fields.
	case "UserPromptSubmit":
		e.Kind = "turn_start"
	case "PreToolUse", "PreCompact", "PostCompact":
		e.Kind = "running"
	case "PostToolUse", "PostToolUseFailure":
		if h.Tool != "" {
			e.Kind = "request_resolved"
			e.Request = "tool:" + h.Tool
		} else {
			e.Kind = "running"
		}
	case "PermissionRequest":
		e.Kind = "request_open"
		if h.Tool != "" {
			e.Request = "tool:" + h.Tool
		} else if h.Request != "" {
			e.Request = "request:" + h.Request
		} else {
			return Event{}, errors.New("permission event lacks correlatable request ID; use normalized ingest")
		}
	case "Stop":
		e.Kind = "idle"
	case "SessionEnd":
		e.Kind = "ended"
	case "SubagentStart", "SubagentStop":
		if h.Agent == "" {
			return Event{}, errors.New("subagent event lacks agent_id")
		}
		// This is a provider-scoped observed agent identity, not a session ID alias.
		e.Parent = h.Session
		e.ParentGeneration = generation
		e.Relation = "child"
		e.Session = fmt.Sprintf("agent:%d:%s:%s", len(h.Session), h.Session, h.Agent)
		e.Kind = "turn_start"
		if h.Event == "SubagentStop" {
			e.Kind = "idle"
		}
	default:
		return Event{}, errors.New("unsupported hook event")
	}
	return e, e.Validate()
}
