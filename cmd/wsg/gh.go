package main

import (
	"encoding/json"
	"fmt"
	"strings"
)

// ghRepo returns the GitHub repo slug ("owner/name") for the working repo.
// Reads pool config first; falls back to the jj `origin` remote URL.
func ghRepo(r *RepoContext) string {
	configFile := r.poolConfigFile()
	if cfg, err := loadPoolConfig(configFile); err == nil && cfg.GHRepo != "" {
		return strings.TrimSuffix(cfg.GHRepo, ".git")
	}
	return jjRemoteOrigin(r.Root)
}

// ghPR is the subset of a PR's JSON that wsg reads via `gh pr list`.
type ghPR struct {
	Number      int    `json:"number"`
	URL         string `json:"url"`
	HeadRefName string `json:"headRefName"`
	Mergeable   string `json:"mergeable"`
}

// ghPRForBranch returns the open PR whose head matches branch, or nil if
// none exists. An error indicates the gh call itself failed; a nil result
// with nil error means there is simply no PR for that branch.
func ghPRForBranch(repo, branch string) (*ghPR, error) {
	prJSON, err := run("", "gh", "-R", repo, "pr", "list",
		"--head", branch, "--json", "number,url,headRefName,mergeable", "--limit", "1")
	if err != nil {
		return nil, fmt.Errorf("failed to find PR: %v", err)
	}
	if prJSON == "" || prJSON == "[]" {
		return nil, nil
	}
	var prs []ghPR
	if err := json.Unmarshal([]byte(prJSON), &prs); err != nil || len(prs) == 0 {
		return nil, nil
	}
	return &prs[0], nil
}

// ghCheck is one CI check on a PR (name + conclusion only).
type ghCheck struct {
	Name       string `json:"name"`
	Conclusion string `json:"conclusion"`
}

// ghFailingChecks returns CI checks on PR prNumber whose conclusion counts
// as failing: FAILURE, STARTUP_FAILURE, or TIMED_OUT.
func ghFailingChecks(repo string, prNumber int) []ghCheck {
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

// ghOpenInBrowser opens the PR for branch in the default browser via gh.
func ghOpenInBrowser(repo, branch string) error {
	_, err := run("", "gh", "-R", repo, "pr", "view", branch, "--web")
	return err
}

// ghPRCreateCmd renders the `gh pr create` command string that a worker
// agent will execute. Kept here (rather than prompt.go) so all gh argv
// construction lives in one module; the exact rendered form is part of
// the agent contract and must not drift.
func ghPRCreateCmd(repo, ticketID string, depCtx *DependencyContext) string {
	if depCtx != nil && depCtx.PRBase != "" {
		return fmt.Sprintf(
			`gh -R %s pr create --head <branch> --base %s --title "%s: <title from ticket>" --body "<summary of changes and link to Linear ticket>"`,
			repo, depCtx.PRBase, ticketID)
	}
	return fmt.Sprintf(
		`gh -R %s pr create --head <branch> --title "%s: <title from ticket>" --body "<summary of changes and link to Linear ticket>"`,
		repo, ticketID)
}
