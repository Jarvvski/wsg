package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type DispatchGroup struct {
	Parent    string                    `json:"parent"`
	CreatedAt string                    `json:"created_at"`
	GHRepo    string                    `json:"gh_repo"`
	SubIssues map[string]*SubIssueState `json:"sub_issues"`
	Opts      DispatchGroupOpts         `json:"opts"`
}

type SubIssueState struct {
	Title        string         `json:"title"`
	Status       SubIssueStatus `json:"status"`
	BlockedBy    []string       `json:"blocked_by"`
	Worker       *string        `json:"worker"`
	Branch       *string        `json:"branch"`
	DispatchedAt *string        `json:"dispatched_at"`
	CompletedAt  *string        `json:"completed_at"`
	SkipReason   *string        `json:"skip_reason,omitempty"`
	Retries      int            `json:"retries"`
}

type DispatchGroupOpts struct {
	Model string `json:"model"`
}

type DependencyContext struct {
	BaseBranches []string
	Context      string
	PRBase       string
}

// ── Persistence ────────────────────────────────────────────────────

func dispatchGroupFile(r *RepoContext, parent string) string {
	return filepath.Join(r.poolDir(), "dispatch-"+strings.ToLower(parent)+".json")
}

func loadDispatchGroup(path string) (*DispatchGroup, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var dg DispatchGroup
	if err := json.Unmarshal(data, &dg); err != nil {
		return nil, err
	}
	return &dg, nil
}

