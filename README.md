# mimori

A local, editor-independent coding-agent state service. This PoC collects small
state events, persists them through a disk spool into SQLite, and serves cached
root summaries and session details over a private Unix socket. No Neovim,
terminal multiplexer, transcript reader, cloud service, or agent launcher is needed.

## Try it

Requires macOS or Linux and Nix with flakes enabled. Install `mimori` into your
environment, for example with a Nix profile:

```sh
nix profile install github:ttak0422/mimori
```

The commands below assume `mimori` is available on `PATH`. For an interactive demo,
run from this repository's root in both terminals. In terminal 1:

```sh
mimori ingest < examples/root.json
mimori daemon
```

In terminal 2:

```sh
mimori ingest < examples/child-waiting.json
mimori query
mimori query --provider demo --session child
mimori ingest < examples/child-resolved.json
mimori query
# Reuse a revision only with the same query/filter:
mimori query --revision '<revision from previous response>'
```

Commands default to `$XDG_STATE_HOME/mimori` or `~/.local/state/mimori`.
Every command accepts `--state-dir /absolute/private/path`. Keep this path short:
Unix socket path limits apply. `daemon` stays in the foreground; Ctrl-C stops it.
For editors or other on-demand clients, run `mimori ensure` before querying. It
starts one independent daemon or reuses the ready daemon, with a 5-second readiness
deadline (`--timeout 10s` overrides it, at most 1 minute). Success prints
`{"version":1,"ready":true}`; failure exits nonzero with a diagnostic on stderr.
Concurrent callers share the same daemon per state directory. The daemon survives
caller cancellation and editor/terminal exit. It has no idle shutdown and installs
no login service. A query never starts it. For foreground diagnostics, stop your
known daemon and run `mimori daemon --state-dir ...` yourself; detached runtime
stdio goes to `/dev/null`, while startup errors are reported to `ensure`.
`mimori query` includes `latest_collector_diagnostic` after a collection error,
even for unchanged snapshots. It retains only the latest bounded message and
observation time until daemon restart; it is historical, not a current health verdict.
Ingest also works while it is stopped. A query failure exits nonzero and must be
shown as stale/unavailable by clients, not interpreted as a live status.

## Development

Nix provides the reproducible build and development tools. `nix develop` supplies
Go, gopls and Python; it does not install the `mimori` command.

```sh
nix develop
nix build
nix flake check
# Alternatively, build and smoke-test with Go >= 1.25:
go build -o bin/mimori ./cmd/mimori
python3 scripts/smoke.py bin/mimori
```

The smoke test creates an isolated temporary directory, queues a root, 50 children
and a grandchild while the daemon is stopped, checks two independent permission
requests, kills/restarts the daemon, measures polling, and cleans up its processes.

## What the PoC establishes

- Atomic, fsynced event files; SQLite commit before removal; replay deduplication.
- A single daemon writer enforced with an OS file lock, released on crash.
- Provider + session + generation identity. Equal working directories never merge.
- Explicit roots, late parents, unresolved identities and cycle diagnostics.
- Per-request waiting sets, absorbing request resolution and generation-local end.
- Turn completion (`idle`) stays distinct from process end (`ended`).
- Cached v1 JSON snapshots, restart epochs and tiny unchanged responses.
- Strict privacy allowlist for Claude/Codex hook adapters; no transcript access.
- Private directories (0700), files/socket (0600), bounded input and spool.

See [the contract](docs/contract.md), [hook compatibility](docs/providers.md), and
[verification and limits](docs/verification.md). This is an intentionally bounded
PoC. It does not install hooks, alter existing clients, launch at login, or control
agents. No license has been selected; do not assume permission for redistribution.
