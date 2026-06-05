package main

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

type PoolConfig struct {
	Size       int      `json:"size"`
	GHRepo     string   `json:"gh_repo"`
	Workers    []string `json:"workers"`
	CreatedAt  string   `json:"created_at"`
	Foreground *bool    `json:"foreground,omitempty"`
}

type WorkerState struct {
	Status      WorkerStatus `json:"status"`
	Ticket      *string      `json:"ticket"`
	PID         *int         `json:"pid"`
	StartedAt   *string      `json:"started_at"`
	CompletedAt *string      `json:"completed_at"`
	LogFile     *string      `json:"log_file"`
	BranchName  *string      `json:"branch_name"`
	ExitCode    *int         `json:"exit_code"`
	Error       *string      `json:"error"`
}

func resolveForeground(r *RepoContext, flag *bool) bool {
	if flag != nil {
		return *flag
	}
	if cfg, err := loadPoolConfig(r.poolConfigFile()); err == nil && cfg.Foreground != nil {
		return *cfg.Foreground
	}
	return false
}

func newIdleWorkerState() *WorkerState {
	return &WorkerState{Status: WorkerStatusIdle}
}

func (ws *WorkerState) MarkDispatched(ticket, logFile, branchName string) {
	now := nowUTC()
	ws.Status = WorkerStatusBusy
	ws.Ticket = &ticket
	ws.StartedAt = &now
	ws.LogFile = &logFile
	ws.BranchName = &branchName
	ws.CompletedAt = nil
	ws.ExitCode = nil
	ws.Error = nil
	ws.PID = nil
}

func (ws *WorkerState) MarkDone(exitCode int) {
	now := nowUTC()
	ws.Status = WorkerStatusDone
	ws.CompletedAt = &now
	ws.ExitCode = &exitCode
}

func (ws *WorkerState) MarkFailed(exitCode int, errMsg string) {
	now := nowUTC()
	ws.Status = WorkerStatusFailed
	ws.CompletedAt = &now
	ws.ExitCode = &exitCode
	ws.Error = &errMsg
}

func (ws *WorkerState) SetPID(pid int) {
	ws.PID = &pid
}

func (ws *WorkerState) Reset() {
	*ws = WorkerState{Status: WorkerStatusIdle}
}

func (ws *WorkerState) MarkResumed(logFile string) {
	now := nowUTC()
	ws.Status = WorkerStatusBusy
	ws.StartedAt = &now
	ws.LogFile = &logFile
	ws.CompletedAt = nil
	ws.ExitCode = nil
	ws.Error = nil
}

// ── WorkerHandle ──────────────────────────────────────────────────

// WorkerHandle is the operational view of one worker slot. Consumer code
// drives it via four high-level verbs - Dispatch, Resume, Reclaim, Status -
// and treats the underlying WorkerState as a read-only snapshot. The state-
// mutation primitives (MarkDispatched/Done/Failed/Resumed and the Run/Launch
// process plumbing) are package-internal; they are still defined on
// WorkerState for tests and Pool.Claim's atomic mark, but are not part of
// the surface a caller is expected to compose against.
//
// A handle is wired up with repo + worker when constructed via LoadLiveWorker
// or CreateIdleWorker; both fields are needed for the lifecycle verbs.
// OpenWorker(path) loads a raw state by path for tests / low-level reads -
// the lifecycle verbs are not available in that form.
type WorkerHandle struct {
	path   string
	repo   *RepoContext
	worker string
	state  *WorkerState
}

// OpenWorker loads worker state from an explicit path. The returned handle
// is not wired with repo+worker, so the lifecycle verbs (Dispatch/Resume/
// Reclaim) cannot be used. For the standard "open worker N from repo R"
// path use LoadLiveWorker, which also reconciles dead-busy state.
func OpenWorker(path string) (*WorkerHandle, error) {
	ws, err := loadWorkerState(path)
	if err != nil {
		return nil, err
	}
	return &WorkerHandle{path: path, state: ws}, nil
}

// CreateIdleWorker writes a fresh idle state for worker under repo r and
// returns a fully-wired handle. Used by Pool.grow when a new worker slot
// is provisioned and by the TUI when a worker file disappears.
func CreateIdleWorker(r *RepoContext, worker string) (*WorkerHandle, error) {
	h := &WorkerHandle{
		path:   r.workerStateFile(worker),
		repo:   r,
		worker: worker,
		state:  newIdleWorkerState(),
	}
	if err := h.save(); err != nil {
		return nil, err
	}
	return h, nil
}

