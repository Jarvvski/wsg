package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func extractSessionID(logFile string) (string, error) {
	f, err := os.Open(logFile)
	if err != nil {
		return "", err
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		var ev struct {
			Type      string `json:"type"`
			Subtype   string `json:"subtype"`
			SessionID string `json:"session_id"`
		}
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			continue
		}
		if ev.Type == "system" && ev.Subtype == "init" && ev.SessionID != "" {
			return ev.SessionID, nil
		}
	}
	return "", fmt.Errorf("no session ID found in log")
}

func cmdSend(args []string) {
	if len(args) < 2 {
		fatal("Usage: wsg send <worker> \"<prompt>\" [--fg] [--budget USD]")
	}

	var worker, prompt string
	fg := false
	budget := "5"

	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--fg":
			fg = true
		case "--budget":
			if i+1 < len(args) {
				budget = args[i+1]
				i++
			}
		default:
			if worker == "" {
				worker = args[i]
			} else if prompt == "" {
				prompt = args[i]
			}
		}
	}

	if worker == "" || prompt == "" {
		fatal("Usage: wsg send <worker> \"<prompt>\" [--fg] [--budget USD]")
	}

	r, err := newRepoContext()
	if err != nil {
		fatal("Not in a jj repo")
	}

	worker = resolveWorker(worker)
	sf := r.workerStateFile(worker)
	ws, err := loadWorkerState(sf)
	if err != nil {
		fatal("Worker %s not found", displayWorker(worker))
	}

	if ws.Status == "busy" {
		fatal("Worker %s is busy. Use 'wsg logs %s' to watch progress.", displayWorker(worker), displayWorker(worker))
	}

	wspath := r.workerDir(worker)
	poolDir := r.poolDir()
	logFile := filepath.Join(poolDir, worker+".log")

	sessionID := ""
	if ws.LogFile != nil && *ws.LogFile != "" {
		if sid, err := extractSessionID(*ws.LogFile); err == nil {
			sessionID = sid
		}
	}

	ws.MarkResumed(logFile)
	saveWorkerState(sf, ws)

	inv := claudeInvocation{
		Budget:    budget,
		SessionID: sessionID,
		Prompt:    prompt,
	}
	if sessionID != "" {
		info("Sending to %s (session %s)...", worker, sessionID[:8])
	} else {
		inv.SystemPrompt = sendSystemPrompt(ghRepo(r))
		info("Starting fresh session for %s...", worker)
	}
	fullArgs := append([]string{"claude"}, inv.Args()...)
	if fg {
		runClaudeFG(wspath, logFile, sf, ws, fullArgs)
	} else {
		pid, err := runClaudeBG(wspath, logFile, sf, ws, fullArgs)
		if err != nil {
			fatal("Failed to start: %v", err)
		}
		info("  %s (PID %d) -> %s", worker, pid, prompt[:min(len(prompt), 60)])
	}
}

