# P0 reconciliation — 2026-10-03

The original project separates five P0 tasks from Neovim/komado integration and
service deployment/retention work marked P1. The following distinguishes working
core behavior, information limits and decisions. A successful normalized demo does
not imply perfect live raw-provider fidelity.

## P0 tasks

| Requirement | Evidence / status | Remaining decision or boundary |
| --- | --- | --- |
| Repository and Go/Nix baseline | Private ttak0422/mimori; pinned flake; local build/check | License deliberately unspecified; owner may select a redistribution license later |
| Target versions, payloads, anonymous fixtures, identity and transitions | Local versions identified; Claude official docs and Codex 0.159.2 immutable generated schemas; testdata/hooks.json; contract.md | Actual installed-runtime capture not performed; no hook installation authorized |
| Ingest → spool → daemon → SQLite → CLI | Separate-process smoke, offline receipt, crash/replay, lock and private permissions | No blocker for this local path |
| Reducer and both initial providers | 50 children + grandchild, multiple requests, permutations, late identity, cycles, generation-local terminal state; adapters for both | Exact raw anonymous permission resolution and Codex identity aliases cannot be inferred from verified input; explicit normalized producer/additional source needed |
| Query/revision/performance | Scoped validators, restart epoch, multiple clients, NotFound/version/size errors, socket/CLI/CPU/byte measurements | Large-scale optimization and pagination not required to prove the initial bounded path |

No PID is used to infer liveness. The specification conditions PID/start-time
matching on using PID; this implementation always reports liveness unknown unless
explicit end was observed. Generation-reuse tests ensure an old incarnation cannot
mutate a new one. They are not claimed as OS PID-probe tests.

## Nine acceptance criteria

1. **Verified:** one root with 50 children and a grandchild; individual detail query.
2. **Verified:** separate roots at the same cwd; unknown/cyclic identities preserved.
3. **Verified with correlatable IDs:** independent child requests; one answer does
   not clear the other. Raw anonymous waits stay visibly uncertain, never falsely
   exact. Claude identified elicitation fixtures also cover the raw-adapter route.
4. **Verified normalized lifecycle:** Stop permits another turn, explicit end is
   absorbing within its generation. Raw hooks need an external incarnation value
   after process/session restart and remain best effort for delayed turns.
5. **Verified for the authoritative contract:** duplicate/reordered/concurrent
   receipt, 100 event permutations, commit-before-ack replay and late parents.
   Exactly ordering raw hooks without provider sequence is an information gap.
6. **Verified:** independent clients, daemon stop/restart, changed epoch and full
   resync. Process smoke includes SIGKILL.
7. **Verified:** unchanged query reads cached state and scope hash only; measured
   transport/CLI time, daemon CPU and bytes, documented before recommending a
   tentative 1–2 second visible-client interval.
8. **Verified fault paths:** malformed input, capacity, private permissions,
   write/sync ENOSPC injection, SQLite disk-full, replay after failure, bounded
   stdin/lock wait and process-level ingest deadline. No full user disk or real
   data deletion was used. Physical power loss remains outside these tests.
9. **Backend independence verified; actual komado exercise is P1:** CLI/socket
   clients do not own the collector; multiple clients can exit independently.
   Running two real komado instances awaits the explicitly later adapter task.

## What remains required for stronger live-provider claims

These are information/access boundaries, not more synthetic-test coverage:

- **Permission correlation:** both verified ordinary PermissionRequest contracts
  lack an ID to match later completion. A producer with request identity or a
  verified extra provider event source is required. Current behavior is waiting
  with `unresolved_count_exact: false`; no guessed clearing is acceptable.
- **Codex aliases/parentage:** schemas expose strings but do not prove how an
  independently reported session maps to a parent-owned agent. A stable provider
  identity contract or narrowly scoped runtime metadata verification is needed.
- **Raw incarnation and ordering:** raw events carry no authoritative generation
  or global sequence. A trusted launcher/producer must supply these, or a future
  provider source must establish them. Receipt timestamps are not substitutes.

The available authorized work establishes normalized consistency and honest raw
observation. It does not authorize enabling an ongoing API subscription, reading
private transcripts or installing real hooks to bridge these gaps. The next
integration decision is which explicit metadata source/producer to support and
whether isolated real-provider capture is desired. No account, credential or
paid run has been requested.

## Explicitly later scope

The project P1 list places the generic Neovim Lua client and thin komado adapter,
service startup settings, retention/cleanup, old-hook migration and rollback later.
The new Neovim design note is conditional on a completed backend and does not
silently move that client work into P0. Automatic retention and persistent service
installation therefore remain deferred; only owned disposable test data was cleaned.
Public release/license selection, hosted test CI and Linux runtime certification
also remain unclaimed. No blocker prevents using the tested local normalized PoC.
