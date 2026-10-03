# Verification and next steps

Measured 2026-10-03, macOS arm64 / Apple M1 Pro, local filesystem, host Go 1.25.9.
Nix locks nixpkgs c59305bab2065cfecc4944690d9eedbb56f3a9fa and uses Go 1.26.8.

## Executed

- `go test -race ./...`: passed. Includes 50 children + grandchild aggregation,
  separate roots at one cwd, independent waits, resolution-before-open, ordered
  transitions, late parents, cycles, generation reuse, unknown child state,
  private permissions, spool capacity, concurrent ingestion, replay/conflicts,
  100 authoritative-event permutations, scope-bound validators, response limits,
  anonymous attention, actual provider-contract fixtures, stdin/lock deadlines,
  quarantine/symlinks, socket queries, singleton writer and restart epochs.
- `go vet ./...`: passed.
- `nix build .#default --no-link`: passed, including package tests.
- `nix flake check`: validates package/check and development shell on this host;
  Linux architectures are declared but have not been executed locally.
- `python3 scripts/smoke.py bin/mimori`: passed using separate CLI/daemon processes,
  offline ingest, 52 sessions, two waits, one resolution, duplicate delivery,
  SIGKILL and restart, and actual socket/CLI polling.

Latest smoke measurement (one local run, not a production service-level objective):

| Measurement | Result |
| --- | --- |
| Direct socket unchanged query, 200 samples | median 0.037 ms; p95 0.061 ms |
| CLI unchanged query, 30 samples | median 8.815 ms |
| Unchanged response | 97 bytes |
| One root aggregate response (52 observed sessions) | 472 bytes |
| First daemon lifetime CPU, user + system | 0.032297 s |
| In-memory unchanged branch, 1,000-session snapshot | 687.1 ns/op, 168 B/op, 7 allocations (scope hash included) |

CPU covers startup, ingestion/reducer updates and these queries, measured with
wait4 for the first daemon only. It is not a sustained idle CPU percentage.
The Go microbenchmark excludes transport and JSON. Real socket measurements and
CLI launch costs are more useful for clients. A 1–2 second visible-client polling
interval is a reasonable initial experiment at this scale, not a verified target
for large repositories. Keep one outstanding request, pause hidden clients and
back off on errors. Collection publishes on a 100 ms interval.

macOS sandbox initially denied Unix socket bind; integration/race/smoke tests were
then run outside that sandbox with isolated `/tmp/mm-*` state. No user's active
provider hooks, agent sessions, client configuration or launch services were used.
The ingest lock budget was adjusted to one second after the concurrent fsync test
exposed contention at the earlier 250 ms budget.

## Explicit limits / remaining acceptance work

- Raw Claude/Codex adapters now use verified public schema contracts and anonymous
  fixtures, not live hook capture. Claude main/subagent identity and identified
  elicitations are covered. Codex aliases, anonymous permission resolution and raw
  ordering still lack source information; see providers.md and p0-status.md.
- No PID/process-start identity probe, heartbeat, transcript collector, automatic
  generation discovery, or process-death inference. Liveness remains unknown.
- No automatic retention/compaction, ended-session hiding, identity alias merge,
  paging, migrations, service installation, komado adapter or old-hook migration.
- Reducer replay is linear in event history per changed batch and ancestry walking
  depends on depth. Sequence conflict detection is linear on insert. The 100,000
  event limit bounds history; this is not a large-scale collector.
- Disk-full handling is tested via ENOSPC injection at spool create/write/sync and
  an actual SQLite max_page_count failure on a disposable DB; queued data survives
  and replays after capacity recovery. Commit-before-ack replay, file permission
  failure and lock/stdin deadlines are tested. Physical power loss and a completely
  full OS filesystem are not simulated. No user data was deleted or disk filled.
- No custom remote test CI workflow is enabled. GitHub's automatic Dependency Graph
  run succeeded for the first main commit; that is not a test suite. This private PoC was validated locally without
  authorizing hosted runner usage. Local test and Nix commands are reproducible.
- Initial target is macOS; Linux build/runtime and real providers are next validation
  targets. Repository remains private and the license is undecided.

The existing komado display problem is not marked fixed: this delivers the
independent backend PoC, with client adoption and live-provider fidelity still open.
