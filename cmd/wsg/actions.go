package main

import "fmt"

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

// Dismiss removes worker from the pool when idle, or resets it to idle
// when in a terminal state (done/failed). Errors on busy. Returns the new
// pool size when the worker was removed, or -1 if the worker was only
// reset (size unchanged).
func (a *WorkerActions) Dismiss(worker string) (int, error) {
	h, err := loadWorker(a.repo, worker)
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
