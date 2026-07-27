package main

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

type PoolConfig struct {
	Size       int       `json:"size"`
	GHRepo     string    `json:"gh_repo"`
	Workers    []string  `json:"workers"`
	CreatedAt  string    `json:"created_at"`
	Foreground *bool     `json:"foreground,omitempty"`
	Agent      AgentKind `json:"agent,omitempty"`
	// Names maps a worker id to a user-chosen display alias. Cosmetic only -
	// it never changes the worker's id, workspace directory, or jj workspace
	// name. Stored here (not in worker-N.json) so it survives the reset /
	// redispatch cycle that wipes worker state. Optional and additive, so
	// jj-wsx (which ignores unknown keys) stays compatible.
	Names map[string]string `json:"names,omitempty"`
	extra map[string]json.RawMessage
}

type poolConfigWire PoolConfig

func (cfg PoolConfig) MarshalJSON() ([]byte, error) {
	return marshalWithExtras(poolConfigWire(cfg), cfg.extra)
}

func (cfg *PoolConfig) UnmarshalJSON(data []byte) error {
	var wire poolConfigWire
	extra, err := unmarshalWithExtras(data, &wire, "size", "gh_repo", "workers", "created_at", "foreground", "agent", "names")
	if err != nil {
		return err
	}
	*cfg = PoolConfig(wire)
	cfg.extra = extra
	return nil
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
	return writeJSONAtomic(path, cfg)
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
	if _, err := commitJSONState(r.poolConfigFile(), filepath.Join(r.poolDir(), ".dispatch.lock"), stateRevision{}, cfg); err != nil {
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
	return withStateLock(lockPath, func() error {
		cfg, err := loadPoolConfig(p.repo.poolConfigFile())
		if err != nil {
			return fmt.Errorf("reload pool config: %w", err)
		}
		p.cfg = cfg
		return fn()
	})
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
	for attempts := 0; attempts < 3; attempts++ {
		workers, err := p.Reserve(tickets)
		if err == nil {
			return workers, nil
		}
		var full *PoolFull
		if !errors.As(err, &full) {
			return nil, err
		}

		expectedSize := 0
		if err := p.withLock(func() error {
			expectedSize = p.cfg.Size
			return nil
		}); err != nil {
			return nil, err
		}
		prepared, err := p.provisionWorkers(full.Gap())
		if err != nil {
			return nil, err
		}

		var reserved []string
		adopted := false
		err = p.withLock(func() error {
			if p.cfg.Size != expectedSize {
				return errStateConflict
			}
			allWorkers := append(append([]string(nil), p.cfg.Workers...), prepared...)
			lockPaths := make([]string, 0, len(allWorkers))
			for _, worker := range allWorkers {
				lockPaths = append(lockPaths, p.repo.workerStateFile(worker)+".lock")
			}
			return withStateLocks(lockPaths, func() error {
				type slot struct {
					name  string
					path  string
					state *WorkerState
				}
				picks := make([]slot, 0, len(tickets))
				for _, worker := range allWorkers {
					statePath := p.repo.workerStateFile(worker)
					state, loadErr := loadWorkerState(statePath)
					if loadErr == nil && state.Status == WorkerStatusIdle {
						picks = append(picks, slot{name: worker, path: statePath, state: state})
						if len(picks) == len(tickets) {
							break
						}
					}
				}
				if len(picks) < len(tickets) {
					return errStateConflict
				}
				p.cfg.Workers = allWorkers
				p.cfg.Size = len(allWorkers)
				if saveErr := savePoolConfig(p.repo.poolConfigFile(), p.cfg); saveErr != nil {
					return saveErr
				}
				adopted = true
				reserved = make([]string, len(tickets))
				for index, pick := range picks {
					ticket := tickets[index]
					pick.state.MarkDispatched(ticket, filepath.Join(p.repo.poolDir(), pick.name+".log"), strings.ToLower(ticket))
					if saveErr := saveWorkerState(pick.path, pick.state); saveErr != nil {
						return saveErr
					}
					reserved[index] = pick.name
				}
				return nil
			})
		})
		if !adopted {
			p.cleanupWorkers(prepared)
		}
		if err == nil {
			return reserved, nil
		}
		if !errors.Is(err, errStateConflict) {
			return nil, err
		}
	}
	return nil, fmt.Errorf("reserve after pool growth: %w", errStateConflict)
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
	lockPaths := make([]string, 0, len(p.cfg.Workers))
	for _, worker := range p.cfg.Workers {
		lockPaths = append(lockPaths, p.repo.workerStateFile(worker)+".lock")
	}
	var out []string
	err := withStateLocks(lockPaths, func() error {
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
			if err != nil || ws.Status != WorkerStatusIdle {
				continue
			}
			picks = append(picks, slot{name: worker, sf: sf, ws: ws})
		}
		if len(picks) < need {
			return &PoolFull{Need: need, Have: len(picks)}
		}
		poolDir := p.repo.poolDir()
		out = make([]string, need)
		for i, slot := range picks {
			ticket := tickets[i]
			slot.ws.MarkDispatched(ticket, filepath.Join(poolDir, slot.name+".log"), strings.ToLower(ticket))
			if err := saveWorkerState(slot.sf, slot.ws); err != nil {
				return fmt.Errorf("save worker state: %w", err)
			}
			out[i] = slot.name
		}
		return nil
	})
	return out, err
}

