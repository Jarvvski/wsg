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

// ResumeOutcome reports whether a resume call continued an existing claude
// session or started a fresh one. A non-empty SessionID means the previous
// run's context was inherited; a non-empty Reason means we tried to resume
// but had to start fresh, so the caller can surface it instead of silently
// paying for a context-cold restart.
type ResumeOutcome struct {
	PID       int
	SessionID string
	Reason    string
}

func (o ResumeOutcome) Resumed() bool { return o.SessionID != "" }

func resumeWorker(r *RepoContext, worker string, opts resumeOpts) (ResumeOutcome, error) {
	h, err := LoadLiveWorker(r, worker)
	if err != nil {
		return ResumeOutcome{}, fmt.Errorf("worker %s not found", displayWorker(worker))
	}

	ws := h.Status()
	if ws.Status.IsActive() {
		return ResumeOutcome{}, fmt.Errorf("worker %s is busy", displayWorker(worker))
	}

	sessionID, reason := resolveSession(ws)

	inv := claudeInvocation{
		SessionID: sessionID,
		Prompt:    opts.Prompt,
	}
	if sessionID == "" && opts.SystemPrompt != "" {
		inv.SystemPrompt = opts.SystemPrompt
	}

	pid, err := h.Resume(inv, opts.Foreground)
	if err != nil {
		return ResumeOutcome{}, err
	}
	return ResumeOutcome{PID: pid, SessionID: sessionID, Reason: reason}, nil
}

// resolveSession decides whether a worker's prior log carries a session ID we
// can resume. Returns (sessionID, "") when resume is possible, or ("", reason)
// when the caller must start fresh - reason is human-facing.
func resolveSession(ws *WorkerState) (string, string) {
	if ws.LogFile == nil || *ws.LogFile == "" {
		return "", "no prior session log"
	}
	path := *ws.LogFile
	if _, err := os.Stat(path); err != nil {
		return "", fmt.Sprintf("log file unreadable (%v)", err)
	}
	sid, err := extractSessionID(path)
	if err != nil || sid == "" {
		return "", "log has no session id yet"
	}
	return sid, ""
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

	outcome, err := NewActions(r).Send(worker, prompt, fg)
	if err != nil {
		fatal("%v", err)
	}
	reportResumeOutcome(outcome)
	if !fg {
		info("  %s (PID %d) -> %s", worker, outcome.PID, prompt[:min(len(prompt), 60)])
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

	outcome, err := NewActions(r).Review(worker, fg)
	if err != nil {
		fatal("%v", err)
	}
	reportResumeOutcome(outcome)
	if !fg {
		info("  %s (PID %d) -> review", worker, outcome.PID)
	}
}

// reportResumeOutcome surfaces whether the resume continued an existing
// session or fell back to a fresh one. Silent fresh starts mask context-cold
// restarts; users have asked to "resume" so a fresh start is worth naming.
func reportResumeOutcome(o ResumeOutcome) {
	if o.Resumed() {
		info("Resumed session %s", o.SessionID)
		return
	}
	if o.Reason != "" {
		info("Starting fresh session (%s)", o.Reason)
	}
}

// resumeBadge renders the resume outcome as a short suffix for TUI status
// lines: "(resumed)" when the session continued, "(fresh: <reason>)" when
// it fell back, "" if neither applies.
func resumeBadge(o ResumeOutcome) string {
	if o.Resumed() {
		return "(resumed)"
	}
	if o.Reason != "" {
		return "(fresh: " + o.Reason + ")"
	}
	return ""
}

func buildWorkerReviewPrompt(r *RepoContext, worker string) (string, error) {
	h, err := LoadLiveWorker(r, worker)
	if err != nil {
		return "", fmt.Errorf("worker %s not found", displayWorker(worker))
	}
	if h.Status().Branch == "" {
		return "", fmt.Errorf("worker %s has no branch - has it run a dispatch?", worker)
	}

	repo := ghRepo(r)
	if repo == "" {
		return "", fmt.Errorf("cannot detect GitHub repo")
	}

	branch := h.Branch()
	pr, err := ghPRForBranch(repo, branch)
	if err != nil {
		return "", err
	}
	if pr == nil {
		return "", fmt.Errorf("no PR found for branch %s", branch)
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

	v, err := openVisor()
	if err != nil {
		fatal("%v", err)
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

	winID, err := v.NewTab(worker, wspath, claudeCmd)
	if err != nil {
		fatal("Failed to create kitty tab: %v", err)
	}

	if winID != "" {
		rightID, _ := v.SplitRight(winID, wspath, "clear; exec zsh")
		if rightID != "" {
			v.SplitDown(rightID, wspath, "clear; exec zsh")
		}
		v.Focus(winID)
	}

	info("Mounted %s in kitty", worker)
}
