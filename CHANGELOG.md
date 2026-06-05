# Changelog

User-visible changes to wsg. Each entry describes what a user (or agent) of the CLI / TUI / `jj-wsx` would notice. Pure internal refactors don't appear here; use `jj log` for the full history.

Semver: PATCH for fixes, MINOR for everything else. MAJOR (1.0.0+) is locked off until the owner explicitly approves it - never auto-bump. The current wire version is in `cmd/wsg/version.go` and printed by `wsg version`. Sections are newest first.

## 0.1.1 - 2026-06-05

### Fixed

- **Dispatch resize prompts no longer lie about the slot count.** `wsg dispatch <A> <B> ...` used to read the pool size via a lockless snapshot, then prompt the user to resize, then claim - and a concurrent process could grab the slots in between. The resize-and-claim now run atomically under the pool lock, so the workers you confirm are the workers you get (or you see a fresh "Resize?" prompt with the real gap).

## 0.1.0 - 2026-06-05

First tagged version. Backfills the user-visible deltas from the architecture-deepening pass landed today (5 candidates: Reclaim, LoadLiveWorker, Launch unification, DispatchGroup methodisation, Pool aggregate) plus the `wsg version` command itself.

### Added

- **`wsg version`.** Prints `wsg <semver>` to stdout. Also responds to `wsg --version`. The current version is the constant `Version` in `cmd/wsg/version.go`.

### Fixed

- **Pool resize no longer races against dispatch.** A concurrent `wsg pool resize` (shrink) and `wsg dispatch` could previously tear down a workspace just as a claude agent was launching in it. Both paths now serialise through the pool mutation lock - either the shrink wins (`cannot shrink: N worker(s) busy`) or the claim wins (`Pool shrunk from X to Y`), never both. (`047337`)
- **No more stuck `busy` workers.** Every read path (`wsg status`, `wsg pool list`, TUI list, orchestrator polling) reconciles dead PIDs - a worker whose claude process died is immediately moved to `done` or `failed` with the exit code from its log, rather than appearing busy forever. (`7d93f5`)
- **`wsg pool reset` reliably kills the running agent.** The kill step was inconsistent across reset, TUI `[K]ill`, and orchestrator cleanup; all three now share `WorkerHandle.Reclaim` and terminate the live PID before resetting state.
- **Dispatch and resume use the same launch path.** Same fg/bg handling, same PID recording, same finalisation on exit, so `wsg dispatch <X>` and `wsg send <worker> "..."` no longer drift on edge cases (process crashes, foreground termination, etc.). (`7a1190`)

### Changed

- **Orchestrated dispatch is more testable.** DAG operations (readiness, wave sizing, sync-from-workers) moved from free functions onto a `DispatchGroup` aggregate. No user-visible behavior change. (`8dac01`)
- **Send works on idle workers.** `wsg send <worker> "..."` no longer requires the worker to have an existing session - sending to an idle worker triggers a new workload. The TUI's `s` key no longer gates on session existence.
