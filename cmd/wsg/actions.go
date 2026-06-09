package main

import (
	"errors"
	"fmt"
)

// WorkerActions is the verb layer the CLI's cmd* functions and the TUI's
// tea.Cmd closures share. Each method takes the worker name as the unit of
// addressing and either mutates the worker state file or launches claude
// against it. The CLI parses os.Args then calls an action; the TUI captures
// the action inside a tea.Cmd closure and translates the result into a
// *ResultMsg.
//
// Foreground/background is a per-call parameter on actions that launch a
// process. The CLI plumbs it through resolveForeground at the parse edge;
// the TUI always runs background. Neither path looks at config inside the
// action - actions trust what they are given.
type WorkerActions struct {
	repo *RepoContext
}

func NewActions(r *RepoContext) *WorkerActions {
	return &WorkerActions{repo: r}
}

// Send resumes worker on prompt, appending the send system prompt for fresh
// sessions. Returns the ResumeOutcome so the caller can render whether the
// existing session was continued or a fresh one was started.
func (a *WorkerActions) Send(worker, prompt string, fg bool) (ResumeOutcome, error) {
	return resumeWorker(a.repo, worker, resumeOpts{
		Prompt:       prompt,
		SystemPrompt: sendSystemPrompt(ghRepo(a.repo)),
		Foreground:   fg,
	})
}

// Review builds a review prompt from the worker's PR (failing checks +
// merge state) and resumes the worker on it. Errors if the worker has no
// branch, no PR for that branch, or gh cannot be reached.
func (a *WorkerActions) Review(worker string, fg bool) (ResumeOutcome, error) {
	prompt, err := buildWorkerReviewPrompt(a.repo, worker)
	if err != nil {
		return ResumeOutcome{}, err
	}
	return resumeWorker(a.repo, worker, resumeOpts{
		Prompt:     prompt,
		Foreground: fg,
	})
}

// Reset returns a worker to idle: kills any live PID, clears state, and
// fires an async `jj restore && jj new main` in the workspace. Same as
// the [K]ill verb in the TUI.
func (a *WorkerActions) Reset(worker string) error {
	h, err := loadWorker(a.repo, worker)
	if err != nil {
		return err
	}
	return h.Reclaim()
}

// OpenPR opens the GitHub PR for the worker's branch in a browser.
func (a *WorkerActions) OpenPR(worker string) error {
	h, err := loadWorker(a.repo, worker)
	if err != nil {
		return err
	}
	if h.Status().Branch == "" {
		return fmt.Errorf("worker %s has no branch", worker)
	}
	repo := ghRepo(a.repo)
	if repo == "" {
		return fmt.Errorf("cannot detect GitHub repo")
	}
	branch := h.Branch()
	if err := ghOpenInBrowser(repo, branch); err != nil {
		return fmt.Errorf("no PR for branch %s", branch)
	}
	return nil
}

// Rebase rebases the worker's branch onto main and pushes. On conflict the
// rebase is undone via `jj op undo` and an error is returned instructing
// the caller to review instead.
func (a *WorkerActions) Rebase(worker string) error {
	h, err := loadWorker(a.repo, worker)
	if err != nil {
		return err
	}
	if h.Status().Branch == "" {
		return fmt.Errorf("worker %s has no branch", worker)
	}
	branch := h.Branch()
	wspath := a.repo.workerDir(worker)
	if err := jjRebase(wspath, branch, "main"); err != nil {
		return err
	}
	if err := jjPush(wspath, branch); err != nil {
		jjOpUndo(wspath)
		return fmt.Errorf("rebase caused conflicts, reverted - use [r]eview instead")
	}
	return nil
}

// DispatchResult is the verb's return shape. The caller renders Outcomes
// (one entry per dispatched ticket) and may consult Partial to surface
// "dispatched N of M" when the pool was full and the caller declined to
// grow it.
type DispatchResult struct {
	Outcomes []TicketOutcome
	Partial  bool
}

