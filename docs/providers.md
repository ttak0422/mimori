# Provider compatibility and evidence

Local binaries identified on 2026-10-03: Claude Code 2.1.281 and Codex CLI 0.159.2.
Only version commands were run: no agent session, private transcript or paid model
request was started. The adapters are fixture-tested; a live hook integration is
not claimed.

Claude's public [hook reference](https://code.claude.com/docs/en/hooks) was checked
on 2026-10-03. It supplies common session identity and event names and documents
`SubagentStart`/`SubagentStop` with agent identity. Codex compatibility is provisional,
based on the user's existing hook configuration contract and anonymized synthetic
fixtures, not a claim that every build emits every field. Before installing any
hook, inspect that installed provider's current `/hooks` interface and field contract.

```sh
printf '%s\n' '{"session_id":"demo","hook_event_name":"UserPromptSubmit","cwd":"/demo"}' |
  mimori hook --provider claude --generation 1
# Same allowlisted envelope is supported with --provider codex.
```

The supported raw events are SessionStart, UserPromptSubmit, PreToolUse,
PostToolUse, PostToolUseFailure, PreCompact, PostCompact, PermissionRequest,
SubagentStart, SubagentStop, Stop and SessionEnd. Availability depends on provider.
Unsupported events fail diagnostically, so only register supported events.

| Hook | Normalization |
| --- | --- |
| SessionStart | identity with unknown parentage |
| UserPromptSubmit | turn_start |
| PreToolUse / compact hooks | running |
| PostToolUse / PostToolUseFailure | Resolve matching `tool_use_id`; otherwise running |
| PermissionRequest | Open `tool:<tool_use_id>` or `request:<request_id>`; reject if both absent |
| SubagentStart / SubagentStop | Separate namespaced agent identity, known parent; running / idle |
| Stop | idle, never process end |
| SessionEnd | ended |

An `agent_id` is not treated as a session ID. Subagent events identify a separate
observed agent under the envelope's parent session, never replace the parent
record, and do not automatically alias a later independent child session. Alias
reconciliation needs a verified provider contract. A SubagentStop means completed
work, not proven process death. Shared parent session IDs cannot attribute every
subagent permission request; normalized events with explicit identity are needed
for precise child-level attribution.

Raw common fields do not prove that a session is a root. Raw sessions remain
unclassified until a trusted producer submits a normalized `identity` with
`relation: root` or `child`. The demo uses explicit verified identities. This avoids
silently merging unrelated sessions or guessing that all SessionStart events are roots.

Raw events lack an authoritative generation/order/event ID, so the adapter uses
`--generation` (default 1), random event IDs, and no seq. Retrying the same spool
record is idempotent; independently re-running a raw hook is not deduplicated as
the same event (request IDs still deduplicate requests). A launcher that knows a
new incarnation must provide an increased generation after an explicit end.
Do not use seconds timestamps as sequence numbers. These are current limitations,
not inferred lifecycle guarantees.

PermissionRequest without a correlatable ID is rejected instead of recording a
wait that could never be accurately resolved. Notifications, MCP elicitation and
provider-specific denial events require additional verified mappings. A request
opened only with `request_id` must be resolved through normalized ingest; PostToolUse
only proves resolution when the tool ID matches. No unrelated event clears waiting.

The parser copies only session/event/cwd/agent/tool/request identifiers. It never
reads transcript_path, prompt, tool_input, tool_response, assistant messages,
provider config, process lists or session indexes. Example fixtures contain no
real session data. Installing hooks and replacing the existing komado path are
separate, explicit migration work; no active provider settings were changed.