func cmdReview(args []string) {
	if len(args) == 0 {
		fatal("Usage: wsg review <worker> [--fg] [--budget USD]")
	}

	var worker string
	fg := false
	budget := "5"

	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--fg":
			fg = true
		case "--budget":
			if i+1 < len(args) {
				budget = args[i+1]
				i++
			}
		default:
			if worker == "" {
				worker = args[i]
			}
		}
	}

	if worker == "" {
		fatal("Usage: wsg review <worker> [--fg] [--budget USD]")
	}

	r, err := newRepoContext()
	if err != nil {
		fatal("Not in a jj repo")
	}

	worker = resolveWorker(worker)
	sf := r.workerStateFile(worker)
	ws, err := loadWorkerState(sf)
	if err != nil {
		fatal("Worker %s not found", displayWorker(worker))
	}

	if ws.Status == "busy" {
		fatal("Worker %s is busy. Use 'wsg logs %s' to watch progress.", displayWorker(worker), displayWorker(worker))
	}

	if ws.BranchName == nil || *ws.BranchName == "" {
		fatal("Worker %s has no branch - has it run a dispatch?", worker)
	}

	if !strings.Contains(*ws.BranchName, "-") || !strings.HasPrefix(*ws.BranchName, "adam/") {
		resolveWorkerBranch(r, worker, ws)
		saveWorkerState(sf, ws)
	}

	if ws.LogFile == nil || *ws.LogFile == "" {
		fatal("No log file for %s - has it run a dispatch?", worker)
	}

	sessionID, err := extractSessionID(*ws.LogFile)
	if err != nil {
		fatal("Cannot resume %s: %v", worker, err)
	}

	repo := ghRepo(r)
	if repo == "" {
		fatal("Cannot detect GitHub repo")
	}

	prJSON, err := run("", "gh", "-R", repo, "pr", "list", "--head", *ws.BranchName, "--json", "number,url,headRefName,mergeable", "--limit", "1")
	if err != nil {
		fatal("Failed to find PR: %v", err)
	}
	if prJSON == "" || prJSON == "[]" {
		fatal("No PR found for branch %s", *ws.BranchName)
	}

	var prs []struct {
		Number      int    `json:"number"`
		URL         string `json:"url"`
		HeadRefName string `json:"headRefName"`
		Mergeable   string `json:"mergeable"`
	}
	if err := json.Unmarshal([]byte(prJSON), &prs); err != nil || len(prs) == 0 {
		fatal("No PR found for branch %s", *ws.BranchName)
	}
	pr := prs[0]

	hasConflicts := strings.EqualFold(pr.Mergeable, "CONFLICTING")
	failingChecks := fetchFailingChecks(repo, pr.Number)
	if hasConflicts {
		info("PR has merge conflicts")
	}
	if len(failingChecks) > 0 {
		info("Found %d failing CI check(s)", len(failingChecks))
	}
	prompt := buildReviewPrompt(repo, pr.Number, pr.URL, pr.HeadRefName, failingChecks, hasConflicts)

	wspath := r.workerDir(worker)
	poolDir := r.poolDir()
	logFile := filepath.Join(poolDir, worker+".log")

	ws.MarkResumed(logFile)
	saveWorkerState(sf, ws)

	inv := claudeInvocation{
		Budget:    budget,
		SessionID: sessionID,
		Prompt:    prompt,
	}

	info("Reviewing PR #%d for %s (%s)...", pr.Number, worker, *ws.BranchName)

	fullArgs := append([]string{"claude"}, inv.Args()...)
	if fg {
		runClaudeFG(wspath, logFile, sf, ws, fullArgs)
	} else {
		pid, err := runClaudeBG(wspath, logFile, sf, ws, fullArgs)
		if err != nil {
			fatal("Failed to start: %v", err)
		}
		info("  %s (PID %d) -> PR #%d", worker, pid, pr.Number)
	}
}

type ghCheck struct {
	Name       string `json:"name"`
	Conclusion string `json:"conclusion"`
}

func fetchFailingChecks(repo string, prNumber int) []ghCheck {
	checksJSON, err := run("", "gh", "-R", repo, "pr", "checks",
		fmt.Sprintf("%d", prNumber), "--json", "name,conclusion")
	if err != nil || checksJSON == "" {
		return nil
	}
	var checks []ghCheck
	if err := json.Unmarshal([]byte(checksJSON), &checks); err != nil {
		return nil
	}
	var failing []ghCheck
	for _, c := range checks {
		switch strings.ToUpper(c.Conclusion) {
		case "FAILURE", "STARTUP_FAILURE", "TIMED_OUT":
			failing = append(failing, c)
		}
	}
	return failing
}

