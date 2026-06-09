package main

import (
	"path/filepath"
)

// launch spawns claude in the worker's workspace using inv. The handle must
// already be in busy state. In foreground mode the process runs to completion
// and finalises the handle; the returned pid is 0. In background mode the pid
// is returned and the handle is finalised asynchronously when the process
// exits.
func (h *WorkerHandle) launch(inv claudeInvocation, fg bool) (int, error) {
	wspath := h.repo.workerDir(h.worker)
	logFile := filepath.Join(h.repo.poolDir(), h.worker+".log")
	argv := append([]string{"claude"}, inv.Args()...)
	if fg {
		h.runFG(wspath, logFile, argv)
		return 0, nil
	}
	return h.runBG(wspath, logFile, argv)
}

func (h *WorkerHandle) runFG(wspath, logFile string, claudeArgs []string) {
	if _, err := startForeground(wspath, logFile, claudeArgs[0], claudeArgs[1:]...); err != nil {
		_ = h.withWorkerLock(func() error {
			h.state.MarkFailed(1, err.Error())
			return h.save()
		})
		return
	}
	h.WaitFinal()
}

func (h *WorkerHandle) runBG(wspath, logFile string, claudeArgs []string) (int, error) {
	pid, err := startBackground(wspath, logFile, claudeArgs[0], claudeArgs[1:]...)
	if err != nil {
		_ = h.withWorkerLock(func() error {
			h.state.MarkFailed(1, err.Error())
			return h.save()
		})
		return 0, err
	}
	h.state.SetPID(pid)
	h.save()

	// Re-open from disk in the supervisor so it sees the saved PID and
	// doesn't share a state pointer with the main goroutine.
	repo, worker := h.repo, h.worker
	go func() {
		h2, err := loadWorker(repo, worker)
		if err != nil {
			return
		}
		h2.WaitFinal()
	}()

	return pid, nil
}

// checkLiveness reconciles a busy handle against the running world: if the
// recorded PID is no longer alive the handle is finalised from the log
// under the worker's flock, so a concurrent supervisor goroutine that is
// also about to finalise can't double-write the state file.
func (h *WorkerHandle) checkLiveness() {
	ws := h.state
	if ws.Status != WorkerStatusBusy || ws.PID == nil {
		return
	}
	if processAlive(*ws.PID) {
		return
	}
	h.finalizeUnderLock()
}

// finalizeUnderLock re-reads worker state from disk under the worker
// flock and, if still busy, finalises from the log. Used by every path
// that may write a terminal state - WaitFinal after a process exit and
// checkLiveness on a stale dead-PID read - so the two cannot race.
func (h *WorkerHandle) finalizeUnderLock() {
	_ = h.withWorkerLock(func() error {
		ws, err := loadWorkerState(h.path)
		if err != nil {
			return nil
		}
		h.state = ws
		if h.state.Status != WorkerStatusBusy {
			return nil
		}
		h.finalizeFromLog()
		return nil
	})
}

// WaitFinal is the single entry point for transitioning a busy worker to
// its terminal state after its process exits. If a PID is recorded it
// waits for that process; then re-reads state under the worker flock and,
// if still busy, finalises from the log. Idempotent: a concurrent
// checkLiveness that won the lock will have already finalised, and the
// re-read observes that and returns without a second write.
//
// runFG calls WaitFinal inline once startForeground returns; runBG hands
// the handle to a goroutine that calls it once the background pid exits.
func (h *WorkerHandle) WaitFinal() {
	if h.state.PID != nil {
		waitForProcess(*h.state.PID)
	}
	h.finalizeUnderLock()
}

// finalizeFromLog transitions a busy worker to its terminal state from the
// agent's stream-json log: done (with the logged exit code and resolved branch)
// on a success result, failed otherwise - including a run the CLI reports as
// is_error even though the process itself exits 0. A missing or unparseable
// result means the process died without reporting, also a failure.
func (h *WorkerHandle) finalizeFromLog() {
	ws := h.state
	if ws.LogFile != nil {
		if result := readLogResult(*ws.LogFile); result != nil {
			if result.Status == WorkerStatusDone {
				ec := 0
				if result.ExitCode != nil {
					ec = *result.ExitCode
				}
				ws.MarkDone(ec)
				h.resolveBranchInMemory()
				h.save()
			} else {
				ec := 1
				if result.ExitCode != nil {
					ec = *result.ExitCode
				}
				errMsg := ""
				if result.Error != nil {
					errMsg = *result.Error
				}
				ws.MarkFailed(ec, errMsg)
				h.save()
			}
			return
		}
	}
	ws.MarkFailed(1, "Process exited unexpectedly")
	h.save()
}