// TicketOutcome describes what happened for a single ticket. The two paths
// are mutually exclusive: Orchestrated tickets carry a Group (existing
// state, possibly terminal) and no Worker/PID; bulk-launched tickets carry
// Worker/PID and no Group.
type TicketOutcome struct {
	Ticket        string
	Worker        string
	PID           int
	Orchestrated  bool
	Group         *DispatchGroup
}

// Dispatch is the shared dispatch verb for CLI and TUI. A single ticket
// (without NoOrchestrate) goes through the orchestrator so sub-issue DAGs
// are handled; multi-ticket batches go through atomic Reserve + direct
// launch unless OrchestrateEach is set (the TUI's per-ticket-orchestrator
// pattern). Pool-full is resolved inline via opts.OnPoolFull.
//
// The action never prints or exits - it returns enough data for the
// caller to render. CLI shells print info lines and fatal on error;
// the TUI translates outcomes into a *ResultMsg.
func (a *WorkerActions) Dispatch(tickets []string, opts DispatchOpts) (DispatchResult, error) {
	if len(tickets) == 0 {
		return DispatchResult{}, errors.New("no tickets to dispatch")
	}
	orchestrate := !opts.NoOrchestrate && (len(tickets) == 1 || opts.OrchestrateEach)
	if orchestrate {
		return a.dispatchOrchestrateEach(tickets, opts)
	}
	return a.dispatchBulk(tickets, opts)
}

// DispatchAll fetches ready tickets from Linear with opts.Label and routes
// them through Dispatch. Returns the fetched ticket IDs alongside the result
// so callers can render "found N" before checking how many actually went
// out. Returns (nil, zero, nil) when Linear has no matching tickets.
// OrchestrateEach is honored: CLI --all leaves it false (atomic bulk path),
// the TUI N-key sets it true (one orchestrator per ticket).
func (a *WorkerActions) DispatchAll(opts DispatchOpts) ([]string, DispatchResult, error) {
	tickets, err := linearReadyTickets(a.repo, opts.Label)
	if err != nil {
		return nil, DispatchResult{}, err
	}
	if len(tickets) == 0 {
		return nil, DispatchResult{}, nil
	}
	res, err := a.Dispatch(tickets, opts)
	return tickets, res, err
}

// dispatchOrchestrateEach pre-grows the pool to fit (so concurrent
// orchestrators each find an idle slot), then spawns one orchestrator
// per ticket. An existing terminal group is reported as-is without a
// re-spawn; non-terminal or missing groups trigger a fresh orchestrator.
func (a *WorkerActions) dispatchOrchestrateEach(tickets []string, opts DispatchOpts) (DispatchResult, error) {
	if err := a.preGrowForOrchestrate(tickets, opts); err != nil {
		return DispatchResult{}, err
	}
	res := DispatchResult{Outcomes: make([]TicketOutcome, 0, len(tickets))}
	for _, t := range tickets {
		out := TicketOutcome{Ticket: t, Orchestrated: true}
		dg := LoadLiveDispatchGroup(a.repo, t)
		spawn := true
		if dg != nil {
			out.Group = dg
			spawn = !dg.Terminal()
		}
		if spawn {
			if err := spawnOrchestrator(a.repo, t, &opts); err != nil {
				return res, fmt.Errorf("orchestrate %s: %w", t, err)
			}
		}
		res.Outcomes = append(res.Outcomes, out)
	}
	return res, nil
}

// preGrowForOrchestrate sizes the pool so each non-terminal ticket has an
// idle worker waiting. The pool lock serialises Resize against Reserve, so
// this is for UX (each orchestrator finds a slot on its first tick) not
// correctness. OnPoolFull governs whether to grow at all; declined growth
// just means the orchestrators will see the existing capacity.
func (a *WorkerActions) preGrowForOrchestrate(tickets []string, opts DispatchOpts) error {
	p, err := OpenPool(a.repo)
	if err != nil {
		return err
	}
	need := 0
	for _, t := range tickets {
		if dg := LoadLiveDispatchGroup(a.repo, t); dg != nil && dg.Terminal() {
			continue
		}
		need++
	}
	if need == 0 {
		return nil
	}
	snap := p.Snapshot()
	if snap.Idle >= need {
		return nil
	}
	grow := true
	if opts.OnPoolFull != nil {
		grow = opts.OnPoolFull(snap.Idle, need)
	}
	if !grow {
		return nil
	}
	newSize := snap.Size + (need - snap.Idle)
	return p.Resize(newSize)
}

