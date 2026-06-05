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
	Title        string   `json:"title"`
	Status       string   `json:"status"`
	BlockedBy    []string `json:"blocked_by"`
	Worker       *string  `json:"worker"`
	Branch       *string  `json:"branch"`
	DispatchedAt *string  `json:"dispatched_at"`
	CompletedAt  *string  `json:"completed_at"`
	SkipReason   *string  `json:"skip_reason,omitempty"`
	Retries      int      `json:"retries"`
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
	dg.SyncFromWorkers(r)
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
		if si.Status != "pending" {
			continue
		}
		allMet := true
		for _, dep := range si.BlockedBy {
			depState, ok := dg.SubIssues[dep]
			if !ok {
				continue
			}
			if depState.Status != "done" && depState.Status != "skipped" {
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
		if si.Status == "skipped" {
			resolved[id] = true
		}
	}

	maxSize := 0
	for {
		var wave []string
		for id, si := range dg.SubIssues {
			if resolved[id] || si.Status == "skipped" {
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
		if si.Status == "pending" || si.Status == "dispatched" {
			return false
		}
	}
	return true
}

func (dg *DispatchGroup) CountStatuses() (done, failed, skipped int) {
	for _, si := range dg.SubIssues {
		switch si.Status {
		case "done":
			done++
		case "failed":
			failed++
		case "skipped":
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
	si.Status = "dispatched"
	si.Worker = &worker
	now := nowUTC()
	si.DispatchedAt = &now
}

// ── World-touching ops ─────────────────────────────────────────────

func (dg *DispatchGroup) SyncFromWorkers(r *RepoContext) bool {
	changed := false
	for id, si := range dg.SubIssues {
		if si.Status != "dispatched" || si.Worker == nil {
			continue
		}
		worker := *si.Worker
		h, err := LoadLiveWorker(r, worker)
		if err != nil {
			continue
		}
		ws := h.State()
		switch ws.Status {
		case "done":
			si.Status = "done"
			now := nowUTC()
			si.CompletedAt = &now
			if ws.BranchName != nil {
				si.Branch = ws.BranchName
			}
			changed = true
			info("  %s completed (branch: %s)", colorize(id, colorGreen), ptrOr(si.Branch, "?"))
			resetWorkerForReuse(r, worker)
		case "failed":
			if si.Retries < 1 {
				si.Retries++
				si.Status = "pending"
				si.Worker = nil
				si.DispatchedAt = nil
				changed = true
				info("  %s failed, will auto-retry (attempt %d)", colorize(id, colorYellow), si.Retries+1)
				resetWorkerForReuse(r, worker)
			} else {
				si.Status = "failed"
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

func (dg *DispatchGroup) RevalidateBranches(r *RepoContext) {
	for id, si := range dg.SubIssues {
		if si.Branch == nil || *si.Branch == "main" {
			continue
		}
		output, err := run(r.Root, "jj", "log", "-r", *si.Branch, "--no-graph", "-T", `"ok"`, "--limit", "1")
		if err != nil || !strings.Contains(output, "ok") {
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

func resetWorkerForReuse(r *RepoContext, worker string) {
	h, err := OpenWorker(r.workerStateFile(worker))
	if err != nil {
		h, _ = CreateIdleWorker(r.workerStateFile(worker))
	}
	if h != nil {
		h.Reclaim(r, worker)
	}
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
		case "pending":
			paddedStatus = colorize(paddedStatus, colorDim)
		case "dispatched":
			paddedStatus = colorize(paddedStatus, colorYellow)
		case "done":
			paddedStatus = colorize(paddedStatus, colorGreen)
		case "failed":
			paddedStatus = colorize(paddedStatus, colorRed)
		case "skipped":
			paddedStatus = colorize(paddedStatus, colorDim)
		}

		fmt.Fprintf(os.Stderr, "%-12s %s %-12s %-40s %s\n", id, paddedStatus, worker, title, blockedBy)
	}
	fmt.Fprintln(os.Stderr)
}

// ── Construction ───────────────────────────────────────────────────

func BuildDispatchGroup(r *RepoContext, parent string, opts *DispatchOpts) (*DispatchGroup, error) {
	repo := ghRepo(r)

	prompt := fmt.Sprintf(`Fetch the parent-child sub-issue graph for Linear issue %s.

Steps:
1. Call list_issues with parentId="%s" to enumerate the DIRECT CHILDREN of %s via the parent-child relationship.
2. If step 1 returns zero issues, respond with exactly: {"sub_issues": {}} and stop. Do not call any other tools.
3. Otherwise, for each child returned in step 1, call get_issue with includeRelations=true to read its blockedBy relations.
4. Return ONLY a JSON object (no markdown, no explanation) in this format:

{
  "sub_issues": {
    "AMBA-17": {
      "title": "Short title from issue",
      "status": "Backlog",
      "blocked_by": [],
      "cross_repo": false
    },
    "AMBA-18": {
      "title": "Short title from issue",
      "status": "In Progress",
      "blocked_by": ["AMBA-17"],
      "cross_repo": false
    }
  }
}

CRITICAL constraints (read carefully):
- Only include issues whose parent is %s (i.e. issues returned by step 1's list_issues call).
- Do NOT include %s itself in sub_issues.
- Do NOT include issues from %s's own blocks / blockedBy / relatedTo relations. Those are siblings or cousins of %s, not its children. A common mistake is to call get_issue on %s and treat its "blocks" list as sub-issues - never do that.
- blocked_by must contain ONLY sibling IDs that also appear as keys in sub_issues. Drop any blockedBy entry that is not a child of %s.
- status is the exact Linear status name (e.g. "Backlog", "Todo", "Planned", "In Progress", "In Review", "Done").
- cross_repo is true if the sub-issue targets a different codebase than %s (look for repo/service names in the title or description).
- Include ALL children from step 1 even if they have no blockers.`, parent, parent, parent, parent, parent, parent, parent, parent, parent, repo)

	info("Building dependency graph for %s...", parent)

	output, err := claudeQuery(r.Root, prompt,
		"mcp__claude_ai_Linear__list_issues,mcp__claude_ai_Linear__get_issue")
	if err != nil {
		return nil, err
	}

	var graphResp struct {
		SubIssues map[string]struct {
			Title     string   `json:"title"`
			Status    string   `json:"status"`
			BlockedBy []string `json:"blocked_by"`
			CrossRepo bool     `json:"cross_repo"`
		} `json:"sub_issues"`
	}
	if err := json.Unmarshal([]byte(output), &graphResp); err != nil {
		return nil, fmt.Errorf("failed to parse dependency graph: %v\nraw: %s", err, output)
	}

	if _, ok := graphResp.SubIssues[parent]; ok {
		info("  Dropping %s from sub-issues (parent cannot be its own child)", parent)
		delete(graphResp.SubIssues, parent)
	}

	if len(graphResp.SubIssues) == 0 {
		return nil, nil
	}

	siblingSet := make(map[string]bool, len(graphResp.SubIssues))
	for id := range graphResp.SubIssues {
		siblingSet[id] = true
	}
	for id, si := range graphResp.SubIssues {
		filtered := si.BlockedBy[:0]
		for _, dep := range si.BlockedBy {
			if siblingSet[dep] {
				filtered = append(filtered, dep)
			} else {
				info("  %s: dropping non-sibling blocker %s", id, dep)
			}
		}
		si.BlockedBy = filtered
		graphResp.SubIssues[id] = si
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

	for id, si := range graphResp.SubIssues {
		state := &SubIssueState{
			Title:     si.Title,
			Status:    "pending",
			BlockedBy: si.BlockedBy,
		}
		if si.CrossRepo {
			state.Status = "skipped"
			reason := "cross-repo"
			state.SkipReason = &reason
		} else if !isDispatchableStatus(si.Status) {
			state.Status = "skipped"
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
	prefix := "adam/" + strings.ToLower(ticketID) + "-"
	output, err := run(r.Root, "jj", "bookmark", "list", "--template", `name ++ "\n"`)
	if err != nil {
		return nil
	}
	for _, line := range strings.Split(output, "\n") {
		if strings.HasPrefix(line, prefix) {
			return &line
		}
	}
	return nil
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
	for {
		if dg.SyncFromWorkers(r) {
			dg.Save(r)
		}

		for _, tid := range dg.Ready() {
			worker, err := p.Claim(tid)
			if err != nil {
				info("No idle workers for %s, will retry next cycle", tid)
				continue
			}

			ticketOpts := *opts
			ticketOpts.TicketID = tid
			launchWorker(r, worker, &ticketOpts, dg.DepContextFor(tid))

			dg.MarkDispatched(tid, worker)
		}

		dg.Save(r)

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
