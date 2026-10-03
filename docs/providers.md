# Provider compatibility and evidence

Local versions checked: Claude Code 2.1.281 and Codex CLI 0.159.2. No live provider
session, private transcript, hook installation or paid model request was used.

## Verified contracts

Checked on 2026-10-03:

- Claude's [official hook reference](https://code.claude.com/docs/en/hooks): common
  agent identity, permission requests, subagent lifecycle and MCP elicitation.
- Codex 0.159.2 [generated input schemas](https://github.com/openai/codex/tree/ff6aec96948b70d94983af2641a6b67c94faeff5/codex-rs/hooks/schema/generated),
  especially [PermissionRequest](https://github.com/openai/codex/blob/ff6aec96948b70d94983af2641a6b67c94faeff5/codex-rs/hooks/schema/generated/permission-request.command.input.schema.json).
  Release tag `rust-v0.159.2` resolves to that commit. These are interface evidence;
  no provider implementation was copied.

Both contracts omit a request/tool-use ID from ordinary PermissionRequest input.
A later PostToolUse has a tool-use ID, but that does not establish which anonymous
permission it resolved. Claude's common agent_id distinguishes subagent activity;
MCP elicitation IDs are optional. Neither contract gives a global lifecycle sequence
or process incarnation suitable for mimori's normalized generation contract.

[Anonymous fixtures](../internal/mimori/testdata/hooks.json) exercise those actual
schema shapes, including nullable paths and omitted IDs. Values are synthetic,
with privacy sentinels. The fixtures are checked by `TestProviderContractFixtures`;
actual runtime hook capture remains unperformed.

## Adapter behavior

```sh
printf '%s\n' '{"session_id":"demo","hook_event_name":"UserPromptSubmit","cwd":"/demo"}' |
  mimori hook --provider claude --generation 1
```

Supported lifecycle hooks: SessionStart, UserPromptSubmit, PreToolUse, PostToolUse,
PostToolUseFailure, PreCompact, PostCompact, PermissionRequest, SubagentStart,
SubagentStop, Stop, SessionEnd. Event availability varies by provider. Claude also
supports Elicitation, ElicitationResult and attention-type Notification. Unsupported
or informational events are rejected diagnostically; register only supported events.

| Input | Result |
| --- | --- |
| SessionStart | identity, no activity assertion |
| UserPromptSubmit / tool activity | turn_start / running |
| Stop / SessionEnd | idle / ended |
| Identified request / matching result | add / resolve exactly that ID |
| Permission without ID / unidentified elicitation / attention notification | attention_unknown |
| Unrelated result, Stop or next turn | does not clear anonymous attention |

An anonymous observation sets `attention_unknown` on its session and makes ancestor
`unresolved_count_exact` false. The numeric unresolved count then represents only
known IDs, a lower bound. Aggregate state remains waiting. This is conservative:
the marker may outlive the actual prompt. Only explicit normalized `attention_clear`
from a verified source, a new generation, or session end clears it. No false exact
count or guessed permission resolution is presented.

Claude events with agent_id use the same namespaced child identity as its
SubagentStart/Stop events; ordinary main-thread events establish a session root.
SubagentStart never replaces the parent record. The raw Claude tree fixture checks
two child elicitations and one response end to end through normalization/reduction.
A SubagentStop records idle, not proven process death.

Codex SubagentStart/Stop retain a separate namespaced observed-agent identity and
known parent envelope. Other Codex session identities remain unclassified until a
trusted normalized identity event supplies parentage. The generated schema does
not prove an alias between agent_id and independently reported session_id. No
same-name/cwd merge or unsupported alias is invented. Resolving those aliases is
an outstanding provider integration item.

Raw events use explicit `--generation` (default 1), generated IDs and no seq, so
`ordering: best_effort` remains visible. Replaying a spool record is idempotent;
re-running a raw hook is not necessarily the same provider event. A caller must
increase generation after a verified new incarnation. Receipt-time spool filenames
preserve normal local enqueue order, but are never advertised as authoritative
provider sequence; clock changes and delayed delivery can still reorder raw state.

The parser copies only identity/cwd/correlation metadata. Prompt, tool input/output,
MCP message/content/URL and transcript paths are not saved or read. Tool use IDs or
explicit normalized request IDs are needed for exact sets. Whole transcript access
and process/session-index scanning are not fallback mechanisms.

## Remaining information boundary

Exact raw permission resolution, Codex parent/session aliasing and authoritative
raw ordering cannot be reconstructed from these payloads alone. Options are an
explicit normalized producer, a verified provider event API, or narrowly scoped
additional metadata collection. Choosing/enabling such an ongoing source requires
an integration decision; this PoC enables none. Existing hooks are untouched.