// loadWorker reads worker N from repo R and returns a wired handle without
// running liveness reconciliation. Use it on mutation-only paths where a
// fresh log re-read would be wasted; otherwise prefer LoadLiveWorker.
func loadWorker(r *RepoContext, worker string) (*WorkerHandle, error) {
	path := r.workerStateFile(worker)
	ws, err := loadWorkerState(path)
	if err != nil {
		return nil, err
	}
	return &WorkerHandle{path: path, repo: r, worker: worker, state: ws}, nil
}

// Status returns a read-only snapshot of the worker's current state. The
// returned pointer aliases the handle's internal state and reflects any
// later mutation through this handle; callers should not mutate it.
func (h *WorkerHandle) Status() *WorkerState {
	return h.state
}

// Dispatch launches claude in the worker's workspace. The worker must
// already be in busy state - Pool.Claim sets the ticket/log/branch fields
// atomically before returning the worker name, so by the time Dispatch is
// called the state file already reflects the run. In foreground mode the
// process runs to completion and finalises the handle; the returned pid is
// 0. In background mode the pid is returned and the supervisor goroutine
// finalises the handle asynchronously when the process exits.
func (h *WorkerHandle) Dispatch(inv claudeInvocation, fg bool) (int, error) {
	return h.launch(inv, fg)
}

// Resume marks a non-busy worker busy on a fresh log file and launches
// claude. Caller is expected to have built inv with SessionID extracted
// from the previous run's log (via extractSessionID on Status().LogFile)
// so claude resumes the same session rather than starting fresh.
func (h *WorkerHandle) Resume(inv claudeInvocation, fg bool) (int, error) {
	logFile := filepath.Join(h.repo.poolDir(), h.worker+".log")
	h.state.MarkResumed(logFile)
	if err := h.save(); err != nil {
		return 0, err
	}
	return h.launch(inv, fg)
}

// Reclaim returns the worker to idle: kills any live PID, clears state,
// and fires an async jj restore + jj new main in the workspace dir if it
// exists. The workspace restore is fire-and-forget; callers should not
// assume it has completed when Reclaim returns.
func (h *WorkerHandle) Reclaim() error {
	if h.state.PID != nil && processAlive(*h.state.PID) {
		killProcess(*h.state.PID)
	}
	if err := h.reset(); err != nil {
		return err
	}
	if h.repo == nil || h.worker == "" {
		return nil
	}
	wspath := h.repo.workerDir(h.worker)
	if fi, err := os.Stat(wspath); err == nil && fi.IsDir() {
		startBackground(wspath, os.DevNull, "sh", "-c",
			"jj restore 2>/dev/null; jj new main 2>/dev/null")
	}
	return nil
}

// reset clears state to idle without process-kill or workspace restore.
// The dismiss-terminal path uses this so a user can inspect / recover an
// unpushed working copy after a failed run.
func (h *WorkerHandle) reset() error {
	h.state.Reset()
	return h.save()
}

func (h *WorkerHandle) save() error {
	return saveWorkerState(h.path, h.state)
}

func loadPoolConfig(path string) (*PoolConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var cfg PoolConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}