// dispatchBulk handles the "atomic Reserve + launch each" path. On
// PoolFull the OnPoolFull callback decides: grow (default) or claim only
// what currently fits (Partial=true). Errors during launch surface to the
// caller with the partial Outcomes intact so the shell can render what
// did get out.
func (a *WorkerActions) dispatchBulk(tickets []string, opts DispatchOpts) (DispatchResult, error) {
	p, err := OpenPool(a.repo)
	if err != nil {
		return DispatchResult{}, fmt.Errorf("no pool: %w", err)
	}
	workers, err := p.Reserve(tickets)
	var pf *PoolFull
	if errors.As(err, &pf) {
		grow := true
		if opts.OnPoolFull != nil {
			grow = opts.OnPoolFull(pf.Have, pf.Need)
		}
		if grow {
			workers, err = p.GrowAndReserve(tickets)
		} else {
			return a.claimPartial(p, tickets, opts)
		}
	}
	if err != nil {
		return DispatchResult{}, err
	}
	return a.launchAll(workers, tickets, opts)
}

func (a *WorkerActions) launchAll(workers, tickets []string, opts DispatchOpts) (DispatchResult, error) {
	res := DispatchResult{Outcomes: make([]TicketOutcome, 0, len(workers))}
	for i, worker := range workers {
		ticketOpts := opts
		ticketOpts.TicketID = tickets[i]
		pid, err := launchWorker(a.repo, worker, intentFromOpts(&ticketOpts, nil))
		if err != nil {
			return res, fmt.Errorf("launch %s: %w", tickets[i], err)
		}
		res.Outcomes = append(res.Outcomes, TicketOutcome{Ticket: tickets[i], Worker: worker, PID: pid})
	}
	return res, nil
}

// claimPartial walks tickets and Claims one worker at a time, stopping at
// the first shortage. Used when OnPoolFull returns false: we still try to
// dispatch what currently fits. Marks Partial=true; the caller can compare
// len(Outcomes) to the original request count for "dispatched N of M".
func (a *WorkerActions) claimPartial(p *Pool, tickets []string, opts DispatchOpts) (DispatchResult, error) {
	res := DispatchResult{Partial: true, Outcomes: make([]TicketOutcome, 0, len(tickets))}
	for _, tid := range tickets {
		worker, err := p.Claim(tid)
		if err != nil {
			return res, nil
		}
		ticketOpts := opts
		ticketOpts.TicketID = tid
		pid, err := launchWorker(a.repo, worker, intentFromOpts(&ticketOpts, nil))
		if err != nil {
			return res, fmt.Errorf("launch %s: %w", tid, err)
		}
		res.Outcomes = append(res.Outcomes, TicketOutcome{Ticket: tid, Worker: worker, PID: pid})
	}
	return res, nil
}

// Dismiss removes worker from the pool when idle, or resets it to idle
// when in a terminal state (done/failed). Errors on busy. Returns the new
// pool size when the worker was removed, or -1 if the worker was only
// reset (size unchanged). Liveness is reconciled first so a dead-busy
// worker can be dismissed without first running `wsg pool reset`.
func (a *WorkerActions) Dismiss(worker string) (int, error) {
	h, err := LoadLiveWorker(a.repo, worker)
	if err != nil {
		return -1, err
	}
	switch h.Status().Status {
	case WorkerStatusBusy:
		return -1, fmt.Errorf("worker %s is busy", worker)
	case WorkerStatusIdle:
		p, err := OpenPool(a.repo)
		if err != nil {
			return -1, err
		}
		return p.Remove(worker)
	default:
		if err := h.reset(); err != nil {
			return -1, err
		}
		return -1, nil
	}
}
