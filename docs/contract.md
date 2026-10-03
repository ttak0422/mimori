# PoC contract v1

## Ingest and identity

`mimori ingest` reads one JSON event from stdin. Required fields:
`version: 1`, `event_id`, `provider`, `session_id`, positive integer `generation`,
`kind`, `observed_at` (RFC3339). Optional fields are `seq`, `relation`, `parent_id`,
`parent_generation`, `request_id`, `cwd`, and `name`. Unknown fields are discarded.
Each string is limited to 4096 bytes and an input to 64 KiB. IDs are opaque and
never used as filesystem paths. Do not put secrets in IDs, names or working paths.

An authoritative producer owns the session generation and strictly increasing
positive `seq`. A generation is an incarnation, not a turn. Events can arrive out
of order. A new generation replaces the prior incarnation; older generations are
ignored. Within one generation, state and relationship each have their own last
sequence. Sequence reuse with a different event is quarantined. Duplicate event
IDs must contain exactly the same normalized event; conflicting reuse is quarantined.
Deduplication is scoped by provider. `observed_at` is never used as a sequence.

When `seq` is absent/zero, state is marked `ordering: best_effort`. Unordered hooks
cannot distinguish all delayed events from fresh ones. In particular, delayed
`UserPromptSubmit` and `Stop` may yield a stale state; they cannot resurrect an
explicitly ended incarnation. Mixed ordered/unordered streams also remain best
effort. Use the normalized contract when stronger ordering is required.

`relation` is `root`, `child`, or `unknown` (also the default). Child events require
`parent_id` and `parent_generation` from the same provider. Unknown parentage,
missing parents, cross-generation parent references, and cycles remain visible in
`unclassified`. Late authoritative identity events resolve trees. A root is domain
identity, independent of which terminal or sidebar happens to display it.

## State transitions

| Event | Effect |
| --- | --- |
| identity | Update supplied relationship; does not assert activity |
| turn_start | running, including after idle |
| running | running; unordered activity cannot override an observed idle |
| idle | turn complete; can start another turn |
| ended | terminal for this generation; clears its own requests |
| request_open | Add one unresolved request ID |
| request_resolved | Permanently resolve this request ID, even if received before open |
| attention_unknown | Record attention whose exact request correlation is missing |
| attention_clear | Clear that uncertainty only on verified external evidence |

A retry/new request needs a new request ID. Completing one child, receiving a
notification, or finishing a turn does not clear any other request. Ended parents
can still aggregate active children. For sequenced events the latest supplied cwd/name wins; raw metadata is best effort. Event observations are not heartbeats:
`liveness` is always `unknown`, except an explicit `ended` observation. No PID
inspection is performed, so PID reuse cannot create a false liveness claim.

## Query

Connect to `query.sock`, send one newline-terminated JSON object and read one
newline-terminated response. One request per connection; API version is required.

```json
{"version":1,"revision":"optional previous revision","provider":"optional filter","session_id":"optional detail ID"}
```

A snapshot returns `version`, `revision`, `complete: true`, `roots`, and `unclassified` (empty lists
may be omitted). A detail query requires provider and returns `session`; missing
sessions return `error_code: not_found`. Unsupported versions use `unsupported_version`. Session records expose self/aggregate state, ancestry,
running descendant count, subtree unresolved request count, own unresolved request
IDs, last observed event time, ordering quality and liveness.
`attention_unknown` is the session's uncorrelated wait marker; subtree
`unresolved_count_exact: false` means the numeric count covers known IDs only.
Anonymous attention keeps aggregate waiting until verified clear/end/new generation. Revision and snapshot
are captured together. No collection, filesystem scan or transcript access is
triggered by a query.

A matching revision returns only version, revision and `unchanged: true`. The validator includes a hash of query shape/filter, so cross-scope reuse returns
a full response. Cache revisions separately for each query shape/filter. Every daemon start creates a new
random epoch, so a client must resync even if event count has not changed. The
revision can advance for an accepted event that does not change visible state.

