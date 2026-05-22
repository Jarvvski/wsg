package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type resumeOpts struct {
	Prompt       string
	Budget       string
	SystemPrompt string
	Foreground   bool
}

func resumeWorker(r *RepoContext, worker string, opts resumeOpts) (int, error) {
	sf := r.workerStateFile(worker)
	h, err := OpenWorker(sf)
	if err != nil {
		return 0, fmt.Errorf("worker %s not found", displayWorker(worker))
	}

	if h.State().Status == "busy" {
		return 0, fmt.Errorf("worker %s is busy", displayWorker(worker))
	}

	wspath := r.workerDir(worker)
	logFile := filepath.Join(r.poolDir(), worker+".log")

	sessionID := ""
	if h.State().LogFile != nil && *h.State().LogFile != "" {
		if sid, err := extractSessionID(*h.State().LogFile); err == nil {
			sessionID = sid
		}
	}

	h.Resume(logFile)

	inv := claudeInvocation{
		Budget:    opts.Budget,
		SessionID: sessionID,
		Prompt:    opts.Prompt,
	}
	if sessionID == "" && opts.SystemPrompt != "" {
		inv.SystemPrompt = opts.SystemPrompt
	}

	fullArgs := append([]string{"claude"}, inv.Args()...)
	if opts.Foreground {
		runClaudeFG(wspath, logFile, h, fullArgs)
		return 0, nil
	}
	return runClaudeBG(wspath, logFile, h, fullArgs)
}

func cmdSend(args []string) {
	if len(args) < 2 {
		fatal("Usage: wsg send <worker> \"<prompt>\" [--fg] [--budget USD]")
	}

	var worker, prompt string
	var fgFlag *bool
	budget := "5"

	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--fg":
			t := true
			fgFlag = &t
		case "--bg":
			f := false
			fgFlag = &f
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
		fatal("Usage: wsg send <worker> \"<prompt>\" [--fg|--bg] [--budget USD]")
	}

	r, err := newRepoContext()
	if err != nil {
		fatal("Not in a jj repo")
	}

	fg := resolveForeground(r, fgFlag)

	worker = resolveWorker(worker)
	info("Sending to %s...", displayWorker(worker))

	pid, err := resumeWorker(r, worker, resumeOpts{
		Prompt:       prompt,
		Budget:       budget,
		SystemPrompt: sendSystemPrompt(ghRepo(r)),
		Foreground:   fg,
	})
	if err != nil {
		fatal("%v", err)
	}
	if !fg {
		info("  %s (PID %d) -> %s", worker, pid, prompt[:min(len(prompt), 60)])
	}
}

func cmdReview(args []string) {
	if len(args) == 0 {
		fatal("Usage: wsg review <worker> [--fg] [--budget USD]")
	}

	var worker string
	var fgFlag *bool
	budget := "5"

	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--fg":
			t := true
			fgFlag = &t
		case "--bg":
			f := false
			fgFlag = &f
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
		fatal("Usage: wsg review <worker> [--fg|--bg] [--budget USD]")
	}

	r, err := newRepoContext()
	if err != nil {
		fatal("Not in a jj repo")
	}

	fg := resolveForeground(r, fgFlag)

	worker = resolveWorker(worker)

	prompt, err := buildWorkerReviewPrompt(r, worker)
	if err != nil {
		fatal("%v", err)
	}

	pid, err := resumeWorker(r, worker, resumeOpts{
		Prompt:     prompt,
		Budget:     budget,
		Foreground: fg,
	})
	if err != nil {
		fatal("%v", err)
	}
	if !fg {
		info("  %s (PID %d) -> review", worker, pid)
	}
}

func buildWorkerReviewPrompt(r *RepoContext, worker string) (string, error) {
	h, err := OpenWorker(r.workerStateFile(worker))
	if err != nil {
		return "", fmt.Errorf("worker %s not found", displayWorker(worker))
	}
	ws := h.State()

	if ws.BranchName == nil || *ws.BranchName == "" {
		return "", fmt.Errorf("worker %s has no branch - has it run a dispatch?", worker)
	}

	if !strings.Contains(*ws.BranchName, "-") || !strings.HasPrefix(*ws.BranchName, "adam/") {
		resolveWorkerBranch(r, worker, ws)
		h.save()
	}

	repo := ghRepo(r)
	if repo == "" {
		return "", fmt.Errorf("cannot detect GitHub repo")
	}

	prJSON, err := run("", "gh", "-R", repo, "pr", "list", "--head", *ws.BranchName, "--json", "number,url,headRefName,mergeable", "--limit", "1")
	if err != nil {
		return "", fmt.Errorf("failed to find PR: %v", err)
	}
	if prJSON == "" || prJSON == "[]" {
		return "", fmt.Errorf("no PR found for branch %s", *ws.BranchName)
	}

	var prs []struct {
		Number      int    `json:"number"`
		URL         string `json:"url"`
		HeadRefName string `json:"headRefName"`
		Mergeable   string `json:"mergeable"`
	}
	if err := json.Unmarshal([]byte(prJSON), &prs); err != nil || len(prs) == 0 {
		return "", fmt.Errorf("no PR found for branch %s", *ws.BranchName)
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
	return buildReviewPrompt(repo, pr.Number, pr.URL, pr.HeadRefName, failingChecks, hasConflicts), nil
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
	h, err := OpenWorker(sf)
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
	if h.State().LogFile != nil && *h.State().LogFile != "" {
		if sid, err := extractSessionID(*h.State().LogFile); err == nil {
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