// hasWorker reports whether name is a member of the pool. Caller may hold
// the lock or not; it reads the in-memory cfg either way.
func (p *Pool) hasWorker(name string) bool {
	for _, w := range p.cfg.Workers {
		if w == name {
			return true
		}
	}
	return false
}

// ReserveWorker atomically marks one specific idle worker busy with ticket.
// Unlike Reserve, which picks the first idle worker in pool order, this
// targets a named slot - the path behind the TUI's [n] dispatch-to-selected.
// Errors (without touching state) if the worker is not in the pool or not
// idle. Serialises against concurrent Claim / Reserve / Resize via the lock.
func (p *Pool) ReserveWorker(name, ticket string) error {
	return p.withLock(func() error {
		if !p.hasWorker(name) {
			return fmt.Errorf("worker %s not in pool", name)
		}
		sf := p.repo.workerStateFile(name)
		return withStateLock(sf+".lock", func() error {
			ws, err := loadWorkerState(sf)
			if err != nil {
				return fmt.Errorf("load worker %s: %w", name, err)
			}
			if ws.Status != WorkerStatusIdle {
				return fmt.Errorf("worker %s is %s, not idle", name, ws.Status)
			}
			logFile := filepath.Join(p.repo.poolDir(), name+".log")
			ws.MarkDispatched(ticket, logFile, strings.ToLower(ticket))
			if err := saveWorkerState(sf, ws); err != nil {
				return fmt.Errorf("save worker state: %w", err)
			}
			return nil
		})
	})
}

// Name returns the display alias for a worker, or "" if unset.
func (p *Pool) Name(worker string) string {
	return p.cfg.Names[worker]
}

// SetName sets (or, with an empty name, clears) a worker's display alias in
// pool.json. The alias is cosmetic - it never touches the worker id,
// workspace directory, or jj workspace name. Errors if the worker is not in
// the pool. Serialises against other pool mutations via the lock.
func (p *Pool) SetName(worker, name string) error {
	return p.withLock(func() error {
		if !p.hasWorker(worker) {
			return fmt.Errorf("worker %s not in pool", worker)
		}
		name = strings.TrimSpace(name)
		if p.cfg.Names == nil {
			p.cfg.Names = map[string]string{}
		}
		if name == "" {
			delete(p.cfg.Names, worker)
		} else {
			p.cfg.Names[worker] = name
		}
		return savePoolConfig(p.repo.poolConfigFile(), p.cfg)
	})
}

