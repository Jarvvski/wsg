# wsg

A workspace manager for [Jujutsu](https://jj-vcs.github.io/jj/) repos. Manages workspaces, a worker pool, and dispatches GitHub issues to Claude Code or Codex agents.

## Requirements

| Tool | Purpose | Install |
|------|---------|---------|
| [Go](https://go.dev/) 1.26+ | Build wsg | `brew install go` |
| [jj](https://jj-vcs.github.io/jj/) | Version control (workspaces, branches, push) | `brew install jj` |
| [Claude Code](https://docs.anthropic.com/en/docs/claude-code) or [Codex](https://developers.openai.com/codex/cli) | Headless agent runtime for workers | Install the selected runtime |
| [gh](https://cli.github.com/) | GitHub PR creation from dispatched agents | `brew install gh` |

Optional:

| Tool | Purpose | Install |
|------|---------|---------|
| [mise](https://mise.jdx.dev/) | Runtime version management | `brew install mise` |
| [rsync](https://rsync.samba.org/) | Workspace file sync | Usually pre-installed |
| [kitty](https://sw.kovidgoat.net/kitty/) | Terminal multiplexer for `wsg mount` | `brew install kitty` |

### gh CLI setup

wsg uses `gh` to create pull requests from dispatched agents. Authenticate before first use:

```bash
gh auth login
```

wsg always passes `-R owner/repo` to `gh` so it works correctly inside jj workspaces (where gh's auto-detection fails). The repo is resolved from `pool.json` or the `origin` remote.

## Install

```bash
make install    # builds and installs to ~/.local/bin/wsg
```

## Usage

### Workspaces

```bash
wsg add <name> [-r <rev>]     # create workspace, print path to stdout
wsg rm [--force] <name>       # remove workspace
wsg list                      # list workspaces (alias: ls)
wsg clean                     # remove all non-default workspaces
wsg root                      # print repo root
wsg where                     # show repo and workspace paths
wsg path <name>               # print workspace path
wsg refresh                   # rebuild workspace cache
```

### Worker pool

A pool of jj workspaces that can run coding agents in parallel.

```bash
wsg pool <N>                  # set pool size (creates pool if needed)
wsg pool list                 # show pool status
wsg pool rm <worker>          # remove a worker (must not be busy)
wsg pool reset <worker>       # reset a worker to idle
wsg pool destroy              # tear down all workers and remove pool
wsg status                    # alias for pool list
```

### Dispatch

Assign GitHub issues to idle workers. Workers run the configured agent, read the issue, implement the work in their workspace, and open a PR.

```bash
wsg dispatch <TICKET>...             # assign ticket(s) to idle workers (background)
wsg dispatch <TICKET> --fg           # assign and watch in foreground
wsg dispatch --all                   # dispatch all ready-for-agent tickets
wsg dispatch --all --label <LABEL>   # filter by label (default: ready-for-agent)
wsg dispatch --model <MODEL>         # override the selected agent's model
wsg dispatch --budget <USD>          # max spend per worker (default: 20)
```

Parent issues with sub-issues are detected automatically and dispatched in dependency order, producing stacked PRs. Use `--no-orchestrate` to skip this and dispatch as a single ticket.

```bash
wsg send <worker> "<prompt>"  # resume worker session with a follow-up prompt
wsg review <worker>           # address PR review comments in worker session
wsg mount <worker>            # open worker in kitty (agent + two shells)
wsg logs <worker>             # tail a worker's log file
```

### TUI

Running `wsg` with no arguments in a TTY with an active pool launches a Bubbletea TUI showing pool status, live log tailing, and an input prompt for sending messages to workers.

### Shell completion

```bash
# zsh - add to .zshrc
eval "$(wsg completion zsh)"
```

## Agent runtime

wsg supports [Claude Code](https://docs.anthropic.com/en/docs/claude-code) and [Codex](https://developers.openai.com/codex/cli) as headless agent runtimes. Configure the runtime in the repository's `.jj/pool.json`:

```json
{
  "agent": "codex"
}
```

The field accepts `claude` or `codex` and defaults to `claude` when omitted. `cx` is a shell alias, so wsg invokes the real `codex` executable. Claude defaults to `opus`; Codex inherits its configured model unless `wsg dispatch --model` overrides it.

The selected runtime also performs Linear ticket discovery and dependency queries. Codex therefore requires an authenticated Linear MCP server in its configuration. Background workers run without approval prompts, so the Linear MCP policy must allow the ticket updates used by the dispatch workflow.

Agent Sessions enable in-session delegation when the installed Agent Runtime supports it. Codex is launched with `multi_agent` when `codex features list` exposes that feature, and Claude Code forwards subagent text when its CLI exposes `--forward-subagent-text`. Capability probes are best-effort: an unavailable probe or optional flag does not block the Run, and the short-lived Linear query path does not enable delegation.

A fresh Agent Session and every Follow-up receive the same delegation contract. Subagents may perform independent exploration, documentation lookup, test or log analysis, and review, but they must not edit tracked files or run jj. The top-level Agent Runtime remains responsible for tracked edits, jj operations, verification, and delivery. Delegation stays inside the Agent Session, cannot nest, and must finish before the Run finishes. Runtime concurrency and token limits remain provider-owned and do not consume extra Worker Pool slots.

Structured logs associate concurrent Claude Code messages through `parent_tool_use_id` and show Codex collaboration lifecycle details, including sender and receiver IDs, prompts, per-agent states, completion, and failure. Worker Status still follows only the top-level Agent Runtime result. Reset terminates the existing runtime process group, including child processes; provider task IDs and detached sessions are not persisted.

### Dispatch workflow

When you run `wsg dispatch TICKET-123`:

1. An idle worker is claimed and its workspace is prepared (rebased onto trunk)
2. wsg constructs a prompt that tells the agent to fetch the ticket via Linear MCP, read the codebase, implement the change, run checks, push a branch with `jj git push`, and open a PR with `gh`
3. The agent runs in the worker's workspace directory and streams structured JSON events to its log
4. The worker state file tracks the runtime, PID, status, ticket, and branch name
5. On completion the worker moves to `done` or `failed`

The agent has access to Linear and GitHub tools for reading tickets and managing PRs, but all version control goes through `jj` - never `git`.

### Orchestration

Parent issues with sub-issues are automatically detected. wsg uses the configured agent to resolve the dependency graph, then dispatches sub-issues in waves:

- Sub-issues with no blockers dispatch first
- Each subsequent wave bases its workspace on the prerequisite branch, producing stacked PRs
- Failed workers are retried once automatically
- The dispatch group state is persisted so progress survives restarts

### Session resume

Every agent session ID is extracted from the worker's structured log. The agent used for the original workload is persisted with the worker so follow-up interactions keep the same runtime even if `pool.json` changes:

- `wsg send <worker> "prompt"` - resume the session with a new instruction
- `wsg review <worker>` - fetches unresolved PR review comments and asks the agent to address them
- `wsg mount <worker>` - opens the worker in a kitty terminal with the agent session and two shell panes

### TUI

Running `wsg` with no arguments in a TTY with an active pool launches a Bubbletea TUI. It shows live worker status, lets you dispatch tickets, tail logs, send follow-up prompts, trigger PR reviews, and reset workers - all without leaving the terminal.

## Environment

| Variable | Description | Default |
|----------|-------------|---------|
| `JJ_WS_DIR` | Base directory for workspaces | `../<repo-name>-workspaces/` |

## License

[AGPL-3.0](LICENSE)
