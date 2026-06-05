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

## Landing a change

One commit per focused fix/change. **Land automatically when tests pass and `make install` runs clean - don't ask first.** Run:

```bash
# 1. If the change is user-visible, bump Version in cmd/wsg/version.go AND
#    prepend an entry to CHANGELOG.md under the new version (see below)
jj describe -m "<imperative one-liner>"   # name the just-finished change
jj bookmark set main --to @               # advance local main to that commit
jj new                                    # ALWAYS follow main movement with a fresh empty @
```

**Invariant:** every time `main` moves, the very next command is `jj new`. `@` must never *be* the main commit - it must sit one commit above main, empty, ready for the next change. This applies to any workflow that moves `main`, not just this one. If you find `@` on main, immediately `jj new` before editing anything.

The working copy is always a commit in jj, so "landing" means giving it a description and moving `main` forward. Don't pile unrelated work into one commit - if the next idea is different, it's a new `jj describe` + `jj new` cycle. Commit messages follow the style in `jj log` (short imperative: "Add X", "Fix Y", "Unify Z").

### Versioning and changelog updates

wsg uses **semver**, stored as the `Version` constant in `cmd/wsg/version.go` and printed by `wsg version`. Before `jj describe` for a user-visible change, bump the version AND prepend a CHANGELOG.md entry under the new version heading. Both edits ride in the same commit as the change.

**User-visible means** a user of the CLI, TUI, or `jj-wsx` would notice: new commands or flags, behavior changes, bug fixes that change observable output, removed features, output-format changes. Skip both the version bump and the changelog entry for pure refactors, test-only changes, doc tweaks, dependency bumps without behavior change, or anything purely internal.

**Bump rule:**
- **PATCH** (e.g. 0.1.0 → 0.1.1) when the change only adds `### Fixed` entries.
- **MINOR** (e.g. 0.1.1 → 0.2.0) when the change adds anything to `### Added`, `### Changed`, or `### Removed`.
- **MAJOR** (1.0.0+) is **locked off** - never bump without explicit owner approval in the conversation. If a change would warrant MAJOR under strict semver (incompatible removal, format break), still bump as MINOR and flag it for the owner; do not unilaterally cross to 1.0.0.

**CHANGELOG format:** under a version heading (`## X.Y.Z - YYYY-MM-DD` - create if the bump is new, otherwise extend the existing heading for the version-in-progress), add a bullet under `### Added`, `### Changed`, `### Fixed`, or `### Removed`. Lead with a **bolded one-line summary**, then a sentence of detail, then a parenthesised short commit hash if the change references a previously landed commit (omit for the change you're about to commit - the hash isn't known yet). User-facing wording - what they'll see, not how it was implemented.

## Agent skills

### Issue tracker

GitHub Issues on `Jarvvski/wsg`. See `docs/agents/issue-tracker.md`.

### Triage labels

Default vocabulary (needs-triage, needs-info, ready-for-agent, ready-for-human, wontfix). See `docs/agents/triage-labels.md`.

### Domain docs

Single-context layout (one `CONTEXT.md` + `docs/adr/` at root). See `docs/agents/domain.md`.