// Resize grows or shrinks the pool while locks protect only state decisions
// and writes. Slow workspace commands run before or after the locked commit.
func (p *Pool) Resize(newSize int) error {
	currentSize := 0
	if err := p.withLock(func() error {
		currentSize = p.cfg.Size
		return nil
	}); err != nil {
		return err
	}
	if newSize == currentSize {
		info("Pool is already size %d", currentSize)
		return nil
	}
	if newSize > currentSize {
		return p.grow(newSize, currentSize)
	}
	return p.shrink(newSize)
}

func (p *Pool) grow(newSize, expectedSize int) error {
	workers, err := p.provisionWorkers(newSize - expectedSize)
	if err != nil {
		return err
	}
	err = p.withLock(func() error {
		if p.cfg.Size != expectedSize {
			return errStateConflict
		}
		p.cfg.Workers = append(p.cfg.Workers, workers...)
		p.cfg.Size = newSize
		return savePoolConfig(p.repo.poolConfigFile(), p.cfg)
	})
	if err != nil {
		p.cleanupWorkers(workers)
		return err
	}
	for _, worker := range workers {
		info("  Created %s", worker)
	}
	info("Pool expanded from %d to %d", expectedSize, newSize)
	return nil
}

func (p *Pool) provisionWorkers(count int) ([]string, error) {
	workers := make([]string, 0, count)
	waits := make([]func(), 0, count)
	for i := 0; i < count; i++ {
		name := generateWorkerName()
		wait, err := Provision(p.repo, name, "", WorkerRole)
		if err != nil {
			for _, wait := range waits {
				wait()
			}
			p.cleanupWorkers(workers)
			return nil, fmt.Errorf("create %s: %w", name, err)
		}
		workers = append(workers, name)
		waits = append(waits, wait)
	}
	for _, wait := range waits {
		wait()
	}
	return workers, nil
}

func (p *Pool) cleanupWorkers(workers []string) {
	for _, worker := range workers {
		_ = Teardown(p.repo, worker)
	}
}

func (p *Pool) shrink(newSize int) error {
	var removed []string
	oldSize := 0
	err := p.withLock(func() error {
		oldSize = p.cfg.Size
		if newSize >= oldSize {
			return errStateConflict
		}
		lockPaths := make([]string, 0, len(p.cfg.Workers))
		for _, worker := range p.cfg.Workers {
			lockPaths = append(lockPaths, p.repo.workerStateFile(worker)+".lock")
		}
		return withStateLocks(lockPaths, func() error {
			busy := 0
			for i := len(p.cfg.Workers) - 1; i >= newSize; i-- {
				worker := p.cfg.Workers[i]
				state, loadErr := loadWorkerState(p.repo.workerStateFile(worker))
				if loadErr == nil && state.Status.IsActive() {
					busy++
					continue
				}
				removed = append(removed, worker)
			}
			toRemove := oldSize - newSize
			if len(removed) < toRemove {
				minimum := oldSize - len(removed)
				return fmt.Errorf("cannot shrink to %d: %d worker(s) busy.\nMinimum safe size is %d. Use 'wsg pool list' to see status", newSize, busy, minimum)
			}
			removed = removed[:toRemove]
			removedSet := make(map[string]bool, len(removed))
			for _, worker := range removed {
				removedSet[worker] = true
				delete(p.cfg.Names, worker)
			}
			remaining := p.cfg.Workers[:0]
			for _, worker := range p.cfg.Workers {
				if !removedSet[worker] {
					remaining = append(remaining, worker)
				}
			}
			p.cfg.Workers = remaining
			p.cfg.Size = len(remaining)
			return savePoolConfig(p.repo.poolConfigFile(), p.cfg)
		})
	})
	if err != nil {
		return err
	}
	for _, worker := range removed {
		_ = Teardown(p.repo, worker)
		info("  Removed %s", worker)
	}
	info("Pool shrunk from %d to %d", oldSize, newSize)
	return nil
}

