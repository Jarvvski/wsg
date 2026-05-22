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

	now := nowUTC()
	ws.Status = "busy"
	ws.StartedAt = &now
	ws.CompletedAt = nil
	ws.ExitCode = nil
	ws.Error = nil
	ws.LogFile = &logFile
	saveWorkerState(sf, ws)

	var claudeArgs []string
	if sessionID != "" {
		claudeArgs = []string{
			"-p",
			"--resume", sessionID,
			"--fork-session",
			"--max-budget-usd", budget,
			"--output-format", "stream-json",
			"--verbose",
			prompt,
		}
		info("Sending to %s (session %s)...", worker, sessionID[:8])
	} else {
		repo := ghRepo(r)
		systemPrompt := fmt.Sprintf(`You are an autonomous agent in a jj (Jujutsu VCS) workspace.

CRITICAL RULES:
- Use jj commands, NEVER git commands.
- The gh CLI requires: gh -R %s pr create ...
- To push your work: jj git push --named <branch>=@
- Do NOT ask questions. Make reasonable decisions and proceed.`, repo)

		claudeArgs = []string{
			"-p",
			"--max-budget-usd", budget,
			"--output-format", "stream-json",
			"--verbose",
			"--append-system-prompt", systemPrompt,
			prompt,
		}
		info("Starting fresh session for %s...", worker)
	}

	if fg {
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
			fatal("Failed to start: %v", err)
		}
		ws.PID = &pid
		saveWorkerState(sf, ws)
		info("  %s (PID %d) -> %s", worker, pid, prompt[:min(len(prompt), 60)])

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

	prJSON, err := run("", "gh", "-R", repo, "pr", "list", "--head", *ws.BranchName, "--json", "number,url,headRefName", "--limit", "1")
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
	}
	if err := json.Unmarshal([]byte(prJSON), &prs); err != nil || len(prs) == 0 {
		fatal("No PR found for branch %s", *ws.BranchName)
	}
	pr := prs[0]

	prompt := fmt.Sprintf(`Review and address PR comments on %s (#%d).

1. Fetch all review comments: gh -R %s pr view %d --comments
   Also check inline review threads: gh api repos/%s/pulls/%d/comments --jq '.[] | {path, line, body, user: .user.login}'

2. For each unresolved comment:
   - Understand the reviewer's feedback
   - Make the requested change (or document why you disagree in the PR)
   - If a comment is unclear, make a reasonable judgment call

3. After addressing all comments, run checks: linting, type checking, and tests.

4. Describe and push:
   jj describe -m "<ticket>: address review feedback"
   jj git push --named %s=@

5. Reply to the PR confirming what you addressed: gh -R %s pr comment %d --body "<summary of changes made>"`,
		pr.URL, pr.Number,
		repo, pr.Number,
		repo, pr.Number,
		pr.HeadRefName,
		repo, pr.Number)

	wspath := r.workerDir(worker)
	poolDir := r.poolDir()
	logFile := filepath.Join(poolDir, worker+".log")

	now := nowUTC()
	ws.Status = "busy"
	ws.StartedAt = &now
	ws.CompletedAt = nil
	ws.ExitCode = nil
	ws.Error = nil
	saveWorkerState(sf, ws)

	claudeArgs := []string{
		"-p",
		"--resume", sessionID,
		"--fork-session",
		"--max-budget-usd", budget,
		"--output-format", "stream-json",
		"--verbose",
		prompt,
	}

	info("Reviewing PR #%d for %s (%s)...", pr.Number, worker, *ws.BranchName)

	if fg {
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
			fatal("Failed to start: %v", err)
		}
		ws.PID = &pid
		saveWorkerState(sf, ws)
		info("  %s (PID %d) -> PR #%d", worker, pid, pr.Number)

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