The collector scans spool every 100 ms, up to 128 files per batch. Queries read the
last published snapshot while a batch is processed. Publication happens only after
successful DB commit. Connections have a two-second deadline and a 32-handler cap;
a busy server closes excess connections. Clients should back off on failure and
avoid concurrent polls. Responses over 4 MiB return `response_too_large`, never a
partially successful list. Narrow provider filters or use session detail. Pagination
is future work. Clients must keep their old snapshot on any error.

## Durability, limits and diagnostics

The daemon uses SQLite WAL and synchronous FULL. Ingest fsyncs the event before
atomic rename and syncs the spool directory. A crash after commit but before file
removal causes harmless replay. Startup scans queued files and replays persisted
normalized events to reconstruct state. No raw hook payloads are stored.

The spool and quarantine each allow at most 4096 files; each input is at most
64 KiB. Invalid JSON and ID/sequence conflicts go to quarantine with payload-free
stderr diagnostics. A database write failure leaves the event queued. Disk-full,
permission, capacity and lock failures are reported by nonzero exit status.
Ingest waits at most one second for stdin and one second for the short spool lock.
A two-second process-level watchdog also covers ingest/hook filesystem calls.
A deadline may expire after a rename already succeeded, so retry normalized events
with the same event ID. An outer provider timeout remains advisable for OS-level
process scheduling/I/O stalls; choose a nonblocking/async mode if supported.
Mimori prints no hook decision or success payload to stdout.

At 100,000 unique events the collector refuses new events, preserving queued data.
This PoC keeps deduplication tombstones and ended sessions until an operator stops
the daemon and archives the entire state directory. Automatic compaction/retention
is deliberately deferred: deleting a tombstone without a replay horizon can
resurrect old activity. Crash-left `.pending-*` files are not events and may be
removed only while all ingest processes are stopped. They count toward capacity.
Inspect diagnostics and quarantine locally; do not upload contents wholesale.

Only one user should own a state directory. Directories must already be private
or be newly created; mimori does not silently repair broad permissions. Symlink
spool entries and lock files are not followed. Protect parent directories and use
a local filesystem. NFS, Windows and hostile processes with the same UID are not
supported security boundaries. DB schema version other than v1 is refused.


## On-demand lifecycle

`mimori ensure [--state-dir /absolute/private/path] [--timeout 5s]` explicitly
ensures readiness. Success is exit 0 and `{"version":1,"ready":true}`. Errors are
exit 1 with stderr diagnostics. The timeout must be positive and at most one minute;
it bounds startup waiting, lock contention, and readiness. A timed-out caller must
not conclude the daemon was killed: startup can still complete independently.

The daemon is a new process session, with cwd `/`, stdin/stdout/stderr detached to
`/dev/null`, and no service-manager dependency. Releasing clients never stops it.
Repeated/concurrent calls preserve an existing ready daemon and revision epoch.
An inherited startup lock closes the caller-death race; the existing daemon lock
still enforces a single writer. Locks release on process exit. Stale Unix sockets
are removed only under the daemon lock after confirming no listening endpoint.
Live, incompatible, unresponsive, or inaccessible endpoints fail without replacement.
Regular files/symlinks at the socket path are never removed. No database deletion,
hook installation, permission grants, or agent process control occurs.

Readiness uses a v1 query with `"health":true`; its complete response contains only
version and revision and does not enumerate sessions. This is transport readiness,
not agent liveness, and clients must never cache it as an empty root snapshot.
Normal query behavior and durable offline ingest are unchanged.


A response may additionally contain `latest_collector_diagnostic` with `message`
(at most 4096 UTF-8 bytes) and `observed_at` (UTC RFC3339). The daemon retains only
the latest collector error in memory, including malformed/quarantined events and
database-write failures. It is exposed through the existing private query socket,
without a new file/log or query-triggered collection. Startup errors still go to
`ensure` stderr. The optional field is included on unchanged responses without
advancing the session revision: a client can inspect it independently of cached
agent rows. It records historical evidence, not current transport/agent health;
a later successful collection does not clear it. Restart clears this in-memory
record. Raw event payloads are never included. This is bounded inspection, not a
persistent diagnostic history.