func buildReviewPrompt(repo string, prNumber int, prURL, branch string, failingChecks []ghCheck, hasConflicts bool) string {
	var b strings.Builder
	step := 1

	header := fmt.Sprintf("#%d", prNumber)
	if prURL != "" {
		header = fmt.Sprintf("%s (#%d)", prURL, prNumber)
	}
	b.WriteString(fmt.Sprintf("Review and address feedback on PR %s.\n\n", header))

	if hasConflicts {
		b.WriteString(fmt.Sprintf("%d. This PR has merge conflicts. Rebase onto trunk and resolve them:\n", step))
		b.WriteString("   jj rebase -d 'trunk()'\n")
		b.WriteString("   Then resolve any conflict markers in the affected files.\n")
		b.WriteString(fmt.Sprintf("   After resolving, push: jj git push --named %s=@\n\n", branch))
		step++
	}

	b.WriteString(fmt.Sprintf("%d. Fetch all review comments: gh -R %s pr view %d --comments\n", step, repo, prNumber))
	b.WriteString(fmt.Sprintf("   Also check inline review threads: gh api repos/%s/pulls/%d/comments --jq '.[] | {path, line, body, user: .user.login}'\n\n", repo, prNumber))
	step++

	b.WriteString(fmt.Sprintf("%d. For each unresolved comment:\n", step))
	b.WriteString("   - Understand the reviewer's feedback\n")
	b.WriteString("   - Make the requested change (or document why you disagree in the PR)\n")
	b.WriteString("   - If a comment is unclear, make a reasonable judgment call\n\n")
	step++

	if len(failingChecks) > 0 {
		b.WriteString(fmt.Sprintf("%d. Fix failing CI checks. These checks are FAILING:\n", step))
		for _, c := range failingChecks {
			b.WriteString(fmt.Sprintf("   - %s\n", c.Name))
		}
		b.WriteString("   Investigate and fix each failure:\n")
		b.WriteString(fmt.Sprintf("   - List failed runs: gh -R %s run list --branch %s --status failure --json databaseId,name --limit 5\n", repo, branch))
		b.WriteString(fmt.Sprintf("   - View failure logs: gh -R %s run view <run-id> --log-failed\n", repo))
		b.WriteString("   - Fix the root cause in the code\n\n")
		step++
	}

	suffix := ""
	if len(failingChecks) > 0 {
		suffix = " and CI failures"
	}
	b.WriteString(fmt.Sprintf("%d. After addressing all feedback%s, run checks: linting, type checking, and tests.\n\n", step, suffix))
	step++

	b.WriteString(fmt.Sprintf("%d. Describe and push:\n", step))
	b.WriteString("   jj describe -m \"<ticket>: address review feedback\"\n")
	b.WriteString(fmt.Sprintf("   jj git push --named %s=@\n\n", branch))
	step++

	b.WriteString(fmt.Sprintf("%d. Reply to the PR confirming what you addressed: gh -R %s pr comment %d --body \"<summary of changes made>\"", step, repo, prNumber))

	return b.String()
}

func cmdMount(args []string) {
	if len(args) == 0 {
		fatal("Usage: wsg mount <worker>")
	}
	worker := resolveWorker(args[0])

	r, err := newRepoContext()
	if err != nil {
		fatal("Not in a jj repo")
	}

	sf := r.workerStateFile(worker)
	if _, err := loadWorkerState(sf); err != nil {
		fatal("Worker %s not found", displayWorker(worker))
	}

	wspath := r.workerDir(worker)
	if fi, err := os.Stat(wspath); err != nil || !fi.IsDir() {
		fatal("Workspace directory missing: %s", wspath)
	}

	vs := visorSocket()
	if vs == "" {
		fatal("No kitty visor socket found. Is kitty running?")
	}

	// Build claude resume command
	sessionName := ""
	ws, _ := loadWorkerState(sf)
	if ws.LogFile != nil && *ws.LogFile != "" {
		if sid, err := extractSessionID(*ws.LogFile); err == nil {
			sessionName = sid
		}
	}

	var claudeCmd string
	if sessionName != "" {
		claudeCmd = fmt.Sprintf("claude --resume %s; exec zsh", sessionName)
	} else {
		claudeCmd = "claude; exec zsh"
	}

	// Tab 1: claude (main pane, left)
	winID, err := run("", "kitten", "@", vs,
		"launch", "--type=tab",
		"--tab-title", worker,
		"--cwd="+wspath,
		"--", "zsh", "-ic", claudeCmd)
	if err != nil {
		fatal("Failed to create kitty tab: %v", err)
	}

	if winID != "" {
		// Pane 2: shell (right top)
		rightID, _ := run("", "kitten", "@", vs,
			"launch", "--match", "id:"+winID,
			"--location=vsplit",
			"--cwd="+wspath,
			"--", "zsh", "-ic", "clear; exec zsh")

		// Pane 3: shell (right bottom)
		if rightID != "" {
			run("", "kitten", "@", vs,
				"launch", "--match", "id:"+rightID,
				"--location=hsplit",
				"--cwd="+wspath,
				"--", "zsh", "-ic", "clear; exec zsh")
		}

		// Focus the claude pane
		run("", "kitten", "@", vs, "focus-window", "--match", "id:"+winID)
	}

	info("Mounted %s in kitty", worker)
}

func visorSocket() string {
	entries, err := os.ReadDir("/tmp")
	if err != nil {
		return ""
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "kitty-visor-") {
			return "--to=unix:/tmp/" + e.Name()
		}
	}
	return ""
}