// Remove atomically removes one non-busy Worker from the pool, then performs
// slow Workspace teardown after releasing all state locks.
func (p *Pool) Remove(worker string) (int, error) {
	newSize := 0
	err := p.withLock(func() error {
		index := -1
		for i, candidate := range p.cfg.Workers {
			if candidate == worker {
				index = i
				break
			}
		}
		if index < 0 {
			return fmt.Errorf("worker %s not in pool", worker)
		}
		path := p.repo.workerStateFile(worker)
		return withStateLock(path+".lock", func() error {
			if state, err := loadWorkerState(path); err == nil && state.Status.IsActive() {
				return fmt.Errorf("worker %s is busy. Reset it first: wsg pool reset %s", worker, worker)
			}
			p.cfg.Workers = append(p.cfg.Workers[:index], p.cfg.Workers[index+1:]...)
			p.cfg.Size = len(p.cfg.Workers)
			delete(p.cfg.Names, worker)
			newSize = p.cfg.Size
			return savePoolConfig(p.repo.poolConfigFile(), p.cfg)
		})
	})
	if err != nil {
		return 0, err
	}
	if err := Teardown(p.repo, worker); err != nil {
		return newSize, err
	}
	return newSize, nil
}

// Destroy removes persisted membership under the pool and Worker locks, then
// terminates processes and tears down Workspaces after releasing the locks.
func (p *Pool) Destroy() error {
	var workers []string
	var pids []int
	err := p.withLock(func() error {
		workers = append(workers, p.cfg.Workers...)
		entries, readErr := os.ReadDir(p.repo.poolDir())
		if readErr != nil && !os.IsNotExist(readErr) {
			return fmt.Errorf("read pool state directory: %w", readErr)
		}
		lockPaths := make([]string, 0, len(workers)+len(entries))
		for _, worker := range workers {
			lockPaths = append(lockPaths, p.repo.workerStateFile(worker)+".lock")
		}
		for _, entry := range entries {
			if strings.HasPrefix(entry.Name(), "dispatch-") && strings.HasSuffix(entry.Name(), ".json") {
				lockPaths = append(lockPaths, filepath.Join(p.repo.poolDir(), entry.Name()+".lock"))
			}
		}
		return withStateLocks(lockPaths, func() error {
			for _, worker := range workers {
				path := p.repo.workerStateFile(worker)
				if state, loadErr := loadWorkerState(path); loadErr == nil && state.PID != nil {
					pids = append(pids, *state.PID)
				}
				if removeErr := os.Remove(path); removeErr != nil && !os.IsNotExist(removeErr) {
					return removeErr
				}
			}
			for _, entry := range entries {
				if strings.HasSuffix(entry.Name(), ".lock") {
					continue
				}
				if removeErr := os.RemoveAll(filepath.Join(p.repo.poolDir(), entry.Name())); removeErr != nil {
					return fmt.Errorf("remove pool state %s: %w", entry.Name(), removeErr)
				}
			}
			if removeErr := os.Remove(p.repo.poolConfigFile()); removeErr != nil && !os.IsNotExist(removeErr) {
				return fmt.Errorf("remove pool config: %w", removeErr)
			}
			return nil
		})
	})
	if err != nil {
		return err
	}
	for _, pid := range pids {
		if processAlive(pid) {
			killProcess(pid)
		}
	}
	var wait sync.WaitGroup
	for _, worker := range workers {
		wait.Add(1)
		go func(worker string) {
			defer wait.Done()
			_ = Teardown(p.repo, worker)
		}(worker)
	}
	wait.Wait()
	return nil
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

	fmt.Printf("%-10s %-12s %-10s %-14s %s\n", "WORKER", "NAME", "STATUS", "TICKET", "ELAPSED")
	fmt.Printf("%-10s %-12s %-10s %-14s %s\n", "------", "----", "------", "------", "-------")

	for _, v := range snap.Workers {
		ws := v.State

		name := "-"
		if alias := p.Name(v.Name); alias != "" {
			name = alias
		}

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

		fmt.Printf("%-10s %-12s %s %-14s %s\n", displayWorker(v.Name), name, paddedStatus, ticket, elapsed)
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