func saveDispatchGroup(path string, dg *DispatchGroup) error {
	data, err := json.MarshalIndent(dg, "", "  ")
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

// LoadLiveDispatchGroup reads the group for parent off disk, reconciles its
// sub-issue state against the worker pool and branch existence, then persists
// the reconciled state. Returns nil if no group exists.
func LoadLiveDispatchGroup(r *RepoContext, parent string) *DispatchGroup {
	path := dispatchGroupFile(r, parent)
	dg, err := loadDispatchGroup(path)
	if err != nil {
		return nil
	}
	dg.SyncFromWorkers(newLiveDispatchWorld(r, nil, nil))
	dg.RevalidateBranches(r)
	saveDispatchGroup(path, dg)
	return dg
}

func (dg *DispatchGroup) Save(r *RepoContext) error {
	return saveDispatchGroup(dispatchGroupFile(r, dg.Parent), dg)
}

// ── DAG queries (pure) ─────────────────────────────────────────────

func (dg *DispatchGroup) Ready() []string {
	var ready []string
	for id, si := range dg.SubIssues {
		if si.Status != SubIssueStatusPending {
			continue
		}
		allMet := true
		for _, dep := range si.BlockedBy {
			depState, ok := dg.SubIssues[dep]
			if !ok {
				continue
			}
			if !depState.Status.Unblocks() {
				allMet = false
				break
			}
		}
		if allMet {
			ready = append(ready, id)
		}
	}
	sort.Strings(ready)
	return ready
}

func (dg *DispatchGroup) MaxWaveSize() int {
	resolved := make(map[string]bool)
	for id, si := range dg.SubIssues {
		if si.Status == SubIssueStatusSkipped {
			resolved[id] = true
		}
	}

	maxSize := 0
	for {
		var wave []string
		for id, si := range dg.SubIssues {
			if resolved[id] || si.Status == SubIssueStatusSkipped {
				continue
			}
			allMet := true
			for _, dep := range si.BlockedBy {
				if !resolved[dep] {
					allMet = false
					break
				}
			}
			if allMet {
				wave = append(wave, id)
			}
		}
		if len(wave) == 0 {
			break
		}
		if len(wave) > maxSize {
			maxSize = len(wave)
		}
		for _, id := range wave {
			resolved[id] = true
		}
	}
	return maxSize
}

func (dg *DispatchGroup) Terminal() bool {
	for _, si := range dg.SubIssues {
		if si.Status.IsActive() {
			return false
		}
	}
	return true
}

func (dg *DispatchGroup) CountStatuses() (done, failed, skipped int) {
	for _, si := range dg.SubIssues {
		switch si.Status {
		case SubIssueStatusDone:
			done++
		case SubIssueStatusFailed:
			failed++
		case SubIssueStatusSkipped:
			skipped++
		}
	}
	return
}

func (dg *DispatchGroup) BaseBranchesFor(ticketID string) []string {
	si := dg.SubIssues[ticketID]
	if si == nil || len(si.BlockedBy) == 0 {
		return nil
	}
	var branches []string
	for _, dep := range si.BlockedBy {
		depState := dg.SubIssues[dep]
		if depState == nil || depState.Branch == nil {
			continue
		}
		branches = append(branches, *depState.Branch)
	}
	return branches
}

func (dg *DispatchGroup) DepContextFor(ticketID string) *DependencyContext {
	branches := dg.BaseBranchesFor(ticketID)
	if len(branches) == 0 {
		return nil
	}
	allMain := true
	for _, b := range branches {
		if b != "main" {
			allMain = false
			break
		}
	}
	if allMain {
		return nil
	}
	si := dg.SubIssues[ticketID]
	var lines []string
	for _, dep := range si.BlockedBy {
		depState := dg.SubIssues[dep]
		if depState == nil || depState.Branch == nil || *depState.Branch == "main" {
			continue
		}
		lines = append(lines, fmt.Sprintf("- Branch: %s (implements %s: \"%s\")", *depState.Branch, dep, depState.Title))
	}
	if len(lines) == 0 {
		return nil
	}
	return &DependencyContext{
		BaseBranches: branches,
		Context:      strings.Join(lines, "\n"),
		PRBase:       branches[0],
	}
}

// ── DAG mutations ──────────────────────────────────────────────────

func (dg *DispatchGroup) MarkDispatched(ticketID, worker string) {
	si := dg.SubIssues[ticketID]
	if si == nil {
		return
	}
	si.Status = SubIssueStatusDispatched
	si.Worker = &worker
	now := nowUTC()
	si.DispatchedAt = &now
}

// ── World-touching ops ─────────────────────────────────────────────

// DispatchWorld is the side-effecting surface AdvanceOnce drives the
// dispatch state machine against. Production is liveDispatchWorld, which
// reads worker state files and shells out to launchWorker. Tests inject a
// fake to exercise the full machine - including ResetWorker failure paths
// that are invisible against the live world because Reclaim's errors are
// swallowed at the call site.
type DispatchWorld interface {
	ReadWorker(name string) (*WorkerState, error)
	ResetWorker(name string) error
	ClaimWorker(ticket string) (string, error)
	LaunchWorker(worker, ticket string, depCtx *DependencyContext)
}

// SyncFromWorkers folds each dispatched sub-issue's worker outcome back
// into the sub-issue: WorkerStatusDone marks the sub-issue done and
// records the resolved branch; WorkerStatusFailed retries once before
// permanent failure. A failure path resets the worker for reuse through
// world; if the reset itself fails the sub-issue still progresses and the
// failure is logged.
func (dg *DispatchGroup) SyncFromWorkers(world DispatchWorld) bool {
	changed := false
	for id, si := range dg.SubIssues {
		if si.Status != SubIssueStatusDispatched || si.Worker == nil {
			continue
		}
		worker := *si.Worker
		ws, err := world.ReadWorker(worker)
		if err != nil {
			continue
		}
		switch ws.Status {
		case WorkerStatusDone:
			si.Status = SubIssueStatusDone
			now := nowUTC()
			si.CompletedAt = &now
			if ws.BranchName != nil {
				si.Branch = ws.BranchName
			}
			changed = true
			info("  %s completed (branch: %s)", colorize(id, colorGreen), ptrOr(si.Branch, "?"))
			if rerr := world.ResetWorker(worker); rerr != nil {
				info("  warning: reset %s for reuse failed: %v", worker, rerr)
			}
		case WorkerStatusFailed:
			if si.Retries < 1 {
				si.Retries++
				si.Status = SubIssueStatusPending
				si.Worker = nil
				si.DispatchedAt = nil
				changed = true
				info("  %s failed, will auto-retry (attempt %d)", colorize(id, colorYellow), si.Retries+1)
				if rerr := world.ResetWorker(worker); rerr != nil {
					info("  warning: reset %s for reuse failed: %v", worker, rerr)
				}
			} else {
				si.Status = SubIssueStatusFailed
				now := nowUTC()
				si.CompletedAt = &now
				changed = true
				errMsg := ptrOr(ws.Error, "unknown error")
				info("  %s failed after retry: %s", colorize(id, colorRed), errMsg)
			}
		}
	}
	return changed
}

// AdvanceOnce drives one tick of the dispatch state machine: syncs every
// dispatched sub-issue against its worker (with retry+reset), then for
// each Ready sub-issue claims+launches a worker. Returns true if any
// state was mutated so the caller can persist before sleeping. This is
// the seam the watch loop and tests share.
func (dg *DispatchGroup) AdvanceOnce(world DispatchWorld) bool {
	changed := dg.SyncFromWorkers(world)
	for _, tid := range dg.Ready() {
		worker, err := world.ClaimWorker(tid)
		if err != nil {
			info("No idle workers for %s, will retry next cycle", tid)
			continue
		}
		world.LaunchWorker(worker, tid, dg.DepContextFor(tid))
		dg.MarkDispatched(tid, worker)
		changed = true
	}
	return changed
}

func (dg *DispatchGroup) RevalidateBranches(r *RepoContext) {
	for id, si := range dg.SubIssues {
		if si.Branch == nil || *si.Branch == "main" {
			continue
		}
		if !jjRevExists(r.Root, *si.Branch) {
			if isMergedStatus(ptrOr(si.SkipReason, "")) {
				main := "main"
				si.Branch = &main
				info("  %s branch gone, using main (merged)", id)
			} else {
				resolved := resolveExistingBranch(r, id)
				if resolved != nil {
					si.Branch = resolved
					info("  %s branch re-resolved to %s", id, *resolved)
				} else {
					main := "main"
					si.Branch = &main
					info("  %s branch not found, falling back to main", id)
				}
			}
		}
	}
}

// liveDispatchWorld is the production DispatchWorld backed by the on-disk
// pool: ReadWorker via LoadLiveWorker (with dead-PID reconciliation),
// ResetWorker via Reclaim, ClaimWorker through the locked Pool, and
// LaunchWorker through the existing launchWorker shell-out.
type liveDispatchWorld struct {
	r    *RepoContext
	pool *Pool
	opts *DispatchOpts
}

func newLiveDispatchWorld(r *RepoContext, pool *Pool, opts *DispatchOpts) *liveDispatchWorld {
	return &liveDispatchWorld{r: r, pool: pool, opts: opts}
}

func (w *liveDispatchWorld) ReadWorker(name string) (*WorkerState, error) {
	h, err := LoadLiveWorker(w.r, name)
	if err != nil {
		return nil, err
	}
	return h.Status(), nil
}

func (w *liveDispatchWorld) ResetWorker(name string) error {
	h, err := loadWorker(w.r, name)
	if err != nil {
		h, err = CreateIdleWorker(w.r, name)
		if err != nil {
			return err
		}
	}
	return h.Reclaim()
}

func (w *liveDispatchWorld) ClaimWorker(ticket string) (string, error) {
	return w.pool.Claim(ticket)
}

func (w *liveDispatchWorld) LaunchWorker(worker, ticket string, depCtx *DependencyContext) {
	ticketOpts := *w.opts
	ticketOpts.TicketID = ticket
	launchWorker(w.r, worker, &ticketOpts, depCtx)
}

// ── Rendering ──────────────────────────────────────────────────────

func (dg *DispatchGroup) PrintStatus() {
	var ids []string
	for id := range dg.SubIssues {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	fmt.Fprintf(os.Stderr, "\n%s sub-issues:\n\n", dg.Parent)
	fmt.Fprintf(os.Stderr, "%-12s %-12s %-12s %-40s %s\n", "TICKET", "STATUS", "WORKER", "TITLE", "BLOCKED BY")
	fmt.Fprintf(os.Stderr, "%-12s %-12s %-12s %-40s %s\n", "------", "------", "------", "-----", "----------")

	for _, id := range ids {
		si := dg.SubIssues[id]

		worker := "-"
		if si.Worker != nil {
			worker = displayWorker(*si.Worker)
		}

		title := si.Title
		if len(title) > 38 {
			title = title[:35] + "..."
		}

		blockedBy := "-"
		if len(si.BlockedBy) > 0 {
			blockedBy = strings.Join(si.BlockedBy, ", ")
		}

		paddedStatus := fmt.Sprintf("%-12s", si.Status)
		switch si.Status {
		case SubIssueStatusPending:
			paddedStatus = colorize(paddedStatus, colorDim)
		case SubIssueStatusDispatched:
			paddedStatus = colorize(paddedStatus, colorYellow)
		case SubIssueStatusDone:
			paddedStatus = colorize(paddedStatus, colorGreen)
		case SubIssueStatusFailed:
			paddedStatus = colorize(paddedStatus, colorRed)
		case SubIssueStatusSkipped:
			paddedStatus = colorize(paddedStatus, colorDim)
		}

		fmt.Fprintf(os.Stderr, "%-12s %s %-12s %-40s %s\n", id, paddedStatus, worker, title, blockedBy)
	}
	fmt.Fprintln(os.Stderr)
}

// ── Construction ───────────────────────────────────────────────────

func BuildDispatchGroup(r *RepoContext, parent string, opts *DispatchOpts) (*DispatchGroup, error) {
	repo := ghRepo(r)

	info("Building dependency graph for %s...", parent)

	entries, err := linearSubIssueGraph(r, parent, repo)
	if err != nil {
		return nil, err
	}

	if _, ok := entries[parent]; ok {
		info("  Dropping %s from sub-issues (parent cannot be its own child)", parent)
		delete(entries, parent)
	}

	if len(entries) == 0 {
		return nil, nil
	}

	siblingSet := make(map[string]bool, len(entries))
	for id := range entries {
		siblingSet[id] = true
	}
	for id, si := range entries {
		filtered := si.BlockedBy[:0]
		for _, dep := range si.BlockedBy {
			if siblingSet[dep] {
				filtered = append(filtered, dep)
			} else {
				info("  %s: dropping non-sibling blocker %s", id, dep)
			}
		}
		si.BlockedBy = filtered
		entries[id] = si
	}

	dg := &DispatchGroup{
		Parent:    parent,
		CreatedAt: nowUTC(),
		GHRepo:    repo,
		SubIssues: make(map[string]*SubIssueState),
		Opts: DispatchGroupOpts{
			Model: opts.Model,
		},
	}

	for id, si := range entries {
		state := &SubIssueState{
			Title:     si.Title,
			Status:    SubIssueStatusPending,
			BlockedBy: si.BlockedBy,
		}
		if si.CrossRepo {
			state.Status = SubIssueStatusSkipped
			reason := "cross-repo"
			state.SkipReason = &reason
		} else if !isDispatchableStatus(si.Status) {
			state.Status = SubIssueStatusSkipped
			reason := si.Status
			state.SkipReason = &reason
			if isMergedStatus(si.Status) {
				main := "main"
				state.Branch = &main
				info("  %s already %s (in main)", id, si.Status)
			} else {
				state.Branch = resolveExistingBranch(r, id)
				if state.Branch != nil {
					info("  %s already %s (branch: %s)", id, si.Status, *state.Branch)
				} else {
					info("  %s already %s (no branch found)", id, si.Status)
				}
			}
		}
		dg.SubIssues[id] = state
	}

	return dg, nil
}

func isDispatchableStatus(status string) bool {
	s := strings.ToLower(status)
	return s == "backlog" || s == "todo" || s == "triage"
}

func isMergedStatus(status string) bool {
	s := strings.ToLower(status)
	return s == "merged" || s == "done"
}

func resolveExistingBranch(r *RepoContext, ticketID string) *string {
	return jjResolveBranchForTicket(r.Root, strings.ToLower(ticketID))
}

func extractJSON(s string) string {
	start := strings.Index(s, "{")
	if start == -1 {
		return s
	}
	depth := 0
	for i := start; i < len(s); i++ {
		switch s[i] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return s[start : i+1]
			}
		}
	}
	return s[start:]
}

