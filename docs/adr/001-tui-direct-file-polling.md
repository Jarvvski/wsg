# ADR-001: TUI uses direct file polling, not a daemon

**Status:** Accepted
**Date:** 2026-05-22

## Context

wsg is gaining an inline TUI (bare `wsg` command) that displays live worker status with navigation and actions. The TUI needs to reflect worker state changes and tail log output in near-real-time.

Two architectures were considered:

1. **Direct file polling** - the TUI process reads worker JSON state files and tails log files directly on a 2-second interval.
2. **Daemon + client** - a long-running `wsg-d` process watches files and pushes state to connected TUI clients over a Unix socket.

## Decision

Direct file polling. No daemon.

## Rationale

- Worker state files (`.jj/pool/worker-*.json`) are already the shared state store - small (< 1KB), atomically written via temp+rename, and readable by any process including `jj-wsx` (Bun TUI).
- Polling ~10 files every 2 seconds is effectively free.
- A daemon adds process lifecycle management, socket protocol design, two-binary coordination, and failure modes (daemon crash while TUI is connected, stale sockets) - all for marginal latency improvement.
- The file-based model means a daemon can be layered in later without changing the worker/dispatch side. The daemon would just become another reader of the same files.

## Consequences

- TUI has a ~2-second update granularity for worker status changes. Acceptable for this use case.
- Log tailing uses seek-to-end + poll rather than push. Slightly higher latency than inotify but simpler and portable.
- Multiple TUI instances can run simultaneously (both read files, no coordination needed).
- If we later need push updates, persistent aggregated state, or cross-session survival, we revisit this decision and extract a daemon layer.