func savePoolConfig(path string, cfg *PoolConfig) error {
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func loadWorkerState(path string) (*WorkerState, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var ws WorkerState
	if err := json.Unmarshal(data, &ws); err != nil {
		return nil, err
	}
	return &ws, nil
}

func saveWorkerState(path string, ws *WorkerState) error {
	data, err := json.MarshalIndent(ws, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
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

// withWorkerLock acquires an exclusive flock on a sibling ".lock" file and
// runs fn while holding it. The lock is on a sidecar - not the worker JSON
// itself - because saveWorkerState rewrites the JSON via temp+rename,
// which invalidates any fd-bound flock on the original inode.
func (h *WorkerHandle) withWorkerLock(fn func() error) error {
	lockPath := h.path + ".lock"
	lf, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0644)
	if err != nil {
		return err
	}
	defer lf.Close()
	if err := syscall.Flock(int(lf.Fd()), syscall.LOCK_EX); err != nil {
		return err
	}
	defer syscall.Flock(int(lf.Fd()), syscall.LOCK_UN)
	return fn()
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

// LoadLiveWorker opens a worker's state and reconciles it against the running
// world: if recorded as busy but the PID is no longer alive, the worker is
// finalised from its log (to done or failed) and that transition is persisted
// to disk before the handle is returned. Use this on every read-then-decide
// path (list, shrink, remove, DAG progression, TUI render, resume); use the
// raw OpenWorker for mutation-only paths where reconciliation would be a
// wasted log read.
func LoadLiveWorker(r *RepoContext, worker string) (*WorkerHandle, error) {
	h, err := loadWorker(r, worker)
	if err != nil {
		return nil, err
	}
	h.checkLiveness()
	return h, nil
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
				h.resolveBranch()
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

// resolveBranch refreshes the stored branch name from the worker's actual jj
// bookmarks - useful after a run when the agent has created the real
// `adam/<ticket>-<slug>` branch and the state file still holds the placeholder
// ticket prefix. Mutates h.state in memory only; the caller is responsible
// for persisting (finalizeFromLog folds it into its single save).
func (h *WorkerHandle) resolveBranch() {
	if h.repo == nil || h.worker == "" {
		return
	}
	ws := h.state
	if ws.BranchName == nil || *ws.BranchName == "" {
		return
	}
	if resolved := jjResolveBranchForTicket(h.repo.workerDir(h.worker), *ws.BranchName); resolved != nil {
		ws.BranchName = resolved
	}
}

// refreshBranch is the standalone form of resolveBranch: mutates and persists.
// Used by review-prompt building, where the placeholder ticket prefix may
// still be in state because finalizeFromLog couldn't reach the workspace.
func (h *WorkerHandle) refreshBranch() error {
	h.resolveBranch()
	return h.save()
}

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

// ── Pool ──────────────────────────────────────────────────────────

// Pool is the aggregate owning .jj/pool.json and the pool mutation lock.
// All state-changing operations (Claim, Resize, Remove, Destroy) serialise
// through withLock so a shrink can no longer remove a worker that a
// concurrent Claim has just marked busy. Read paths (Snapshot, Config)
// take no lock and may return a slightly stale view; the next mutation
// reloads under lock and acts on fresh state.
type Pool struct {
	repo *RepoContext
	cfg  *PoolConfig
}

// PoolSnapshot is a moment-in-time view: cfg fields plus a live count of
// each worker's status (reconciled against PIDs via LoadLiveWorker).
type PoolSnapshot struct {
	Size    int
	Workers []string
	Idle    int
	Busy    int
	Done    int
	Failed  int
}

// OpenPool reads .jj/pool.json. Returns an error if no pool exists.
func OpenPool(r *RepoContext) (*Pool, error) {
	cfg, err := loadPoolConfig(r.poolConfigFile())
	if err != nil {
		return nil, err
	}
	return &Pool{repo: r, cfg: cfg}, nil
}

// CreatePool initialises an empty pool config and ensures the pool dir
// exists. Used by `wsg pool create N` before a Resize grows it to N.
func CreatePool(r *RepoContext) (*Pool, error) {
	if err := os.MkdirAll(r.poolDir(), 0755); err != nil {
		return nil, fmt.Errorf("create pool dir: %w", err)
	}
	cfg := &PoolConfig{
		Size:      0,
		GHRepo:    ghRepo(r),
		Workers:   []string{},
		CreatedAt: nowUTC(),
	}
	if err := savePoolConfig(r.poolConfigFile(), cfg); err != nil {
		return nil, err
	}
	return &Pool{repo: r, cfg: cfg}, nil
}

// Config returns the in-memory pool config. The view is fresh as of the
// last OpenPool / successful mutation; concurrent processes may have
// changed disk state since.
func (p *Pool) Config() *PoolConfig {
	return p.cfg
}

// Snapshot reads every worker state file, reconciling dead-busy entries
// via LoadLiveWorker, and returns the live counts. Does not take the
// lock - the result reflects the moment of read.
func (p *Pool) Snapshot() *PoolSnapshot {
	snap := &PoolSnapshot{
		Size:    p.cfg.Size,
		Workers: append([]string(nil), p.cfg.Workers...),
	}
	for _, name := range p.cfg.Workers {
		h, err := LoadLiveWorker(p.repo, name)
		if err != nil {
			continue
		}
		switch h.Status().Status {
		case WorkerStatusIdle:
			snap.Idle++
		case WorkerStatusBusy:
			snap.Busy++
		case WorkerStatusDone:
			snap.Done++
		case WorkerStatusFailed:
			snap.Failed++
		}
	}
	return snap
}

// withLock acquires the pool mutation lock, reloads cfg from disk so the
// mutation sees the latest state from any concurrent process, runs fn,
// and releases. All mutating Pool operations go through this. The lock
// file (.dispatch.lock) keeps its name for compatibility with any pool
// that already has a stale lock file in place; renaming would create a
// window where holders of the old and new names both think they own it.
func (p *Pool) withLock(fn func() error) error {
	lockPath := filepath.Join(p.repo.poolDir(), ".dispatch.lock")
	lf, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0644)
	if err != nil {
		return fmt.Errorf("open pool lock: %w", err)
	}
	defer lf.Close()
	if err := syscall.Flock(int(lf.Fd()), syscall.LOCK_EX); err != nil {
		return fmt.Errorf("flock pool lock: %w", err)
	}
	defer syscall.Flock(int(lf.Fd()), syscall.LOCK_UN)

	cfg, err := loadPoolConfig(p.repo.poolConfigFile())
	if err != nil {
		return fmt.Errorf("reload pool config: %w", err)
	}
	p.cfg = cfg
	return fn()
}

// Claim atomically picks the first idle worker and marks it busy with
// ticket. Serialises against concurrent Claim, Resize, Remove.
func (p *Pool) Claim(ticket string) (string, error) {
	var picked string
	err := p.withLock(func() error {
		ticketLower := strings.ToLower(ticket)
		poolDir := p.repo.poolDir()
		for _, worker := range p.cfg.Workers {
			sf := p.repo.workerStateFile(worker)
			ws, err := loadWorkerState(sf)
			if err != nil {
				continue
			}
			if ws.Status != WorkerStatusIdle {
				continue
			}
			logFile := filepath.Join(poolDir, worker+".log")
			ws.MarkDispatched(ticket, logFile, ticketLower)
			if err := saveWorkerState(sf, ws); err != nil {
				return fmt.Errorf("save worker state: %w", err)
			}
			picked = worker
			return nil
		}
		return fmt.Errorf("no idle workers")
	})
	if err != nil {
		return "", err
	}
	return picked, nil
}

// Resize grows or shrinks the pool under the lock. Grow adds workers in
// parallel via jj workspace add; shrink removes idle/done/failed workers
// from the tail. Shrink fails (without changing state) if not enough
// workers are removable.
func (p *Pool) Resize(newSize int) error {
	return p.withLock(func() error {
		oldSize := p.cfg.Size
		if newSize == oldSize {
			info("Pool is already size %d", oldSize)
			return nil
		}
		if newSize > oldSize {
			return p.grow(newSize)
		}
		return p.shrink(newSize)
	})
}

// grow adds (newSize - oldSize) workers. Caller holds the lock.
func (p *Pool) grow(newSize int) error {
	r := p.repo
	oldSize := p.cfg.Size
	var wg sync.WaitGroup
	for i := 0; i < newSize-oldSize; i++ {
		name := generateWorkerName()
		wait, err := Provision(r, name, "", WorkerRole)
		if err != nil {
			return fmt.Errorf("create %s: %w", name, err)
		}
		p.cfg.Workers = append(p.cfg.Workers, name)
		wg.Add(1)
		go func() {
			defer wg.Done()
			wait()
		}()
		info("  Created %s", name)
	}
	wg.Wait()
	p.cfg.Size = newSize
	if err := savePoolConfig(r.poolConfigFile(), p.cfg); err != nil {
		return err
	}
	info("Pool expanded from %d to %d", oldSize, newSize)
	return nil
}

// shrink drops workers from the tail. Caller holds the lock. Only
// removes workers in a non-busy state; errors if there aren't enough.
func (p *Pool) shrink(newSize int) error {
	r := p.repo
	poolDir := r.poolDir()
	oldSize := p.cfg.Size

	nonBusy := 0
	var removable []string
	for i := len(p.cfg.Workers) - 1; i >= newSize; i-- {
		name := p.cfg.Workers[i]
		sf := filepath.Join(poolDir, name+".json")
		if _, err := os.Stat(sf); os.IsNotExist(err) {
			removable = append(removable, name)
			continue
		}
		h, err := LoadLiveWorker(r, name)
		if err != nil {
			removable = append(removable, name)
			continue
		}
		if h.Status().Status.IsRemovable() {
			removable = append(removable, name)
		} else {
			nonBusy++
		}
	}

	toRemove := oldSize - newSize
	if len(removable) < toRemove {
		minSize := oldSize - len(removable)
		return fmt.Errorf("cannot shrink to %d: %d worker(s) busy.\nMinimum safe size is %d. Use 'wsg pool list' to see status", newSize, nonBusy, minSize)
	}

	removed := make(map[string]bool)
	for _, name := range removable {
		p.tearDownWorker(name)
		removed[name] = true
		info("  Removed %s", name)
	}

	remaining := make([]string, 0, newSize)
	for _, w := range p.cfg.Workers {
		if !removed[w] {
			remaining = append(remaining, w)
		}
	}
	p.cfg.Workers = remaining
	p.cfg.Size = newSize
	if err := savePoolConfig(r.poolConfigFile(), p.cfg); err != nil {
		return err
	}
	info("Pool shrunk from %d to %d", oldSize, newSize)
	return nil
}

// Remove tears down a single non-busy worker and updates cfg. Returns
// the new pool size.
func (p *Pool) Remove(worker string) (int, error) {
	var newSize int
	err := p.withLock(func() error {
		idx := -1
		for i, w := range p.cfg.Workers {
			if w == worker {
				idx = i
				break
			}
		}
		if idx < 0 {
			return fmt.Errorf("worker %s not in pool", worker)
		}
		if h, err := LoadLiveWorker(p.repo, worker); err == nil {
			if h.Status().Status.IsActive() {
				return fmt.Errorf("worker %s is busy. Reset it first: wsg pool reset %s", worker, worker)
			}
		}
		p.tearDownWorker(worker)
		p.cfg.Workers = append(p.cfg.Workers[:idx], p.cfg.Workers[idx+1:]...)
		p.cfg.Size = len(p.cfg.Workers)
		if err := savePoolConfig(p.repo.poolConfigFile(), p.cfg); err != nil {
			return err
		}
		newSize = p.cfg.Size
		return nil
	})
	return newSize, err
}

// Destroy kills any live worker processes, tears down every worker's
// workspace + state, and removes the pool directory and config. Best-
// effort: a worker whose PID is already gone is skipped silently.
func (p *Pool) Destroy() error {
	return p.withLock(func() error {
		var wg sync.WaitGroup
		for _, worker := range p.cfg.Workers {
			wg.Add(1)
			go func(worker string) {
				defer wg.Done()
				if ws, err := loadWorkerState(p.repo.workerStateFile(worker)); err == nil {
					if ws.PID != nil && processAlive(*ws.PID) {
						killProcess(*ws.PID)
					}
				}
				p.tearDownWorker(worker)
			}(worker)
		}
		wg.Wait()
		os.RemoveAll(p.repo.poolDir())
		os.Remove(p.repo.poolConfigFile())
		return nil
	})
}

// tearDownWorker removes a worker's workspace, state, and log files.
// Internal; caller must hold the lock and have verified the worker is
// safe to remove.
func (p *Pool) tearDownWorker(worker string) {
	Teardown(p.repo, worker)
}

func nowUTC() string {
	return time.Now().UTC().Format("2006-01-02T15:04:05Z")
}

func elapsedDisplay(startedAt string, completedAt *string) string {
	start, err := time.Parse("2006-01-02T15:04:05Z", startedAt)
	if err != nil {
		return "-"
	}
	var end time.Time
	if completedAt != nil && *completedAt != "" {
		end, err = time.Parse("2006-01-02T15:04:05Z", *completedAt)
		if err != nil {
			end = time.Now().UTC()
		}
	} else {
		end = time.Now().UTC()
	}
	diff := end.Sub(start)
	mins := int(diff.Minutes())
	secs := int(diff.Seconds()) % 60
	return fmt.Sprintf("%dm %ds", mins, secs)
}

// ── Pool commands ──────────────────────────────────────────────────

func cmdPool(args []string) {
	subcmd := "list"
	if len(args) > 0 {
		subcmd = args[0]
		args = args[1:]
	}
	switch subcmd {
	case "create", "c", "resize", "r":
		cmdPoolResize(args)
	case "list", "ls", "status":
		cmdPoolList()
	case "rm", "remove":
		cmdPoolRm(args)
	case "destroy":
		cmdPoolDestroy()
	case "reset":
		cmdPoolReset(args)
	case "help":
		cmdHelp()
	default:
		if _, err := strconv.Atoi(subcmd); err == nil {
			cmdPoolResize(append([]string{subcmd}, args...))
		} else {
			fatal("Unknown pool command: %s", subcmd)
		}
	}
}

func cmdPoolResize(args []string) {
	if len(args) == 0 {
		fatal("Usage: wsg pool resize <N>")
	}

	var newSizeStr string
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--size", "-s":
			if i+1 < len(args) {
				newSizeStr = args[i+1]
				i++
			}
		default:
			if !strings.HasPrefix(args[i], "-") {
				newSizeStr = args[i]
			}
		}
	}
	if newSizeStr == "" {
		fatal("Usage: wsg pool resize <N>")
	}

	newSize, err := strconv.Atoi(newSizeStr)
	if err != nil {
		fatal("Invalid pool size: %s", newSizeStr)
	}

	r, err := newRepoContext()
	if err != nil {
		fatal("Not in a jj repo")
	}

	p, err := OpenPool(r)
	if err != nil {
		p, err = CreatePool(r)
		if err != nil {
			fatal("Create pool: %v", err)
		}
	}
	if err := p.Resize(newSize); err != nil {
		fatal("%v", err)
	}
}

func generateWorkerName() string {
	b := make([]byte, 3)
	rand.Read(b)
	return fmt.Sprintf("worker-%x", b)
}

func cmdPoolList() {
	r, err := newRepoContext()
	if err != nil {
		fatal("Not in a jj repo")
	}

	p, err := OpenPool(r)
	if err != nil {
		fatal("No pool. Run: wsg pool create --size N")
	}

	idle, busy, doneCount, failed := 0, 0, 0, 0

	fmt.Printf("%-10s %-10s %-14s %s\n", "WORKER", "STATUS", "TICKET", "ELAPSED")
	fmt.Printf("%-10s %-10s %-14s %s\n", "------", "------", "------", "-------")

	for _, worker := range p.Config().Workers {
		h, err := LoadLiveWorker(r, worker)
		if err != nil {
			continue
		}
		ws := h.Status()

		ticket := "-"
		if ws.Ticket != nil {
			ticket = *ws.Ticket
		}

		elapsed := "-"
		if ws.StartedAt != nil && *ws.StartedAt != "" {
			elapsed = elapsedDisplay(*ws.StartedAt, ws.CompletedAt)
		}

		paddedStatus := fmt.Sprintf("%-10s", ws.Status)
		switch ws.Status {
		case WorkerStatusIdle:
			paddedStatus = colorize(paddedStatus, colorDim)
		case WorkerStatusBusy:
			paddedStatus = colorize(paddedStatus, colorYellow)
		case WorkerStatusDone:
			paddedStatus = colorize(paddedStatus, colorGreen)
		case WorkerStatusFailed:
			paddedStatus = colorize(paddedStatus, colorRed)
		}

		fmt.Printf("%-10s %s %-14s %s\n", displayWorker(worker), paddedStatus, ticket, elapsed)

		switch ws.Status {
		case WorkerStatusIdle:
			idle++
		case WorkerStatusBusy:
			busy++
		case WorkerStatusDone:
			doneCount++
		case WorkerStatusFailed:
			failed++
		}
	}

	fmt.Println()
	fmt.Printf("Pool: %d idle, %d busy, %d done, %d failed (%d total)\n",
		idle, busy, doneCount, failed, p.Config().Size)
}

func cmdPoolDestroy() {
	r, err := newRepoContext()
	if err != nil {
		fatal("Not in a jj repo")
	}

	p, err := OpenPool(r)
	if err != nil {
		info("No pool to destroy")
		return
	}
	if err := p.Destroy(); err != nil {
		fatal("%v", err)
	}
	info("Pool destroyed")
}

func cmdPoolRm(args []string) {
	if len(args) == 0 {
		fatal("Usage: wsg pool rm <worker>")
	}
	worker := resolveWorker(args[0])

	r, err := newRepoContext()
	if err != nil {
		fatal("Not in a jj repo")
	}

	p, err := OpenPool(r)
	if err != nil {
		fatal("No pool. Run: wsg pool create --size N")
	}
	size, err := p.Remove(worker)
	if err != nil {
		fatal("%v", err)
	}
	info("Removed %s (pool size: %d)", worker, size)
}

func cmdPoolReset(args []string) {
	if len(args) == 0 {
		fatal("Usage: wsg pool reset <worker>")
	}
	worker := resolveWorker(args[0])

	r, err := newRepoContext()
	if err != nil {
		fatal("Not in a jj repo")
	}

	if err := NewActions(r).Reset(worker); err != nil {
		fatal("Reset %s: %v", worker, err)
	}
	info("Reset %s to idle", worker)
}
