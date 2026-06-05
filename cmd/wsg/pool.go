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
	Status      string  `json:"status"`
	Ticket      *string `json:"ticket"`
	PID         *int    `json:"pid"`
	StartedAt   *string `json:"started_at"`
	CompletedAt *string `json:"completed_at"`
	LogFile     *string `json:"log_file"`
	BranchName  *string `json:"branch_name"`
	ExitCode    *int    `json:"exit_code"`
	Error       *string `json:"error"`
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
	return &WorkerState{Status: "idle"}
}

func (ws *WorkerState) MarkDispatched(ticket, logFile, branchName string) {
	now := nowUTC()
	ws.Status = "busy"
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
	ws.Status = "done"
	ws.CompletedAt = &now
	ws.ExitCode = &exitCode
}

func (ws *WorkerState) MarkFailed(exitCode int, errMsg string) {
	now := nowUTC()
	ws.Status = "failed"
	ws.CompletedAt = &now
	ws.ExitCode = &exitCode
	ws.Error = &errMsg
}

func (ws *WorkerState) SetPID(pid int) {
	ws.PID = &pid
}

func (ws *WorkerState) Reset() {
	*ws = WorkerState{Status: "idle"}
}

func (ws *WorkerState) MarkResumed(logFile string) {
	now := nowUTC()
	ws.Status = "busy"
	ws.StartedAt = &now
	ws.LogFile = &logFile
	ws.CompletedAt = nil
	ws.ExitCode = nil
	ws.Error = nil
}

// ── WorkerHandle ──────────────────────────────────────────────────

type WorkerHandle struct {
	path  string
	state *WorkerState
}

func OpenWorker(path string) (*WorkerHandle, error) {
	ws, err := loadWorkerState(path)
	if err != nil {
		return nil, err
	}
	return &WorkerHandle{path: path, state: ws}, nil
}

func CreateIdleWorker(path string) (*WorkerHandle, error) {
	ws := newIdleWorkerState()
	h := &WorkerHandle{path: path, state: ws}
	if err := h.save(); err != nil {
		return nil, err
	}
	return h, nil
}

func (h *WorkerHandle) State() *WorkerState {
	return h.state
}

func (h *WorkerHandle) Dispatch(ticket, logFile, branchName string) error {
	h.state.MarkDispatched(ticket, logFile, branchName)
	return h.save()
}

func (h *WorkerHandle) Done(exitCode int) error {
	h.state.MarkDone(exitCode)
	return h.save()
}

func (h *WorkerHandle) Failed(exitCode int, errMsg string) error {
	h.state.MarkFailed(exitCode, errMsg)
	return h.save()
}

func (h *WorkerHandle) Resume(logFile string) error {
	h.state.MarkResumed(logFile)
	return h.save()
}

func (h *WorkerHandle) SetPID(pid int) error {
	h.state.SetPID(pid)
	return h.save()
}

func (h *WorkerHandle) Reset() error {
	h.state.Reset()
	return h.save()
}

