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

func resolveForeground(r *RepoContext, flag *bool) bool {
	if flag != nil {
		return *flag
	}
	if cfg, err := loadPoolConfig(r.poolConfigFile()); err == nil && cfg.Foreground != nil {
		return *cfg.Foreground
	}
	return false
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

// PoolSnapshot is a moment-in-time view of the pool: one WorkerView per
// configured worker (reconciled against PIDs via LoadLiveWorker) plus the
// rolled-up status counts. It is the single external enumeration path -
// callers that want to walk the pool consume this rather than calling
// loadWorkerState or LoadLiveWorker directly, so liveness is uniform.
type PoolSnapshot struct {
	Size    int
	Workers []WorkerView
	Idle    int
	Busy    int
	Done    int
	Failed  int
}

// WorkerView is the frozen, liveness-reconciled view of one worker in a
// snapshot. State aliases the originating handle's internal state at
// snapshot time; callers must treat it as read-only.
type WorkerView struct {
	Name  string
	State *WorkerState
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
// via LoadLiveWorker, and returns one WorkerView per configured worker
// alongside the live status counts. Does not take the lock and is a
// best-effort view: another process can flip a worker's state the instant
// after the read returns. It exists for TUI rendering, CLI status output,
// and shell completion - never use it to make a claim decision. The
// reserve verbs (Reserve, GrowAndReserve, Claim) are the locked path.
func (p *Pool) Snapshot() *PoolSnapshot {
	snap := &PoolSnapshot{
		Size:    p.cfg.Size,
		Workers: make([]WorkerView, 0, len(p.cfg.Workers)),
	}
	for _, name := range p.cfg.Workers {
		h, err := LoadLiveWorker(p.repo, name)
		if err != nil {
			continue
		}
		ws := h.Status()
		snap.Workers = append(snap.Workers, WorkerView{Name: name, State: ws})
		switch ws.Status {
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

// PoolFull is the typed error returned by Reserve when the pool does
// not have enough idle workers to satisfy the request. Callers inspect
// Gap() to decide whether to prompt for a resize and follow up with
// GrowAndReserve, or fall back to per-ticket Claim for a partial run.
// No state has been written when PoolFull is returned.
type PoolFull struct {
	Need int
	Have int
}

func (e *PoolFull) Error() string {
	return fmt.Sprintf("pool full: %d idle, need %d", e.Have, e.Need)
}

func (e *PoolFull) Gap() int {
	return e.Need - e.Have
}

// Reserve atomically marks len(tickets) idle workers busy, one per
// ticket, in the order tickets are given. The returned slice aligns
// with the input by index. On shortage returns *PoolFull (without
// touching state) so the caller can decide between resize, partial
// dispatch, or abort. Serialises against concurrent Claim, Resize,
// Remove via the pool lock.
func (p *Pool) Reserve(tickets []string) ([]string, error) {
	var out []string
	err := p.withLock(func() error {
		picked, err := p.reserveLocked(tickets)
		if err != nil {
			return err
		}
		out = picked
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// GrowAndReserve grows the pool by the idle gap and reserves the
// requested workers in a single locked critical section. Used by the
// CLI when the user has agreed to a resize prompt - the grow and the
// reserve happen atomically so a concurrent process can't claim the
// freshly-grown slots out from under us.
func (p *Pool) GrowAndReserve(tickets []string) ([]string, error) {
	var out []string
	err := p.withLock(func() error {
		idle := 0
		for _, worker := range p.cfg.Workers {
			ws, lerr := loadWorkerState(p.repo.workerStateFile(worker))
			if lerr == nil && ws.Status == WorkerStatusIdle {
				idle++
			}
		}
		need := len(tickets)
		if idle < need {
			newSize := p.cfg.Size + (need - idle)
			if gerr := p.grow(newSize); gerr != nil {
				return gerr
			}
		}
		picked, rerr := p.reserveLocked(tickets)
		if rerr != nil {
			return rerr
		}
		out = picked
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// Claim atomically picks the first idle worker and marks it busy with
// ticket. Convenience wrapper over Reserve for the single-ticket call
// sites; the orchestrator's per-tick claim loop uses this, as do
// recovery paths that grow-then-claim one slot at a time.
func (p *Pool) Claim(ticket string) (string, error) {
	workers, err := p.Reserve([]string{ticket})
	if err != nil {
		return "", err
	}
	return workers[0], nil
}

// reserveLocked finds N idle workers and marks each busy with the
// corresponding ticket, in input order. Caller must hold the pool
// lock. On *PoolFull no state has been written.
func (p *Pool) reserveLocked(tickets []string) ([]string, error) {
	need := len(tickets)
	type slot struct {
		name string
		sf   string
		ws   *WorkerState
	}
	picks := make([]slot, 0, need)
	for _, worker := range p.cfg.Workers {
		if len(picks) == need {
			break
		}
		sf := p.repo.workerStateFile(worker)
		ws, err := loadWorkerState(sf)
		if err != nil {
			continue
		}
		if ws.Status != WorkerStatusIdle {
			continue
		}
		picks = append(picks, slot{name: worker, sf: sf, ws: ws})
	}
	if len(picks) < need {
		return nil, &PoolFull{Need: need, Have: len(picks)}
	}
	poolDir := p.repo.poolDir()
	out := make([]string, need)
	for i, s := range picks {
		ticket := tickets[i]
		logFile := filepath.Join(poolDir, s.name+".log")
		s.ws.MarkDispatched(ticket, logFile, strings.ToLower(ticket))
		if err := saveWorkerState(s.sf, s.ws); err != nil {
			return nil, fmt.Errorf("save worker state: %w", err)
		}
		out[i] = s.name
	}
	return out, nil
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

	snap := p.Snapshot()

	fmt.Printf("%-10s %-10s %-14s %s\n", "WORKER", "STATUS", "TICKET", "ELAPSED")
	fmt.Printf("%-10s %-10s %-14s %s\n", "------", "------", "------", "-------")

	for _, v := range snap.Workers {
		ws := v.State

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

		fmt.Printf("%-10s %s %-14s %s\n", displayWorker(v.Name), paddedStatus, ticket, elapsed)
	}

	fmt.Println()
	fmt.Printf("Pool: %d idle, %d busy, %d done, %d failed (%d total)\n",
		snap.Idle, snap.Busy, snap.Done, snap.Failed, snap.Size)
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