// ── Orchestrated dispatch ──────────────────────────────────────────

func cmdDispatchOrchestrated(r *RepoContext, dg *DispatchGroup, opts *DispatchOpts) {
	p, err := OpenPool(r)
	if err != nil {
		fatal("No pool. Run: wsg pool create --size N")
	}

	maxConc := dg.MaxWaveSize()
	snap := p.Snapshot()
	if snap.Idle < maxConc {
		needed := maxConc - snap.Idle
		newSize := snap.Size + needed
		info("Auto-expanding pool from %d to %d for wave parallelism (%d idle, need %d)", snap.Size, newSize, snap.Idle, maxConc)
		if err := p.Resize(newSize); err != nil {
			fatal("Resize: %v", err)
		}
		info("Pool ready: %d workers available", p.Snapshot().Idle)
	}

	dg.Save(r)
	watchDispatchGroup(r, p, dg, opts)
}

func watchDispatchGroup(r *RepoContext, p *Pool, dg *DispatchGroup, opts *DispatchOpts) {
	world := newLiveDispatchWorld(r, p, opts)
	for {
		if dg.AdvanceOnce(world) {
			dg.Save(r)
		}

		if dg.Terminal() {
			fmt.Fprintln(os.Stderr)
			dg.PrintStatus()
			done, failed, skipped := dg.CountStatuses()
			if failed > 0 {
				info("Orchestration complete: %d done, %d failed, %d skipped", done, failed, skipped)
			} else {
				info("All %d sub-issues done (%d skipped)", done, skipped)
			}
			return
		}

		time.Sleep(5 * time.Second)
	}
}