// Reclaim returns the worker to idle: kills any live PID, resets state, and
// fires an async jj restore + jj new main in the workspace dir if it exists.
// The workspace restore is fire-and-forget; callers should not assume it has
// completed when Reclaim returns.
func (h *WorkerHandle) Reclaim(r *RepoContext, worker string) error {
	if h.state.PID != nil && processAlive(*h.state.PID) {
		killProcess(*h.state.PID)
	}
	if err := h.Reset(); err != nil {
		return err
	}
	wspath := r.workerDir(worker)
	if fi, err := os.Stat(wspath); err == nil && fi.IsDir() {
		startBackground(wspath, os.DevNull, "sh", "-c",
			"jj restore 2>/dev/null; jj new main 2>/dev/null")
	}
	return nil
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

func (h *WorkerHandle) CheckLiveness(r *RepoContext, worker string) {
	ws := h.state
	if ws.Status != "busy" || ws.PID == nil {
		return
	}
	if processAlive(*ws.PID) {
		return
	}
	h.finalizeFromLog(r, worker)
}

// LoadLiveWorker opens a worker's state and reconciles it against the running
// world: if recorded as busy but the PID is no longer alive, the worker is
// finalised from its log (to done or failed) and that transition is persisted
// to disk before the handle is returned. Use this on every read-then-decide
// path (list, shrink, remove, DAG progression, TUI render, resume); use the
// raw OpenWorker for mutation-only paths where reconciliation would be a
// wasted log read.
func LoadLiveWorker(r *RepoContext, worker string) (*WorkerHandle, error) {
	h, err := OpenWorker(r.workerStateFile(worker))
	if err != nil {
		return nil, err
	}
	h.CheckLiveness(r, worker)
	return h, nil
}

// finalizeFromLog transitions a busy worker to its terminal state from the
// agent's stream-json log: done (with the logged exit code and resolved branch)
// on a success result, failed otherwise - including a run the CLI reports as
// is_error even though the process itself exits 0. A missing or unparseable
// result means the process died without reporting, also a failure.
func (h *WorkerHandle) finalizeFromLog(r *RepoContext, worker string) {
	ws := h.state
	if ws.LogFile != nil {
		if result := readLogResult(*ws.LogFile); result != nil {
			if result.Status == "done" {
				ec := 0
				if result.ExitCode != nil {
					ec = *result.ExitCode
				}
				h.Done(ec)
				resolveWorkerBranch(r, worker, ws)
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
				h.Failed(ec, errMsg)
			}
			return
		}
	}
	h.Failed(1, "Process exited unexpectedly")
}

// Launch spawns claude in the worker's workspace using inv. The handle must
// already be in busy state (claimIdleWorker for dispatch, h.Resume for resume).
// In foreground mode the process runs to completion and finalises the handle;
// the returned pid is 0. In background mode the pid is returned and the handle
// is finalised asynchronously when the process exits.
func (h *WorkerHandle) Launch(r *RepoContext, worker string, inv claudeInvocation, fg bool) (int, error) {
	wspath := r.workerDir(worker)
	logFile := filepath.Join(r.poolDir(), worker+".log")
	argv := append([]string{"claude"}, inv.Args()...)
	if fg {
		h.RunFG(wspath, logFile, argv)
		return 0, nil
	}
	return h.RunBG(r, worker, wspath, logFile, argv)
}

func (h *WorkerHandle) RunFG(wspath, logFile string, claudeArgs []string) {
	exitCode, err := startForeground(wspath, logFile, claudeArgs[0], claudeArgs[1:]...)
	if err != nil {
		h.Failed(1, err.Error())
	} else if exitCode == 0 {
		h.Done(exitCode)
	} else {
		h.Failed(exitCode, "")
	}
}

func (h *WorkerHandle) RunBG(r *RepoContext, worker, wspath, logFile string, claudeArgs []string) (int, error) {
	pid, err := startBackground(wspath, logFile, claudeArgs[0], claudeArgs[1:]...)
	if err != nil {
		h.Failed(1, err.Error())
		return 0, err
	}
	h.SetPID(pid)

	path := h.path
	go func() {
		waitForProcess(pid)
		h, err := OpenWorker(path)
		if err != nil {
			return
		}
		if h.State().Status == "busy" {
			h.finalizeFromLog(r, worker)
		}
	}()

	return pid, nil
}

func resolveWorkerBranch(r *RepoContext, worker string, ws *WorkerState) {
	if ws.BranchName == nil || *ws.BranchName == "" {
		return
	}
	if resolved := jjResolveBranchForTicket(r.workerDir(worker), *ws.BranchName); resolved != nil {
		ws.BranchName = resolved
	}
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
		switch h.State().Status {
		case "idle":
			snap.Idle++
		case "busy":
			snap.Busy++
		case "done":
			snap.Done++
		case "failed":
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
			if ws.Status != "idle" {
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
	poolDir := r.poolDir()
	oldSize := p.cfg.Size
	var wg sync.WaitGroup
	for i := 0; i < newSize-oldSize; i++ {
		name := generateWorkerName()
		wspath := r.workerDir(name)
		if err := jjAddWorkspace(r.Root, wspath, ""); err != nil {
			return fmt.Errorf("create %s: %w", name, err)
		}
		p.cfg.Workers = append(p.cfg.Workers, name)
		cacheAddEntry(r.cacheFile(), name, wspath)
		wg.Add(1)
		go func(name, wspath string) {
			defer wg.Done()
			copyEnvFile(r.Root, wspath)
			copySynapseClone(r.Root, wspath)
			CreateIdleWorker(filepath.Join(poolDir, name+".json"))
		}(name, wspath)
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
		s := h.State().Status
		if s == "idle" || s == "done" || s == "failed" {
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
			if h.State().Status == "busy" {
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
	r := p.repo
	wspath := r.workerDir(worker)
	jjForgetWorkspace(r.Root, worker)
	cacheRemoveEntry(r.cacheFile(), worker)
	if fi, err := os.Stat(wspath); err == nil && fi.IsDir() {
		os.RemoveAll(wspath)
	}
	os.Remove(filepath.Join(r.poolDir(), worker+".json"))
	os.Remove(filepath.Join(r.poolDir(), worker+".log"))
}

func ghRepo(r *RepoContext) string {
	configFile := r.poolConfigFile()
	if cfg, err := loadPoolConfig(configFile); err == nil && cfg.GHRepo != "" {
		return strings.TrimSuffix(cfg.GHRepo, ".git")
	}
	return jjRemoteOrigin(r.Root)
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
		ws := h.State()

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
		case "idle":
			paddedStatus = colorize(paddedStatus, colorDim)
		case "busy":
			paddedStatus = colorize(paddedStatus, colorYellow)
		case "done":
			paddedStatus = colorize(paddedStatus, colorGreen)
		case "failed":
			paddedStatus = colorize(paddedStatus, colorRed)
		}

		fmt.Printf("%-10s %s %-14s %s\n", displayWorker(worker), paddedStatus, ticket, elapsed)

		switch ws.Status {
		case "idle":
			idle++
		case "busy":
			busy++
		case "done":
			doneCount++
		case "failed":
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

	sf := r.workerStateFile(worker)
	h, err := OpenWorker(sf)
	if err != nil {
		fatal("Worker %s not found", worker)
	}

	if err := h.Reclaim(r, worker); err != nil {
		fatal("Reset %s: %v", worker, err)
	}
	info("Reset %s to idle", worker)
}
