# wsg - jj workspace manager

Go CLI that manages Jujutsu VCS workspaces, a worker pool, and dispatches Linear tickets to Claude agents.

## Project structure

```
cmd/wsg/
  main.go         Entry point + command dispatch
  repo.go         jj repo discovery (find .jj, resolve repo root, base dir)
  cache.go        .jj/ws-cache read/write/rebuild
  workspace.go    Workspace CRUD (add, rm, clean, list, where, path, root, refresh)
  pool.go         Pool config, worker state, resize, list, destroy, reset
  dispatch.go     Dispatch single/all, launch worker, prompt construction
  exec.go         Helpers for running external processes (jj, claude, mise, gh)
  color.go        TTY detection + ANSI color
```

## Build

```bash
make            # build binary
make install    # build + install to ~/.local/bin/wsg
```

## Key conventions

- Zero external dependencies - stdlib only (`encoding/json`, `os/exec`, `flag`, `syscall`)
- Single `package main` in `cmd/wsg/` - flat file layout, no internal packages
- stdout for machine-readable data (paths), stderr for human messages (`info()` helper)
- All JSON state files use pointer types (`*string`, `*int`) to marshal `null` correctly
- Atomic file writes via temp file + `os.Rename`

## Data files (in the jj repo's `.jj/` directory)

- `ws-cache` - tab-separated `name\tpath` per line
- `pool.json` - `{size, gh_repo, workers[], created_at}`
- `pool/worker-N.json` - `{status, ticket, pid, started_at, completed_at, log_file, branch_name, exit_code, error}`

These formats must stay compatible with `jj-wsx` (Bun TUI) which reads them directly.

## External commands

wsg shells out to: `jj`, `claude`, `mise`, `gh`, `rsync`, `tail`. Never use `git`.

## Testing changes

After modifying, always run `make install` and verify:
- `wsg list` - matches `jj-ws list` output
- `wsg status` - columns aligned, colors render on TTY
- `wsg pool reset <worker>` - resets to idle
- `wsg dispatch <TICKET> --fg` - launches claude agent in worker workspace