func ptrOr(p *string, fallback string) string {
	if p != nil && *p != "" {
		return *p
	}
	return fallback
}

// ── Orchestration entry point ─────────────────────────────────────

func tryOrchestrate(r *RepoContext, ticket string, opts *DispatchOpts) {
	if dg := LoadLiveDispatchGroup(r, ticket); dg != nil {
		dg.PrintStatus()
		if dg.Terminal() {
			done, failed, skipped := dg.CountStatuses()
			info("Orchestration complete: %d done, %d failed, %d skipped", done, failed, skipped)
		} else {
			spawnOrchestratorCLI(r, ticket, opts)
		}
		return
	}
	spawnOrchestratorCLI(r, ticket, opts)
}

// ── Background orchestration ───────────────────────────────────────

func spawnOrchestrator(r *RepoContext, parent string, opts *DispatchOpts) error {
	logFile := filepath.Join(r.poolDir(), "dispatch-"+strings.ToLower(parent)+".log")
	_, err := startBackground(r.Root, logFile, "wsg", "__orchestrate", parent,
		"--model", opts.Model)
	return err
}

func spawnOrchestratorCLI(r *RepoContext, parent string, opts *DispatchOpts) {
	if err := spawnOrchestrator(r, parent, opts); err != nil {
		fatal("Failed to start orchestrator: %v", err)
	}
	logFile := filepath.Join(r.poolDir(), "dispatch-"+strings.ToLower(parent)+".log")
	info("Orchestrating %s in background", parent)
	info("  Log: %s", logFile)
	info("  Re-run 'wsg dispatch %s' to check progress", parent)
}

