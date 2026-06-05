package main

import (
	"errors"
	"fmt"
	"os"
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
	AllMode       bool
	Label         string
	NoOrchestrate bool
}

func cmdDispatch(args []string) {
	opts := DispatchOpts{
		Model: "opus",
		Label: "ready-for-agent",
	}

	var tickets []string
	var fgFlag *bool
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
			opts.AllMode = true
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

	if opts.AllMode {
		dispatchAll(&opts)
		return
	}

	if len(tickets) == 0 {
		fatal("Usage: wsg dispatch <TICKET>... [--fg|--bg] [--model MODEL]")
	}

	p, err := OpenPool(r)
	if err != nil {
		fatal("No pool. Run: wsg pool create --size N")
	}

	if len(tickets) == 1 && !opts.NoOrchestrate {
		tryOrchestrate(r, tickets[0], &opts)
		return
	}

	workers, err := p.Reserve(tickets)
	var pf *PoolFull
	if errors.As(err, &pf) {
		newSize := p.Config().Size + pf.Gap()
		if confirm("Pool has %d idle worker(s) but %d ticket(s) to dispatch. Resize pool to %d?", pf.Have, pf.Need, newSize) {
			workers, err = p.GrowAndReserve(tickets)
		} else {
			claimPartial(r, p, tickets, &opts, true)
			return
		}
	}
	if err != nil {
		fatal("Reserve: %v", err)
	}
	for i, worker := range workers {
		opts.TicketID = tickets[i]
		launchWorker(r, worker, &opts, nil)
	}
}

// claimPartial walks tickets and claims one worker per ticket through
// the locked Pool, stopping at the first shortage. Used when the user
// declines an upfront resize: we still try to dispatch as many tickets
// as currently fit. fatalOnZero is true for the interactive dispatch
// path (no idle = fatal) and false for the batch path where partial
// progress is the norm.
func claimPartial(r *RepoContext, p *Pool, tickets []string, opts *DispatchOpts, fatalOnZero bool) int {
	dispatched := 0
	for _, tid := range tickets {
		worker, err := p.Claim(tid)
		if err != nil {
			if dispatched == 0 && fatalOnZero {
				fatal("No idle workers. Run: wsg pool list")
			}
			info("No more idle workers. Dispatched %d/%d ticket(s).", dispatched, len(tickets))
			return dispatched
		}
		ticketOpts := *opts
		ticketOpts.TicketID = tid
		launchWorker(r, worker, &ticketOpts, nil)
		dispatched++
	}
	return dispatched
}

func dispatchAll(opts *DispatchOpts) {
	r, err := newRepoContext()
	if err != nil {
		fatal("Not in a jj repo")
	}

	p, err := OpenPool(r)
	if err != nil {
		fatal("No pool. Run: wsg pool create --size N")
	}

	info("Fetching tickets with label '%s'...", opts.Label)

	tickets, err := linearReadyTickets(r, opts.Label)
	if err != nil {
		fatal("Failed to fetch tickets: %v", err)
	}

	if len(tickets) == 0 {
		info("No tickets found with label '%s'", opts.Label)
		return
	}

	workers, err := p.Reserve(tickets)
	var pf *PoolFull
	if errors.As(err, &pf) {
		newSize := p.Config().Size + pf.Gap()
		if confirm("Pool has %d idle worker(s) but %d ticket(s) to dispatch. Resize pool to %d?", pf.Have, pf.Need, newSize) {
			workers, err = p.GrowAndReserve(tickets)
		} else {
			count := claimPartial(r, p, tickets, opts, false)
			info("Dispatched %d ticket(s)", count)
			return
		}
	}
	if err != nil {
		fatal("Reserve: %v", err)
	}
	for i, worker := range workers {
		ticketOpts := *opts
		ticketOpts.TicketID = tickets[i]
		launchWorker(r, worker, &ticketOpts, nil)
	}
	info("Dispatched %d ticket(s)", len(workers))
}

func launchWorker(r *RepoContext, worker string, opts *DispatchOpts, depCtx *DependencyContext) {
	wspath := r.workerDir(worker)

	if depCtx != nil && len(depCtx.BaseBranches) > 0 {
		if err := jjNewOn(wspath, depCtx.BaseBranches...); err != nil {
			fatal("Failed to set %s to base branches %v: %v", worker, depCtx.BaseBranches, err)
		}
	} else {
		if err := jjNewOn(wspath, "main"); err != nil {
			fatal("Failed to reset %s to main: %v", worker, err)
		}
	}

	repo := ghRepo(r)
	ticketLower := strings.ToLower(opts.TicketID)

	userEmail, err := jjConfigGet(r.Root, "user.email")
	if err != nil {
		fatal("Cannot read jj user.email: %v", err)
	}
	userName, err := jjConfigGet(r.Root, "user.name")
	if err != nil {
		fatal("Cannot read jj user.name: %v", err)
	}
	branchPrefix := strings.ToLower(strings.Fields(userName)[0])

	h, err := loadWorker(r, worker)
	if err != nil {
		fatal("Failed to open worker state: %v", err)
	}

	systemPrompt := buildDispatchSystemPrompt(repo, branchPrefix, ticketLower, depCtx)
	prCreateCmd := ghPRCreateCmd(repo, opts.TicketID, depCtx)
	workerPrompt := buildDispatchWorkerPrompt(opts.TicketID, userEmail, branchPrefix, ticketLower, prCreateCmd)

	inv := claudeInvocation{
		Model:        opts.Model,
		Name:         fmt.Sprintf("pool:%s:%s", worker, opts.TicketID),
		SystemPrompt: systemPrompt,
		Prompt:       workerPrompt,
	}

	info("Dispatching %s to %s...", opts.TicketID, worker)

	pid, err := h.Dispatch(inv, opts.Foreground)
	if err != nil {
		fatal("Failed to start worker: %v", err)
	}
	if !opts.Foreground {
		info("  %s (PID %d) -> %s", worker, pid, opts.TicketID)
	}
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
