# wsg

A workspace manager for [Jujutsu](https://jj-vcs.github.io/jj/) repos. Manages workspaces, a worker pool, and dispatches GitHub issues to Claude Code agents.

## Install

Requires Go 1.26+.

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

A pool of jj workspaces that can run Claude Code agents in parallel.

```bash
wsg pool <N>                  # set pool size (creates pool if needed)
wsg pool list                 # show pool status
wsg pool rm <worker>          # remove a worker (must not be busy)
wsg pool reset <worker>       # reset a worker to idle
wsg pool destroy              # tear down all workers and remove pool
wsg status                    # alias for pool list
```

### Dispatch

Assign GitHub issues to idle workers. Workers run Claude Code agents that read the issue, implement the work in their workspace, and open a PR.

```bash
wsg dispatch <TICKET>...             # assign ticket(s) to idle workers (background)
wsg dispatch <TICKET> --fg           # assign and watch in foreground
wsg dispatch --all                   # dispatch all ready-for-agent tickets
wsg dispatch --all --label <LABEL>   # filter by label (default: ready-for-agent)
wsg dispatch --model <MODEL>         # model for agents (default: opus)
wsg dispatch --budget <USD>          # max spend per worker (default: 20)
```

Parent issues with sub-issues are detected automatically and dispatched in dependency order, producing stacked PRs. Use `--no-orchestrate` to skip this and dispatch as a single ticket.

```bash
wsg send <worker> "<prompt>"  # resume worker session with a follow-up prompt
wsg review <worker>           # address PR review comments in worker session
wsg mount <worker>            # open worker in kitty (claude + two shells)
wsg logs <worker>             # tail a worker's log file
```

### TUI

Running `wsg` with no arguments in a TTY with an active pool launches a Bubbletea TUI showing pool status, live log tailing, and an input prompt for sending messages to workers.

### Shell completion

```bash
# zsh - add to .zshrc
eval "$(wsg completion zsh)"
```

## Environment

| Variable | Description | Default |
|----------|-------------|---------|
| `JJ_WS_DIR` | Base directory for workspaces | `../<repo-name>-workspaces/` |

## External dependencies

wsg shells out to: `jj`, `claude`, `mise`, `gh`, `rsync`, `tail`.

## License

[AGPL-3.0](LICENSE)
