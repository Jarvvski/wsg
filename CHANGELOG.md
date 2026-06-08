# Changelog

User-visible changes to wsg. Each entry describes what a user (or agent) of the CLI / TUI / `jj-wsx` would notice. Pure internal refactors don't appear here; use `jj log` for the full history.

Semver: PATCH for fixes, MINOR for everything else. MAJOR (1.0.0+) is locked off until the owner explicitly approves it - never auto-bump. The current wire version is in `cmd/wsg/version.go` and printed by `wsg version`. Sections are newest first.

## 0.3.2 - 2026-06-08

### Fixed

- **Dispatch failures before the agent launches now release the worker back to idle.** When `wsg dispatch <TICKET>` failed early - because `jj new`, `jj config get user.email`, or `jj config get user.name` returned an error - the worker had already been claimed (status `busy`) but no claude process was started, leaving the slot stuck until the user ran `wsg pool reset`. The pre-launch setup is now folded into `WorkerHandle.Dispatch`, and any failure inside it resets the worker to `idle` before returning the error, so the pool stays usable on transient jj hiccups.

## 0.3.1 - 2026-06-08

### Fixed

- **TUI now reflects a dispatch within milliseconds instead of after the Linear sub-issue fetch.** `[n]` in the TUI spawned `wsg __orchestrate`, which ran a Claude+Linear MCP round-trip (`BuildDispatchGroup`) before reserving a worker - so the row stayed idle for several seconds even though work had begun. The orchestrator now reserves the worker (and auto-grows the pool if needed) up front and only then fetches the sub-issue graph; if sub-issues turn out to exist the placeholder is released so the orchestrated flow can claim per-sub-issue slots cleanly. For the common leaf-ticket case the worker shows busy on the very next 2 s tick.

## 0.3.0 - 2026-06-08

### Changed

- **TUI keybinding legend is now color-coded.** The footer hints (`[n]ew  [N]all  [f]ollow  [s]end  [r]eview  [g]rebase  [o]pen PR  [d]ismiss  [K]ill  [q]uit`) used to render in plain text, leaving the destructive `[K]ill` indistinguishable from the rest. Each chunk is now painted to mirror the status column: yellow for actions that make a worker busy (`n N s r`), green for actions on a finished worker (`g o d`), red for `[K]ill`, dim for passive nav (`f q`).

## 0.2.1 - 2026-06-05

### Fixed

- **Transient Linear MCP failures no longer kill a batch.** `wsg dispatch <parent>` builds its sub-issue graph through one Linear MCP round-trip; a network blip or an unparseable JSON response used to abort the whole dispatch. The call now retries once with a short backoff, and the parsed response is validated to drop malformed rows (parent reappearing as its own child, empty title or status, self-references in `blocked_by`) with a one-line log per drop, so a partly-broken response still moves the rest of the work forward.

## 0.2.0 - 2026-06-05

### Changed

- **`wsg send` and `wsg review` now report whether the session was actually resumed.** Previously, if the worker's log was missing, truncated, or hadn't flushed its `session_id` yet, the command silently started a context-cold fresh session. It now prints `Resumed session <id>` on success or `Starting fresh session (<reason>)` when the resume couldn't be honoured, so a cold restart is visible instead of paid-for-quietly. The TUI's status line shows the same `(resumed)` / `(fresh: <reason>)` suffix.

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
