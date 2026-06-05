package main

import (
	"fmt"
	"os"
	"strings"
)

type resumeOpts struct {
	Prompt       string
	SystemPrompt string
	Foreground   bool
}

func resumeWorker(r *RepoContext, worker string, opts resumeOpts) (int, error) {
	h, err := LoadLiveWorker(r, worker)
	if err != nil {
		return 0, fmt.Errorf("worker %s not found", displayWorker(worker))
	}

	ws := h.Status()
	if ws.Status == "busy" {
		return 0, fmt.Errorf("worker %s is busy", displayWorker(worker))
	}

	sessionID := ""
	if ws.LogFile != nil && *ws.LogFile != "" {
		if sid, err := extractSessionID(*ws.LogFile); err == nil {
			sessionID = sid
		}
	}

	inv := claudeInvocation{
		SessionID: sessionID,
		Prompt:    opts.Prompt,
	}
	if sessionID == "" && opts.SystemPrompt != "" {
		inv.SystemPrompt = opts.SystemPrompt
	}

	return h.Resume(inv, opts.Foreground)
}

func cmdSend(args []string) {
	if len(args) < 2 {
		fatal("Usage: wsg send <worker> \"<prompt>\" [--fg]")
	}

	var worker, prompt string
	var fgFlag *bool

	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--fg":
			t := true
			fgFlag = &t
		case "--bg":
			f := false
			fgFlag = &f
		default:
			if worker == "" {
				worker = args[i]
			} else if prompt == "" {
				prompt = args[i]
			}
		}
	}

	if worker == "" || prompt == "" {
		fatal("Usage: wsg send <worker> \"<prompt>\" [--fg|--bg]")
	}

	r, err := newRepoContext()
	if err != nil {
		fatal("Not in a jj repo")
	}

	fg := resolveForeground(r, fgFlag)

	worker = resolveWorker(worker)
	info("Sending to %s...", displayWorker(worker))

	pid, err := NewActions(r).Send(worker, prompt, fg)
	if err != nil {
		fatal("%v", err)
	}
	if !fg {
		info("  %s (PID %d) -> %s", worker, pid, prompt[:min(len(prompt), 60)])
	}
}

func cmdReview(args []string) {
	if len(args) == 0 {
		fatal("Usage: wsg review <worker> [--fg]")
	}

	var worker string
	var fgFlag *bool

	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--fg":
			t := true
			fgFlag = &t
		case "--bg":
			f := false
			fgFlag = &f
		default:
			if worker == "" {
				worker = args[i]
			}
		}
	}

	if worker == "" {
		fatal("Usage: wsg review <worker> [--fg|--bg]")
	}

	r, err := newRepoContext()
	if err != nil {
		fatal("Not in a jj repo")
	}

	fg := resolveForeground(r, fgFlag)

	worker = resolveWorker(worker)

	pid, err := NewActions(r).Review(worker, fg)
	if err != nil {
		fatal("%v", err)
	}
	if !fg {
		info("  %s (PID %d) -> review", worker, pid)
	}
}

func buildWorkerReviewPrompt(r *RepoContext, worker string) (string, error) {
	h, err := loadWorker(r, worker)
	if err != nil {
		return "", fmt.Errorf("worker %s not found", displayWorker(worker))
	}
	ws := h.Status()

	if ws.BranchName == nil || *ws.BranchName == "" {
		return "", fmt.Errorf("worker %s has no branch - has it run a dispatch?", worker)
	}

	if !strings.Contains(*ws.BranchName, "-") || !strings.HasPrefix(*ws.BranchName, "adam/") {
		h.refreshBranch()
	}

	repo := ghRepo(r)
	if repo == "" {
		return "", fmt.Errorf("cannot detect GitHub repo")
	}

	pr, err := ghPRForBranch(repo, *ws.BranchName)
	if err != nil {
		return "", err
	}
	if pr == nil {
		return "", fmt.Errorf("no PR found for branch %s", *ws.BranchName)
	}

	hasConflicts := strings.EqualFold(pr.Mergeable, "CONFLICTING")
	failingChecks := ghFailingChecks(repo, pr.Number)
	if hasConflicts {
		info("PR has merge conflicts")
	}
	if len(failingChecks) > 0 {
		info("Found %d failing CI check(s)", len(failingChecks))
	}
	return buildReviewPrompt(repo, pr.Number, pr.URL, pr.HeadRefName, failingChecks, hasConflicts), nil
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

	h, err := loadWorker(r, worker)
	if err != nil {
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

	sessionName := ""
	if ws := h.Status(); ws.LogFile != nil && *ws.LogFile != "" {
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
