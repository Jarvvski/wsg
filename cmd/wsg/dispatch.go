package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type claudeInvocation struct {
	Model        string
	SessionID    string
	Name         string
	SystemPrompt string
	Prompt       string
}

func (c *claudeInvocation) Args() []string {
	args := []string{"-p"}
	if c.Model != "" {
		args = append(args, "--model", c.Model)
	}
	if c.SessionID != "" {
		args = append(args, "--resume", c.SessionID, "--fork-session")
	}
	args = append(args, "--output-format", "stream-json", "--verbose")
	if c.Name != "" {
		args = append(args, "--name", c.Name)
	}
	if c.SystemPrompt != "" && c.SessionID == "" {
		args = append(args, "--append-system-prompt", c.SystemPrompt)
	}
	args = append(args, c.Prompt)
	return args
}

type DispatchOpts struct {
	TicketID      string
	Foreground    bool
	Model         string
	Label         string
	NoOrchestrate bool

	// OnPoolFull, if non-nil, decides whether to grow the pool when a
	// reservation finds fewer idle workers than needed. Returning true grows
	// the pool to fit; false claims only what currently fits (partial). Nil
	// means "always grow" - the TUI default. The CLI plumbs `confirm` here.
	OnPoolFull func(have, need int) bool

	// OrchestrateEach, when true, spawns one orchestrator subprocess per
	// ticket in a batch (TUI batch behavior). When false, batches go through
	// atomic Reserve + direct launch (CLI batch behavior). Ignored when
	// NoOrchestrate is set.
	OrchestrateEach bool
}

func cmdDispatch(args []string) {
	opts := DispatchOpts{
		Model: "opus",
		Label: "ready-for-agent",
	}

	var tickets []string
	var fgFlag *bool
	var allMode bool
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--fg":
			t := true
			fgFlag = &t
		case "--bg":
			f := false
			fgFlag = &f
		case "--model":
			if i+1 < len(args) {
				opts.Model = args[i+1]
				i++
			}
		case "--all":
			allMode = true
		case "--no-orchestrate":
			opts.NoOrchestrate = true
		case "--label":
			if i+1 < len(args) {
				opts.Label = args[i+1]
				i++
			}
		default:
			if !strings.HasPrefix(args[i], "-") {
				tickets = append(tickets, args[i])
			}
		}
	}

	r, err := newRepoContext()
	if err != nil {
		fatal("Not in a jj repo")
	}

	opts.Foreground = resolveForeground(r, fgFlag)
	opts.OnPoolFull = cliConfirmGrow

	actions := NewActions(r)

	if allMode {
		info("Fetching tickets with label '%s'...", opts.Label)
		// --all has historically been bulk-only (no per-ticket orchestrator
		// even if only one ticket matches). Pin that here so changing the
		// verb's defaults can't drift the CLI surface.
		allOpts := opts
		allOpts.NoOrchestrate = true
		fetched, res, err := actions.DispatchAll(allOpts)
		if err != nil {
			fatal("DispatchAll: %v", err)
		}
		if len(fetched) == 0 {
			info("No tickets found with label '%s'", opts.Label)
			return
		}
		renderCLIDispatch(r, res, len(fetched), true)
		return
	}

	if len(tickets) == 0 {
		fatal("Usage: wsg dispatch <TICKET>... [--fg|--bg] [--model MODEL]")
	}

	res, err := actions.Dispatch(tickets, opts)
	if err != nil {
		fatal("Dispatch: %v", err)
	}
	renderCLIDispatch(r, res, len(tickets), false)
}

// cliConfirmGrow is the OnPoolFull callback the CLI plumbs into the verb:
// it asks the user interactively whether to grow the pool to fit. The TUI
// passes nil instead and always grows.
func cliConfirmGrow(have, need int) bool {
	newSize := have + need
	return confirm("Pool has %d idle worker(s) but %d ticket(s) to dispatch. Resize pool to %d?", have, need, newSize)
}

// renderCLIDispatch prints the per-outcome CLI status lines and the
// orchestration tables/summaries. The verb returns data; this is where
// "dispatched X to Y", "Orchestrating ... in background", and the
// PrintStatus tables live.
func renderCLIDispatch(r *RepoContext, res DispatchResult, requested int, all bool) {
	for _, o := range res.Outcomes {
		if o.Orchestrated {
			renderOrchestratedOutcome(r, o)
			continue
		}
		// Foreground launch returns PID 0 after WaitFinal; the original
		// CLI suppressed the line entirely in that case so the user sees
		// claude's terminal output, not a bogus "PID 0" trailer.
		if o.PID == 0 {
			continue
		}
		info("  %s (PID %d) -> %s", o.Worker, o.PID, o.Ticket)
	}
	dispatched := len(res.Outcomes)
	if res.Partial {
		if dispatched == 0 && !all {
			fatal("No idle workers. Run: wsg pool list")
		}
		info("No more idle workers. Dispatched %d/%d ticket(s).", dispatched, requested)
		return
	}
	if all {
		info("Dispatched %d ticket(s)", dispatched)
	}
}

func renderOrchestratedOutcome(r *RepoContext, o TicketOutcome) {
	if o.Group != nil {
		o.Group.PrintStatus()
		if o.Group.Terminal() {
			done, failed, skipped := o.Group.CountStatuses()
			info("Orchestration complete: %d done, %d failed, %d skipped", done, failed, skipped)
			return
		}
	}
	logFile := filepath.Join(r.poolDir(), "dispatch-"+strings.ToLower(o.Ticket)+".log")
	info("Orchestrating %s in background", o.Ticket)
	info("  Log: %s", logFile)
	info("  Re-run 'wsg dispatch %s' to check progress", o.Ticket)
}

// intentFromOpts builds a DispatchIntent from CLI-parsed dispatch options
// and the per-call dependency context. The CLI's opts.TicketID is the
// ticket for this call; depCtx is non-nil only for orchestrated stacked
// dispatches.
func intentFromOpts(opts *DispatchOpts, depCtx *DependencyContext) DispatchIntent {
	return DispatchIntent{
		Ticket:     opts.TicketID,
		Model:      opts.Model,
		DepCtx:     depCtx,
		Foreground: opts.Foreground,
	}
}

// launchWorker loads the handle for worker and hands intent to
// WorkerHandle.Dispatch. Returns the launched PID for caller-side
// logging. No prints, no fatal - the CLI shell and the action both call
// this and decide how to render success or surface errors.
func launchWorker(r *RepoContext, worker string, intent DispatchIntent) (int, error) {
	h, err := loadWorker(r, worker)
	if err != nil {
		return 0, fmt.Errorf("open worker state: %w", err)
	}
	pid, err := h.Dispatch(intent)
	if err != nil {
		return 0, fmt.Errorf("start worker: %w", err)
	}
	return pid, nil
}

func cmdLogs(args []string) {
	if len(args) == 0 {
		fatal("Usage: wsg logs <worker>")
	}
	worker := resolveWorker(args[0])

	r, err := newRepoContext()
	if err != nil {
		fatal("Not in a jj repo")
	}

	sf := r.workerStateFile(worker)
	ws, err := loadWorkerState(sf)
	if err != nil {
		fatal("Worker %s not found", worker)
	}

	if ws.LogFile == nil || *ws.LogFile == "" {
		fatal("No log file for %s", worker)
	}
	if _, err := os.Stat(*ws.LogFile); os.IsNotExist(err) {
		fatal("No log file for %s", worker)
	}

	streamLogs(*ws.LogFile)
}
