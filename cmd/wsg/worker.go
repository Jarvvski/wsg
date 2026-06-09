package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// WorkerState carries the worker's best-known branch in a single Branch
// field: the ticket-derived placeholder set at dispatch, replaced in place
// by the agent's real branch once jj bookmarks have been scanned. On disk
// it maps to a single `branch_name` key for jj-wsx wire compat. Callers
// that need the real branch (not the placeholder) go through
// WorkerHandle.Branch(), which scans jj on first read and persists.
type WorkerState struct {
	Status      WorkerStatus
	Ticket      *string
	PID         *int
	StartedAt   *string
	CompletedAt *string
	LogFile     *string
	Branch      string
	ExitCode    *int
	Error       *string
}

// workerStateWire is the on-disk shape jj-wsx consumes. BranchName carries
// resolved-then-prefix; all other fields mirror WorkerState 1:1.
type workerStateWire struct {
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

func (ws *WorkerState) MarshalJSON() ([]byte, error) {
	w := workerStateWire{
		Status:      ws.Status,
		Ticket:      ws.Ticket,
		PID:         ws.PID,
		StartedAt:   ws.StartedAt,
		CompletedAt: ws.CompletedAt,
		LogFile:     ws.LogFile,
		BranchName:  branchNameWire(ws.Branch),
		ExitCode:    ws.ExitCode,
		Error:       ws.Error,
	}
	return json.Marshal(w)
}

func (ws *WorkerState) UnmarshalJSON(data []byte) error {
	var w workerStateWire
	if err := json.Unmarshal(data, &w); err != nil {
		return err
	}
	ws.Status = w.Status
	ws.Ticket = w.Ticket
	ws.PID = w.PID
	ws.StartedAt = w.StartedAt
	ws.CompletedAt = w.CompletedAt
	ws.LogFile = w.LogFile
	ws.ExitCode = w.ExitCode
	ws.Error = w.Error
	ws.Branch = ""
	if w.BranchName != nil {
		ws.Branch = *w.BranchName
	}
	return nil
}

func branchNameWire(branch string) *string {
	if branch == "" {
		return nil
	}
	return &branch
}

// isResolvedBranch reports whether s is the agent's real branch
// ("<owner>/<ticket>-<slug>") rather than the dispatch-time placeholder.
// Used by Branch() to decide whether to scan jj for the real name.
func isResolvedBranch(s string) bool {
	return strings.HasPrefix(s, branchOwner+"/")
}

func newIdleWorkerState() *WorkerState {
	return &WorkerState{Status: WorkerStatusIdle}
}

func (ws *WorkerState) MarkDispatched(ticket, logFile, branchPlaceholder string) {
	now := nowUTC()
	ws.Status = WorkerStatusBusy
	ws.Ticket = &ticket
	ws.StartedAt = &now
	ws.LogFile = &logFile
	ws.Branch = branchPlaceholder
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

// Status returns a read-only snapshot of the worker's current state. The
// returned pointer aliases the handle's internal state and reflects any
// later mutation through this handle; callers should not mutate it.
func (h *WorkerHandle) Status() *WorkerState {
	return h.state
}

// DispatchIntent is the typed input to WorkerHandle.Dispatch: the ticket
// the worker is running, the model to run it on, optional dependency
// context for a stacked branch, and whether to run in foreground. The
// handle owns everything else - workspace prep, identity lookup, prompt
// build, claude launch - so dispatch.go is a pure CLI shell.
type DispatchIntent struct {
	Ticket     string
	Model      string
	DepCtx     *DependencyContext
	Foreground bool
}

// Dispatch prepares the worker's workspace, builds the agent prompts from
// repo identity, and launches claude. The worker must already be in busy
// state - Pool.Claim sets the ticket/log/branch fields atomically before
// returning the worker name. On any pre-launch failure the worker is reset
// to idle so the slot stays usable; the returned error carries the cause.
// In foreground mode the process runs to completion and finalises the
// handle; the returned pid is 0. In background mode the pid is returned
// and the supervisor goroutine finalises the handle asynchronously when
// the process exits.
func (h *WorkerHandle) Dispatch(intent DispatchIntent) (int, error) {
	wspath := h.repo.workerDir(h.worker)

	baseRevs := []string{"main"}
	if intent.DepCtx != nil && len(intent.DepCtx.BaseBranches) > 0 {
		baseRevs = intent.DepCtx.BaseBranches
	}
	if err := jjNewOn(wspath, baseRevs...); err != nil {
		h.reset()
		return 0, fmt.Errorf("set %s to %v: %w", h.worker, baseRevs, err)
	}

	userEmail, err := jjConfigGet(h.repo.Root, "user.email")
	if err != nil {
		h.reset()
		return 0, fmt.Errorf("read jj user.email: %w", err)
	}
	userName, err := jjConfigGet(h.repo.Root, "user.name")
	if err != nil {
		h.reset()
		return 0, fmt.Errorf("read jj user.name: %w", err)
	}
	branchPrefix := strings.ToLower(strings.Fields(userName)[0])

	repo := ghRepo(h.repo)
	ticketLower := strings.ToLower(intent.Ticket)
	systemPrompt := buildDispatchSystemPrompt(repo, branchPrefix, ticketLower, intent.DepCtx)
	prCreateCmd := ghPRCreateCmd(repo, intent.Ticket, intent.DepCtx)
	workerPrompt := buildDispatchWorkerPrompt(intent.Ticket, userEmail, branchPrefix, ticketLower, prCreateCmd)

	inv := claudeInvocation{
		Model:        intent.Model,
		Name:         fmt.Sprintf("pool:%s:%s", h.worker, intent.Ticket),
		SystemPrompt: systemPrompt,
		Prompt:       workerPrompt,
	}
	return h.launch(inv, intent.Foreground)
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

// Branch returns the worker's real branch, scanning jj bookmarks if only
// the dispatch-time placeholder is on file and persisting the resolved
// value so concurrent readers see it too. Returns "" if no dispatch has
// ever run, or the placeholder itself if jj can't be scanned (no repo
// wired on the handle, or no real branch exists yet).
func (h *WorkerHandle) Branch() string {
	if h.resolveBranchInMemory() {
		_ = h.save()
	}
	return h.state.Branch
}

// resolveBranchInMemory swaps the dispatch-time placeholder in h.state.Branch
// for the agent's real branch if jj bookmarks know about it. Returns true if
// the value changed, so callers can choose whether to persist (finalizeFromLog
// folds the save into its single state write; Branch() persists on the spot).
func (h *WorkerHandle) resolveBranchInMemory() bool {
	b := h.state.Branch
	if b == "" || isResolvedBranch(b) {
		return false
	}
	if h.repo == nil || h.worker == "" {
		return false
	}
	resolved := jjResolveBranchForTicket(h.repo.workerDir(h.worker), b)
	if resolved == nil {
		return false
	}
	h.state.Branch = *resolved
	return true
}

// WorkerReader is a stateful, mtime-gated wrapper around LoadLiveWorker for
// tick-style read loops (the orchestrator's watch loop). Each Read stats
// the worker's state file; identical mtimes return the cached *WorkerState
// without re-parsing JSON or re-running liveness reconciliation. Once the
// file is rewritten - by the supervisor goroutine finalising a busy worker,
// by Pool.Claim atomically marking idle→busy, or by any other process - the
// next Read sees a fresh mtime, falls through to LoadLiveWorker, and
// re-caches. The returned pointer aliases the cache; callers must treat it
// as read-only, the same contract WorkerView.State already carries.
type WorkerReader struct {
	repo  *RepoContext
	cache map[string]workerCacheEntry
}

type workerCacheEntry struct {
	mtime time.Time
	state *WorkerState
}

// NewWorkerReader returns a fresh reader with an empty cache. A reader's
// cache grows to one entry per worker name it has ever read; for the
// orchestrator that is bounded by the pool size.
func NewWorkerReader(r *RepoContext) *WorkerReader {
	return &WorkerReader{repo: r, cache: map[string]workerCacheEntry{}}
}

// Read returns the worker's current state, reusing the cached value when
// the state file's mtime is unchanged since the last successful read. A
// fresh mtime triggers a full LoadLiveWorker - the same reconciliation any
// other reader would perform - and the result is cached for the next tick.
func (wr *WorkerReader) Read(name string) (*WorkerState, error) {
	path := wr.repo.workerStateFile(name)
	fi, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if entry, ok := wr.cache[name]; ok && entry.mtime.Equal(fi.ModTime()) {
		return entry.state, nil
	}
	h, err := LoadLiveWorker(wr.repo, name)
	if err != nil {
		return nil, err
	}
	mtime := fi.ModTime()
	if fi2, err := os.Stat(path); err == nil {
		mtime = fi2.ModTime()
	}
	wr.cache[name] = workerCacheEntry{mtime: mtime, state: h.Status()}
	return h.Status(), nil
}
