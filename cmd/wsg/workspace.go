package main

import (
	"bufio"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func cmdList() {
	r, err := newRepoContext()
	if err != nil {
		fatal("Not in a jj repo")
	}
	entries, err := cacheGet(r)
	if err != nil {
		fatal("%v", err)
	}
	if len(entries) == 0 {
		fmt.Println("  No workspaces")
		return
	}
	for _, e := range entries {
		marker := ""
		if e.Name != "default" {
			if fi, err := os.Stat(filepath.Join(e.Path, ".jj")); err != nil || !fi.IsDir() {
				marker = " (missing)"
			}
		}
		fmt.Printf("  %s ➜ %s%s\n", e.Name, e.Path, marker)
	}
}

func cmdAdd(args []string) {
	fs := flag.NewFlagSet("add", flag.ExitOnError)
	revision := fs.String("r", "", "revision")
	fs.StringVar(revision, "revision", "", "revision")
	fs.Parse(args)
	if fs.NArg() < 1 {
		fatal("Usage: wsg add <name> [-r <rev>]")
	}
	name := fs.Arg(0)

	r, err := newRepoContext()
	if err != nil {
		fatal("Not in a jj repo")
	}

	if name == "default" {
		fmt.Println(r.Root)
		return
	}

	wspath := r.workerDir(name)

	entries, err := cacheGet(r)
	if err != nil {
		fatal("%v", err)
	}
	for _, e := range entries {
		if e.Name == name {
			fmt.Println(wspath)
			return
		}
	}

	wait, err := Provision(r, name, *revision, AdHocRole)
	if err != nil {
		fatal("jj workspace add: %v", err)
	}
	wait()
	fmt.Println(wspath)
}

// ProvisionRole distinguishes a one-shot `wsg add` workspace from a pool
// worker. WorkerRole additionally writes the idle worker state file so the
// pool can dispatch into the new slot.
type ProvisionRole int

const (
	AdHocRole ProvisionRole = iota
	WorkerRole
)

// Provision creates a jj workspace at r.workerDir(name) on revision (empty
// means head) and registers it in the cache - both synchronously. The
// slower copy steps (.env, the synapse clone, plus the idle worker state
// file for WorkerRole) run in a background goroutine; the returned wait
// function blocks until they complete.
//
// Pool.grow drives multiple workers' finalisation in parallel by collecting
// wait funcs onto a sync.WaitGroup. cmdAdd calls wait() before printing the
// path so the user can cd into a fully-set-up workspace.
func Provision(r *RepoContext, name, revision string, role ProvisionRole) (func(), error) {
	wspath := r.workerDir(name)
	if err := os.MkdirAll(r.BaseDir, 0755); err != nil {
		return nil, err
	}
	if err := jjAddWorkspace(r.Root, wspath, revision); err != nil {
		return nil, err
	}
	cacheRebuild(r)
	done := make(chan struct{})
	go func() {
		defer close(done)
		copyEnvFile(r.Root, wspath)
		copySynapseClone(r.Root, wspath)
		if role == WorkerRole {
			CreateIdleWorker(r, name)
		}
	}()
	return func() { <-done }, nil
}

// Teardown reverses Provision: forgets the jj workspace, drops the cache
// entry, removes the workspace directory, and best-effort deletes the
// worker state and log files (no-op when absent, so safe for AdHocRole
// workspaces too). Any external clean-up step - e.g. cmdRm's
// `mise run :dev -- murder` - is the caller's responsibility and must
// happen before this is called.
func Teardown(r *RepoContext, name string) error {
	wspath := r.workerDir(name)
	jjForgetWorkspace(r.Root, name)
	cacheRebuild(r)
	if fi, err := os.Stat(wspath); err == nil && fi.IsDir() {
		os.RemoveAll(wspath)
	}
	os.Remove(r.workerStateFile(name))
	os.Remove(filepath.Join(r.poolDir(), name+".log"))
	return nil
}

func copyEnvFile(root, wspath string) {
	src := filepath.Join(root, ".env")
	dst := filepath.Join(wspath, ".env")
	if _, err := os.Stat(src); err != nil {
		return
	}
	if _, err := os.Stat(dst); err == nil {
		return
	}
	data, err := os.ReadFile(src)
	if err != nil {
		return
	}
	os.WriteFile(dst, data, 0644)
}

func copySynapseClone(root, wspath string) {
	rel := "tools/dev-cli/synapse/clone"
	src := filepath.Join(root, rel)
	if fi, err := os.Stat(src); err != nil || !fi.IsDir() {
		return
	}
	dst := filepath.Join(wspath, rel)
	if fi, err := os.Stat(dst); err == nil && fi.IsDir() {
		return
	}
	os.MkdirAll(filepath.Dir(dst), 0755)
	run("", "rsync", "-a", "--exclude", ".git", src+"/", dst+"/")
}

func cmdRm(args []string) {
	force := false
	var names []string
	for _, a := range args {
		if a == "--force" || a == "-f" {
			force = true
		} else {
			names = append(names, a)
		}
	}
	if len(names) == 0 {
		fatal("Usage: wsg rm [--force] <name> [name...]")
	}

	r, err := newRepoContext()
	if err != nil {
		fatal("Not in a jj repo")
	}

	for _, name := range names {
		if name == "default" {
			info("Cannot remove default workspace")
			continue
		}
		wspath := r.workerDir(name)
		if !force {
			if fi, err := os.Stat(wspath); err == nil && fi.IsDir() {
				if output, err := run(wspath, "mise", "run", ":dev", "--", "murder"); err != nil {
					info("Cleanup failed for %s:\n%s", name, output)
					continue
				}
			}
		}
		existed := false
		if fi, err := os.Stat(wspath); err == nil && fi.IsDir() {
			existed = true
		}
		Teardown(r, name)
		if existed {
			info("Deleted %s", wspath)
		}
	}
}

func cmdClean() {
	r, err := newRepoContext()
	if err != nil {
		fatal("Not in a jj repo")
	}
	entries, err := cacheGet(r)
	if err != nil {
		fatal("%v", err)
	}

	var removable []string
	for _, e := range entries {
		if e.Name != "default" {
			removable = append(removable, e.Name)
		}
	}
	if len(removable) == 0 {
		fmt.Println("No workspaces to remove")
		return
	}

	fmt.Printf("Remove %d workspace(s)?\n", len(removable))
	for _, name := range removable {
		fmt.Printf("  %s\n", name)
	}
	fmt.Print("Confirm? (y/n) ")

	reader := bufio.NewReader(os.Stdin)
	ans, _ := reader.ReadString('\n')
	ans = strings.TrimSpace(strings.ToLower(ans))
	if ans != "y" {
		return
	}
	for _, name := range removable {
		cmdRm([]string{name})
	}
}

func cmdRoot() {
	root, err := repoRoot()
	if err != nil {
		fatal("Not in a jj repo")
	}
	fmt.Println(root)
}

func cmdWhere() {
	r, err := newRepoContext()
	if err != nil {
		fatal("Not in a jj repo")
	}
	fmt.Printf("repo:       %s\n", r.Root)
	fmt.Printf("workspaces: %s\n", r.BaseDir)
}

func cmdPath(args []string) {
	if len(args) == 0 {
		fatal("Usage: wsg path <name>")
	}
	name := args[0]
	r, err := newRepoContext()
	if err != nil {
		fatal("Not in a jj repo")
	}
	if name == "default" {
		fmt.Println(r.Root)
	} else {
		fmt.Println(filepath.Join(r.BaseDir, name))
	}
}

func cmdRefresh() {
	r, err := newRepoContext()
	if err != nil {
		fatal("Not in a jj repo")
	}
	if _, err := cacheRebuild(r); err != nil {
		fatal("%v", err)
	}
	info("Cache refreshed")
}