func cmdOrchestrate(args []string) {
	if len(args) == 0 {
		fatal("Usage: wsg __orchestrate <PARENT-TICKET> [--model MODEL]")
	}

	parent := args[0]
	opts := DispatchOpts{
		Model: "opus",
	}
	for i := 1; i < len(args); i++ {
		switch args[i] {
		case "--model":
			if i+1 < len(args) {
				opts.Model = args[i+1]
				i++
			}
		}
	}

	r, err := newRepoContext()
	if err != nil {
		fatal("Not in a jj repo")
	}

	var dg *DispatchGroup
	if existing := LoadLiveDispatchGroup(r, parent); existing != nil {
		dg = existing
	} else {
		dg, err = BuildDispatchGroup(r, parent, &opts)
		if err != nil {
			fatal("Failed to build dependency graph: %v", err)
		}
		if dg == nil {
			// No sub-issues - fall back to single-ticket dispatch
			opts.TicketID = parent
			p, perr := OpenPool(r)
			if perr != nil {
				fatal("No pool. Run: wsg pool create --size N")
			}
			worker, werr := p.Claim(parent)
			if werr != nil {
				newSize := p.Config().Size + 1
				info("Auto-expanding pool to %d for %s", newSize, parent)
				if rerr := p.Resize(newSize); rerr != nil {
					fatal("Resize: %v", rerr)
				}
				worker, werr = p.Claim(parent)
				if werr != nil {
					fatal("No idle workers for %s", parent)
				}
			}
			launchWorker(r, worker, &opts, nil)
			return
		}
	}

	cmdDispatchOrchestrated(r, dg, &opts)
}
