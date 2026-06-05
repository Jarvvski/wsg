package main

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

func main() {
	cmd := ""
	if len(os.Args) > 1 {
		cmd = os.Args[1]
	}
	var args []string
	if len(os.Args) > 2 {
		args = os.Args[2:]
	}

	switch cmd {
	case "add", "a":
		cmdAdd(args)
	case "rm", "remove":
		cmdRm(args)
	case "list", "ls":
		cmdList()
	case "clean":
		cmdClean()
	case "root":
		cmdRoot()
	case "where", "info":
		cmdWhere()
	case "path":
		cmdPath(args)
	case "refresh", "sync":
		cmdRefresh()
	case "reset":
		cmdPoolReset(args)
	case "pool":
		cmdPool(args)
	case "dispatch", "d":
		cmdDispatch(args)
	case "send", "s":
		cmdSend(args)
	case "review", "rev":
		cmdReview(args)
	case "mount", "m":
		cmdMount(args)
	case "status":
		cmdPoolList()
	case "logs", "log":
		cmdLogs(args)
	case "__orchestrate":
		cmdOrchestrate(args)
	case "completion":
		cmdCompletion(args)
	case "__complete":
		cmdInternalComplete(args)
	case "help", "-h", "--help":
		cmdHelp()
	case "":
		cmdDefault()
	default:
		fatal("Unknown command: %s. Run 'wsg help' for usage.", cmd)
	}
}

func cmdDefault() {
	r, err := newRepoContext()
	if err != nil {
		fatal("Not in a jj repo")
	}
	if _, err := OpenPool(r); err == nil {
		if isTTY {
			runTUI(r)
		} else {
			cmdPoolList()
		}
		return
	}
	if !isTTY {
		fatal("No pool. Run: wsg pool <N>")
	}
	info("No pool configured for this repo.")
	fmt.Fprintf(os.Stderr, "Pool size: ")
	var input string
	fmt.Scanln(&input)
	input = strings.TrimSpace(input)
	if input == "" {
		return
	}
	size, err := strconv.Atoi(input)
	if err != nil || size < 1 {
		fatal("Invalid pool size: %s", input)
	}
	cmdPoolResize([]string{strconv.Itoa(size)})
}

func fatal(format string, a ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", a...)
	os.Exit(1)
}

func info(format string, a ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", a...)
}

func confirm(format string, a ...any) bool {
	fmt.Fprintf(os.Stderr, format+" [Y/n] ", a...)
	var answer string
	fmt.Scanln(&answer)
	answer = strings.ToLower(strings.TrimSpace(answer))
	return answer != "n" && answer != "no"
}

func cmdHelp() {
	fmt.Print(`wsg - jj workspace manager

Usage:
  wsg add <name> [-r <rev>]     Create workspace and print path (stdout)
  wsg rm [--force] <name>       Remove workspace
  wsg list                      List workspaces
  wsg clean                     Remove all non-default workspaces
  wsg root                      Print repo root
  wsg where                     Show repo and workspace paths
  wsg path <name>               Print workspace path
  wsg refresh                   Rebuild workspace cache

Pool:
  wsg pool <N>                  Set pool size (creates pool if needed, safe shrink)
  wsg pool list                 Show pool status
  wsg pool rm <worker>          Remove a worker from the pool (must not be busy)
  wsg pool reset <worker>       Reset a worker to idle
  wsg pool destroy              Tear down all workers and remove pool

Dispatch:
  wsg dispatch <TICKET>...      Assign ticket(s) to idle workers
  wsg dispatch --all            Dispatch all ready-for-agent tickets
    --fg / --bg                 Override foreground config for this run
    --model MODEL               Model for worker agents (default: opus)
    --label LABEL               Label filter for --all (default: ready-for-agent)
    --no-orchestrate            Skip parent issue detection, dispatch as single ticket

  Parent issues: when a single ticket has sub-issues with blockers,
  wsg auto-detects this and dispatches in dependency order. Workers
  for blocked issues start on their blocker's branch (stacked PRs).
  Re-run the same command to resume after Ctrl-C.
  wsg send <worker> "<prompt>"  Resume worker session with a follow-up prompt
    --fg / --bg                 Override foreground config for this run
  wsg review <worker>           Address PR review comments in worker session
    --fg / --bg                 Override foreground config for this run
  wsg mount <worker>            Open worker in kitty (claude + two shells)

Config (pool.json):
  "foreground": true/false      Default foreground mode (default: false)

Observability:
  wsg status                    Alias for pool list
  wsg logs <worker>             Tail a worker's log file (stream-json formatted)

Environment:
  JJ_WS_DIR    Base directory for workspaces
               Default: ../<repo-name>-workspaces/
`)
}
