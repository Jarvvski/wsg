# Issue tracker: GitHub

Issues and PRDs for this repo live as GitHub issues on `Jarvvski/wsg`. Use the `gh` CLI for all operations, passing `-R Jarvvski/wsg` when not inside a git clone.

## Conventions

- **Create an issue**: `gh issue create -R Jarvvski/wsg --title "..." --body "..."`. Use a heredoc for multi-line bodies.
- **Read an issue**: `gh issue view <number> -R Jarvvski/wsg --comments`, filtering comments by `jq` and also fetching labels.
- **List issues**: `gh issue list -R Jarvvski/wsg --state open --json number,title,body,labels,comments --jq '[.[] | {number, title, body, labels: [.labels[].name], comments: [.comments[].body]}]'` with appropriate `--label` and `--state` filters.
- **Comment on an issue**: `gh issue comment <number> -R Jarvvski/wsg --body "..."`
- **Apply / remove labels**: `gh issue edit <number> -R Jarvvski/wsg --add-label "..."` / `--remove-label "..."`
- **Close**: `gh issue close <number> -R Jarvvski/wsg --comment "..."`

Infer the repo from `jj git remote list` - `gh` may not auto-detect in jj workspaces, so always pass `-R`.

## When a skill says "publish to the issue tracker"

Create a GitHub issue.

## When a skill says "fetch the relevant ticket"

Run `gh issue view <number> -R Jarvvski/wsg --comments`.
