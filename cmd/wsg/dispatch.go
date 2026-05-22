package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

type DispatchOpts struct {
	TicketID      string
	Foreground    bool
	Model         string
	Budget        string
	AllMode       bool
	Label         string
	NoOrchestrate bool
}

func cmdDispatch(args []string) {
	opts := DispatchOpts{
		Model:  "opus",
		Budget: "20",
		Label:  "ready-for-agent",
	}

	var tickets []string
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--fg":
			opts.Foreground = true
		case "--model":
			if i+1 < len(args) {
				opts.Model = args[i+1]
				i++
			}
		case "--budget":
			if i+1 < len(args) {
				opts.Budget = args[i+1]
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

	if opts.AllMode {
		dispatchAll(&opts)
		return
	}

	if len(tickets) == 0 {
		fatal("Usage: wsg dispatch <TICKET>... [--fg] [--model MODEL] [--budget USD]")
	}

	r, err := newRepoContext()
	if err != nil {
		fatal("Not in a jj repo")
	}

	if _, err := loadPoolConfig(r.poolConfigFile()); err != nil {
		fatal("No pool. Run: wsg pool create --size N")
	}

	if len(tickets) == 1 && !opts.NoOrchestrate {
		dgFile := dispatchGroupFile(r, tickets[0])
		if dg, err := loadDispatchGroup(dgFile); err == nil {
			syncGroupFromWorkers(r, dg)
			revalidateBranches(r, dg)
			saveDispatchGroup(dgFile, dg)
			printGroupStatus(dg)
			if isGroupTerminal(dg) {
				done, failed, skipped := countGroupStatuses(dg)
				info("Orchestration complete: %d done, %d failed, %d skipped", done, failed, skipped)
			} else {
				spawnOrchestrator(r, tickets[0], &opts)
			}
			return
		}
		spawnOrchestrator(r, tickets[0], &opts)
		return
	}

	idle := countIdleWorkers(r)
	need := len(tickets)
	if idle < need {
		cfg, _ := loadPoolConfig(r.poolConfigFile())
		newSize := cfg.Size + (need - idle)
		if confirm("Pool has %d idle worker(s) but %d ticket(s) to dispatch. Resize pool to %d?", idle, need, newSize) {
			cmdPoolResize([]string{strconv.Itoa(newSize)})
		}
	}

	dispatched := 0
	for _, tid := range tickets {
		worker, err := findIdleWorker(r)
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

	if _, err := loadPoolConfig(r.poolConfigFile()); err != nil {
		fatal("No pool. Run: wsg pool create --size N")
	}

	info("Fetching tickets with label '%s'...", opts.Label)

	prompt := fmt.Sprintf(
		"Use the Linear MCP list_issues tool to find issues with label '%s' that are in 'Todo' state for the Ameba team. Return ONLY the issue identifiers (e.g. AMBA-42) as a JSON array in this exact format: {\"tickets\": [\"AMBA-1\", \"AMBA-2\"]}",
		opts.Label,
	)

	output, err := run(r.Root, "claude", "-p",
		"--model", "haiku",
		"--max-budget-usd", "0.05",
		"--output-format", "json",
		"--no-session-persistence",
		"--allowedTools=mcp__claude_ai_Linear__list_issues,mcp__claude_ai_Linear__get_issue",
		prompt,
	)
	if err != nil {
		fatal("Failed to fetch tickets: %v", err)
	}

	tickets := parseTicketResponse(output)
	if len(tickets) == 0 {
		info("No tickets found with label '%s'", opts.Label)
		return
	}

	idle := countIdleWorkers(r)
	need := len(tickets)
	if idle < need {
		cfg, _ := loadPoolConfig(r.poolConfigFile())
		newSize := cfg.Size + (need - idle)
		if confirm("Pool has %d idle worker(s) but %d ticket(s) to dispatch. Resize pool to %d?", idle, need, newSize) {
			cmdPoolResize([]string{strconv.Itoa(newSize)})
		}
	}

	count := 0
	for _, tid := range tickets {
		worker, err := findIdleWorker(r)
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
	// Claude --output-format json wraps in {"result": "..."}
	var wrapper struct {
		Result string `json:"result"`
	}
	if err := json.Unmarshal([]byte(output), &wrapper); err == nil && wrapper.Result != "" {
		output = wrapper.Result
	}
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
		jjArgs := append([]string{"new"}, depCtx.BaseBranches...)
		if _, err := run(wspath, "jj", jjArgs...); err != nil {
			fatal("Failed to set %s to base branches %v: %v", worker, depCtx.BaseBranches, err)
		}
	} else {
		if _, err := run(wspath, "jj", "new", "main"); err != nil {
			fatal("Failed to reset %s to main: %v", worker, err)
		}
	}

	poolDir := r.poolDir()
	logFile := filepath.Join(poolDir, worker+".log")
	repo := ghRepo(r)
	ticketLower := strings.ToLower(opts.TicketID)

	sf := r.workerStateFile(worker)
	now := nowUTC()
	ws := &WorkerState{
		Status:     "busy",
		Ticket:     &opts.TicketID,
		StartedAt:  &now,
		LogFile:    &logFile,
		BranchName: &ticketLower,
	}
	saveWorkerState(sf, ws)

	systemPrompt := fmt.Sprintf(`You are an autonomous implementation agent in a jj (Jujutsu VCS) workspace.

CRITICAL RULES:
- Use jj commands, NEVER git commands.
- The gh CLI requires: gh -R %s pr create ...
- Branch naming: adam/%s-<short-description> (lowercase, hyphens, max 4 words from ticket title). Example: adam/amba-42-supplier-contact-sync
- To push your work: jj git push --named <branch>=@
- You have access to Linear MCP tools for fetching ticket details and updating status.
- Do NOT ask questions. Make reasonable decisions and proceed.
- If you encounter ambiguity, document your assumptions in the PR description.`, repo, ticketLower)

	if depCtx != nil && depCtx.Context != "" {
		systemPrompt += fmt.Sprintf(`

STACKED BRANCH: Your workspace is based on prerequisite work:
%s

CRITICAL: Do NOT rebase onto main. Your changes build on top of the prerequisite branch(es).
If you see merge conflict markers, resolve them before proceeding.`, depCtx.Context)
	}

	prCreateCmd := fmt.Sprintf(
		`gh -R %s pr create --head <branch> --title "%s: <title from ticket>" --body "<summary of changes and link to Linear ticket>"`,
		repo, opts.TicketID)
	if depCtx != nil && depCtx.PRBase != "" {
		prCreateCmd = fmt.Sprintf(
			`gh -R %s pr create --head <branch> --base %s --title "%s: <title from ticket>" --body "<summary of changes and link to Linear ticket>"`,
			repo, depCtx.PRBase, opts.TicketID)
	}

	workerPrompt := fmt.Sprintf(`Implement Linear ticket %s.

1. Fetch the ticket: use the Linear MCP get_issue tool with id "%s". Read the full description.
   The "Outcome" section defines your acceptance criteria - verify against it before finishing.

2. Claim the ticket: use the Linear MCP save_issue tool with id "%s" to set state "In Progress" and assignee "adam@ameba.ai".

3. Derive a branch name from the ticket title in the format: adam/%s-<short-description>
   Use lowercase, hyphens, max 4 words from the title. Example: adam/amba-42-supplier-contact-sync

4. Read CLAUDE.md and relevant source files to understand the codebase and conventions.

5. Implement the changes described in the ticket. Follow existing patterns. Write or update tests as specified.

6. Run checks: linting, type checking, and tests. Fix any issues before proceeding.

7. Describe your changes: jj describe -m "%s: <concise summary of what you implemented>"

8. Push: jj git push --named <branch>=@

9. Create a PR:
   %s

10. Update Linear:
    - Use the Linear MCP save_issue tool to move %s to "Reviewable" state
    - Use the Linear MCP save_comment tool to add a comment with: what was implemented, the PR URL, and any assumptions made`,
		opts.TicketID, opts.TicketID, opts.TicketID, ticketLower, opts.TicketID, prCreateCmd, opts.TicketID)

	claudeArgs := []string{
		"-p",
		"--model", opts.Model,
		"--max-budget-usd", opts.Budget,
		"--output-format", "stream-json",
		"--verbose",
		"--name", fmt.Sprintf("pool:%s:%s", worker, opts.TicketID),
		"--append-system-prompt", systemPrompt,
		workerPrompt,
	}

	info("Dispatching %s to %s...", opts.TicketID, worker)

	if opts.Foreground {
		exitCode, err := startForeground(wspath, logFile, "claude", claudeArgs...)
		now := nowUTC()
		if err != nil {
			errMsg := err.Error()
			ws.Status = "failed"
			ws.CompletedAt = &now
			ws.Error = &errMsg
			ec := 1
			ws.ExitCode = &ec
		} else if exitCode == 0 {
			ws.Status = "done"
			ws.CompletedAt = &now
			ws.ExitCode = &exitCode
		} else {
			ws.Status = "failed"
			ws.CompletedAt = &now
			ws.ExitCode = &exitCode
		}
		saveWorkerState(sf, ws)
	} else {
		pid, err := startBackground(wspath, logFile, "claude", claudeArgs...)
		if err != nil {
			errMsg := err.Error()
			now := nowUTC()
			ws.Status = "failed"
			ws.CompletedAt = &now
			ws.Error = &errMsg
			saveWorkerState(sf, ws)
			fatal("Failed to start worker: %v", err)
		}
		ws.PID = &pid
		saveWorkerState(sf, ws)
		info("  %s (PID %d) -> %s", worker, pid, opts.TicketID)

		go func() {
			waitForProcess(pid)
			ws, err := loadWorkerState(sf)
			if err != nil {
				return
			}
			now := nowUTC()
			ws.CompletedAt = &now
			if ws.Status == "busy" {
				ws.Status = "done"
				ec := 0
				ws.ExitCode = &ec
			}
			saveWorkerState(sf, ws)
		}()
	}
}

func waitForProcess(pid int) {
	proc, err := os.FindProcess(pid)
	if err != nil {
		return
	}
	proc.Wait()
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
