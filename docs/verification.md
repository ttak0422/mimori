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

Socket/process tests require an environment that permits Unix socket binding.
All fixtures use disposable state directories. The ingest lock budget is one
second to accommodate concurrent durable writes.

## Explicit limits / remaining acceptance work

- Raw Claude/Codex adapters now use verified public schema contracts and anonymous
  fixtures, not live hook capture. Claude main/subagent identity and identified
  elicitations are covered. Codex aliases, anonymous permission resolution and raw
  ordering still lack source information; see providers.md and p0-status.md.
- No PID/process-start identity probe, heartbeat, transcript collector, automatic
  generation discovery, or process-death inference. Liveness remains unknown.
- No automatic retention/compaction, ended-session hiding, identity alias merge,
  paging, migrations, or service installation. Editor adapters are separate clients.
- Reducer replay is linear in event history per changed batch and ancestry walking
  depends on depth. Sequence conflict detection is linear on insert. The 100,000
  event limit bounds history; this is not a large-scale collector.
- Disk-full handling is tested via ENOSPC injection at spool create/write/sync and
  an actual SQLite max_page_count failure on a disposable DB; queued data survives
  and replays after capacity recovery. Commit-before-ack replay, file permission
  failure and lock/stdin deadlines are tested. Physical power loss and a completely
  full OS filesystem are not simulated.
- No custom remote test CI workflow is configured. The Go and Nix checks above
  are reproducible locally; dependency graph checks are not a test suite.
- Linux build/runtime and live provider sessions remain validation targets.

## On-demand lifecycle validation (2026-10-03 UTC)

The macOS lifecycle suite exercises eight simultaneous ensure processes, reuse
without epoch change, SIGKILL/stale-socket restart, loss of the initiating process
group before readiness, and timeout without killing the child. It also verifies
read-only query behavior, live incompatible endpoint preservation (including the
foreground daemon path), private-directory checks, regular-file preservation,
and startup storage-error delivery. A malformed event injected after detached
readiness verifies that the latest bounded collector diagnostic remains queryable
on unchanged replies, excludes the raw fixture payload, and accompanies quarantine.
Fixtures and daemon processes are isolated and cleaned up by the test helper.

`go test -race ./...`, `go vet ./...`, and
`nix flake check --no-update-lock-file --no-write-lock-file` passed on macOS.
Linux runtime behavior has not been tested.

The 52-session smoke fixture passed crash recovery and durable offline ingest.
On this run, 30 CLI query samples measured median 8.75 ms / p95 10.03 ms; 30
healthy `ensure` reuse samples measured median 8.67 ms / p95 9.61 ms. The 200 raw
socket samples measured median 0.055 ms / p95 0.093 ms. Unchanged responses were
97 bytes and the one-root snapshot was 472 bytes. These are local warm-process
measurements, not latency guarantees. The 5-second readiness default allows
substantial cold startup margin and remains configurable up to one minute;
large retained stores can still exceed it and return a bounded error while the
independent daemon continues startup. The latest collector diagnostic is retained in bounded memory and exposed by
query, including unchanged replies; foreground `daemon` also prints diagnostics.
