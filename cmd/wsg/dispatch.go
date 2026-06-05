package main

import (
	"encoding/json"
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

	snap := p.Snapshot()
	need := len(tickets)
	if snap.Idle < need {
		newSize := snap.Size + (need - snap.Idle)
		if confirm("Pool has %d idle worker(s) but %d ticket(s) to dispatch. Resize pool to %d?", snap.Idle, need, newSize) {
			if err := p.Resize(newSize); err != nil {
				fatal("Resize: %v", err)
			}
		}
	}

	dispatched := 0
	for _, tid := range tickets {
		worker, err := p.Claim(tid)
		if err != nil {
			if dispatched > 0 {
				info("No more idle workers. Dispatched %d/%d ticket(s).", dispatched, need)
			} else {
				fatal("No idle workers. Run: wsg pool list")
			}
			return
		}
		opts.TicketID = tid
		launchWorker(r, worker, &opts, nil)
		dispatched++
	}
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

	prompt := fmt.Sprintf(
		"Use the Linear MCP list_issues tool to find issues with label '%s' that are in 'Todo' state for the Ameba team. Return ONLY the issue identifiers (e.g. AMBA-42) as a JSON array in this exact format: {\"tickets\": [\"AMBA-1\", \"AMBA-2\"]}",
		opts.Label,
	)

	output, err := claudeQuery(r.Root, prompt,
		"mcp__claude_ai_Linear__list_issues,mcp__claude_ai_Linear__get_issue")
	if err != nil {
		fatal("Failed to fetch tickets: %v", err)
	}

	tickets := parseTicketResponse(output)
	if len(tickets) == 0 {
		info("No tickets found with label '%s'", opts.Label)
		return
	}

	snap := p.Snapshot()
	need := len(tickets)
	if snap.Idle < need {
		newSize := snap.Size + (need - snap.Idle)
		if confirm("Pool has %d idle worker(s) but %d ticket(s) to dispatch. Resize pool to %d?", snap.Idle, need, newSize) {
			if err := p.Resize(newSize); err != nil {
				fatal("Resize: %v", err)
			}
		}
	}

	count := 0
	for _, tid := range tickets {
		worker, err := p.Claim(tid)
		if err != nil {
			info("No more idle workers. Dispatched %d/%d ticket(s).", count, need)
			return
		}
		ticketOpts := *opts
		ticketOpts.TicketID = tid
		launchWorker(r, worker, &ticketOpts, nil)
		count++
	}
	info("Dispatched %d ticket(s)", count)
}

func parseTicketResponse(output string) []string {
	var payload struct {
		Tickets []string `json:"tickets"`
	}
	if err := json.Unmarshal([]byte(output), &payload); err != nil {
		return nil
	}
	return payload.Tickets
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

	h, err := OpenWorker(r.workerStateFile(worker))
	if err != nil {
		fatal("Failed to open worker state: %v", err)
	}

	systemPrompt := buildDispatchSystemPrompt(repo, branchPrefix, ticketLower, depCtx)
	prCreateCmd := buildPRCreateCmd(repo, opts.TicketID, depCtx)
	workerPrompt := buildDispatchWorkerPrompt(opts.TicketID, userEmail, branchPrefix, ticketLower, prCreateCmd)

	inv := claudeInvocation{
		Model:        opts.Model,
		Name:         fmt.Sprintf("pool:%s:%s", worker, opts.TicketID),
		SystemPrompt: systemPrompt,
		Prompt:       workerPrompt,
	}

	info("Dispatching %s to %s...", opts.TicketID, worker)

	pid, err := h.Launch(r, worker, inv, opts.Foreground)
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
