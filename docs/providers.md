# Provider compatibility and evidence

Compatibility evidence: Claude Code 2.1.281 and Codex CLI 0.159.2 contracts.
Validation uses synthetic fixtures; live hook capture has not been tested.

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
and process/session-index scanning are not fallback mechanisms. The optional
App Server title lookup below uses only known IDs and never broadens this into
a session discovery mechanism.

## Remaining information boundary

Exact raw permission resolution, Codex parent/session aliasing and authoritative
raw ordering cannot be reconstructed from these payloads alone. Options are an
explicit normalized producer, a verified provider event API, or narrowly scoped
additional metadata collection. Such a source must establish the missing identity
and ordering guarantees explicitly; none is enabled by this PoC.

## Provider session titles

The optional query `title` is separate from the normalized producer's `name`.
Consumers should prefer `title`, then `name`, then cwd context, while retaining
both original fields for detail views. Empty titles clear the override. Titles
never identify, merge, reparent, resume, or extend the retention of a session.

### Claude Code

Claude Code 2.1.281 and the [hook reference](https://code.claude.com/docs/en/hooks)
provide `session_title` on SessionStart and UserPromptSubmit. Mimori allowlists
only that field on main-session hooks. A missing, null, empty, or whitespace-only
value clears the previous custom title; other hooks and subagent envelopes do not
change the parent title. This also safely falls back for older versions that omit
the field. Repeated renames arrive on the next supported hook. A rename followed
by no prompt/resume cannot be observed immediately through these hooks; there is
no transcript scan or additional Claude polling process.

Unsequenced hook updates are ordered by mimori's observation timestamp and then
event ID. This protects against delayed spool delivery, not provider events that
arrive at the hook out of order: the provider supplies no rename sequence. A
sequenced normalized producer retains sequence authority. The separate `title`
event may update an already-known generation without changing activity, ordering,
`last_event_at`, or `ended_at`; it cannot create a session or generation.

### Codex App Server (optional)

Set `MIMORI_CODEX_SOCKET` to the absolute path of an **already-running** local
Codex App Server Unix control socket before starting mimori. The existing
`codex app-server daemon version` command reports the daemon's socket when
available. Mimori never starts, resumes, subscribes to, or stops Codex. No socket
configuration means this integration is disabled. An existing mimori daemon
keeps its startup environment until restarted by its owner.

The [App Server contract](https://developers.openai.com/codex/app-server/) and
Codex 0.160.0 generated schema expose `thread/read` with `includeTurns:false`,
`thread.name`, and `thread.sessionId`. Mimori uses a WebSocket over the Unix
socket, performs initialize/initialized, and requests only concrete candidates
already established by ingested hooks. It never calls thread/list or reads the
session index, SQLite files, or rollout files. Returned preview/turn content is
not copied into the cache, database, diagnostics, or query responses.

Identity evidence at `rust-v0.160.0`:

- [Session::session_id](https://github.com/openai/codex/blob/rust-v0.160.0/codex-rs/core/src/session/session.rs)
  is shared by the root and descendant threads; thread_id is concrete.
- [Hook runtime](https://github.com/openai/codex/blob/rust-v0.160.0/codex-rs/core/src/hook_runtime.rs)
  supplies this session ID and the concrete thread ID as the spawned agent_id.
- A main-session SessionStart/UserPromptSubmit supplies the root candidate;
  SubagentStart/Stop supplies the child candidate. Each metadata response must
  match both the requested concrete `id` and expected root `sessionId`. Missing
  fields, mismatches, and unsupported schemas are discarded, never inferred from
  cwd, name, or transcript paths. Older stored observations need a new qualifying
  hook before they have a candidate.

Refresh is demand-driven by normal mimori queries, shared across editors, at most
once per 30 seconds, up to 16 retained candidates per batch with a rotating cursor.
Each batch has a two-second deadline, 256 KiB per frame, 2 MiB total, and a 64-frame
limit. The cache holds at most 256 entries for five minutes and is scoped to the
session generation and concrete thread. Expired ended sessions are excluded
before candidates are selected. No queries means no recurring metadata work.
Large candidate sets take multiple batches and may exceed the cache horizon;
these rows keep their ordinary fallback labels.

Renames from other clients are detected by periodic reads, not by assuming
cross-process delivery of thread/name/updated. A successful null/empty name clears
the override. Connection/contract failure drops stale cache values and reports
`title_status: unavailable` without changing collector health. Unsupported or
inaccessible providers keep their existing labels. This covers a reachable local
App Server's persisted threads, not arbitrary cloud tasks or standalone embedded
instances with no exposed compatible endpoint. Metadata updates/expiry participate
in query revisions, including detail queries; they do not enqueue lifecycle events
or alter the five-minute ended-session retention deadline.
