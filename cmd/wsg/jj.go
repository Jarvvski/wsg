package main

import "strings"

// jj.go is the single seam onto the `jj` binary. Every shell-out to `jj`
// outside this file is a bug - call one of the verbs below instead.
//
// The verbs are intentionally thin: they take `dir` (a repo root or workspace
// path), assemble argv, and either return a parsed value or just an error.
// Output parsing (bookmark lists, remote URLs, rev existence) lives here so
// callers do not re-implement it.

// branchOwner is the bookmark namespace this repo's branches live under, used
// to identify worker-created branches for a ticket. Centralised here so the
// convention `<owner>/<ticket>-<slug>` has one home.
const branchOwner = "adam"

// branchPrefix returns "<owner>/<ticketLower>-", the prefix every worker
// branch for ticketLower starts with. Match a bookmark against this prefix
// to find the branch a worker created for its ticket.
func branchPrefix(ticketLower string) string {
	return branchOwner + "/" + ticketLower + "-"
}

func jjAddWorkspace(repoRoot, wspath, revision string) error {
	args := []string{"workspace", "add"}
	if revision != "" {
		args = append(args, "--revision", revision)
	}
	args = append(args, wspath)
	_, err := run(repoRoot, "jj", args...)
	return err
}

func jjForgetWorkspace(repoRoot, name string) error {
	_, err := run(repoRoot, "jj", "workspace", "forget", name)
	return err
}

// jjListWorkspaceNames returns the workspace names reported by
// `jj workspace list`, in order. Lines are of the form "name: <commit>"; only
// the name is returned.
func jjListWorkspaceNames(repoRoot string) ([]string, error) {
	output, err := run(repoRoot, "jj", "workspace", "list")
	if err != nil {
		return nil, err
	}
	var names []string
	for _, line := range strings.Split(output, "\n") {
		if line == "" {
			continue
		}
		name := strings.TrimSpace(strings.SplitN(line, ":", 2)[0])
		if name != "" {
			names = append(names, name)
		}
	}
	return names, nil
}

// jjBookmarks returns the bookmark names visible from dir, one per line.
func jjBookmarks(dir string) ([]string, error) {
	output, err := run(dir, "jj", "bookmark", "list", "--template", `name ++ "\n"`)
	if err != nil {
		return nil, err
	}
	var names []string
	for _, line := range strings.Split(output, "\n") {
		if line != "" {
			names = append(names, line)
		}
	}
	return names, nil
}

// jjResolveBranchForTicket scans bookmarks visible from dir and returns the
// first one whose name starts with the per-ticket branch prefix. Returns nil
// if there is no match or the listing fails.
func jjResolveBranchForTicket(dir, ticketLower string) *string {
	bms, err := jjBookmarks(dir)
	if err != nil {
		return nil
	}
	prefix := branchPrefix(ticketLower)
	for _, name := range bms {
		if strings.HasPrefix(name, prefix) {
			n := name
			return &n
		}
	}
	return nil
}

func jjNewOn(dir string, revs ...string) error {
	args := append([]string{"new"}, revs...)
	_, err := run(dir, "jj", args...)
	return err
}

func jjPush(dir, branch string) error {
	_, err := run(dir, "jj", "git", "push", "-b", branch)
	return err
}

func jjRebase(dir, branch, dest string) error {
	_, err := run(dir, "jj", "rebase", "-b", branch, "-d", dest)
	return err
}

func jjOpUndo(dir string) error {
	_, err := run(dir, "jj", "op", "undo")
	return err
}

// jjRevExists reports whether rev resolves to at least one commit. Used to
// check whether a branch bookmark still exists.
func jjRevExists(dir, rev string) bool {
	output, err := run(dir, "jj", "log", "-r", rev, "--no-graph", "-T", `"ok"`, "--limit", "1")
	if err != nil {
		return false
	}
	return strings.Contains(output, "ok")
}

func jjConfigGet(dir, key string) (string, error) {
	return run(dir, "jj", "config", "get", key)
}

// jjRemoteOrigin parses `jj git remote list` and returns the owner/name slug
// for the `origin` remote (e.g. "Jarvvski/wsg"). Returns "" if there is no
// origin or its URL cannot be parsed.
func jjRemoteOrigin(dir string) string {
	output, err := run(dir, "jj", "git", "remote", "list")
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(output, "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 2 && fields[0] == "origin" {
			url := strings.TrimSuffix(fields[1], ".git")
			if idx := strings.LastIndex(url, ":"); idx != -1 {
				url = url[idx+1:]
			} else if idx := strings.LastIndex(url, "/"); idx != -1 {
				parts := strings.Split(url, "/")
				if len(parts) >= 2 {
					url = parts[len(parts)-2] + "/" + parts[len(parts)-1]
				}
			}
			return url
		}
	}
	return ""
}
