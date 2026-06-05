package main

import (
	"fmt"
	"strings"
)

func sendSystemPrompt(repo string) string {
	return fmt.Sprintf(`You are an autonomous agent in a jj (Jujutsu VCS) workspace.

CRITICAL RULES:
- Use jj commands, NEVER git commands.
- The gh CLI requires: gh -R %s pr create ...
- To push your work: jj git push --named <branch>=@
- Do NOT ask questions. Make reasonable decisions and proceed.`, repo)
}

func buildDispatchSystemPrompt(repo, branchPrefix, ticketLower string, depCtx *DependencyContext) string {
	prompt := fmt.Sprintf(`You are an autonomous implementation agent in a jj (Jujutsu VCS) workspace.

CRITICAL RULES:
- Use jj commands, NEVER git commands.
- The gh CLI requires: gh -R %s pr create ...
- Branch naming: %s/%s-<short-description> (lowercase, hyphens, max 4 words from ticket title). Example: %s/amba-42-supplier-contact-sync
- To push your work: jj git push --named <branch>=@
- You have access to Linear MCP tools for fetching ticket details and updating status.
- Do NOT ask questions. Make reasonable decisions and proceed.
- If you encounter ambiguity, document your assumptions in the PR description.
- Do NOT add a "Generated with Claude Code" footer or any AI attribution to PRs, commits, or comments.`, repo, branchPrefix, ticketLower, branchPrefix)

	if depCtx != nil && depCtx.Context != "" {
		prompt += fmt.Sprintf(`

STACKED BRANCH: Your workspace is based on prerequisite work:
%s

CRITICAL: Do NOT rebase onto main. Your changes build on top of the prerequisite branch(es).
If you see merge conflict markers, resolve them before proceeding.`, depCtx.Context)
	}

	return prompt
}

func buildDispatchWorkerPrompt(ticketID, userEmail, branchPrefix, ticketLower, prCreateCmd string) string {
	return fmt.Sprintf(`Implement Linear ticket %s.

1. Fetch the ticket: use the Linear MCP get_issue tool with id "%s". Read the full description.
   The "Outcome" section defines your acceptance criteria - verify against it before finishing.

2. Claim the ticket: use the Linear MCP save_issue tool with id "%s" to set state "In Progress" and assignee "%s".

3. Derive a branch name from the ticket title in the format: %s/%s-<short-description>
   Use lowercase, hyphens, max 4 words from the title. Example: %s/amba-42-supplier-contact-sync

4. Read CLAUDE.md and relevant source files to understand the codebase and conventions.

5. Implement using TDD: invoke the /tdd skill with the ticket requirements as context.
   Let the skill drive the red-green-refactor loop until acceptance criteria are met.

6. After /tdd completes, run the full check suite: linting, type checking, and all tests. Fix any issues.

7. Describe your changes: jj describe -m "%s: <concise summary of what you implemented>"

8. Push: jj git push --named <branch>=@

9. Create a PR:
   %s

10. Update Linear:
    - Use the Linear MCP save_issue tool to move %s to "Reviewable" state
    - Use the Linear MCP save_comment tool to add a comment with: what was implemented, the PR URL, and any assumptions made`,
		ticketID, ticketID, ticketID, userEmail, branchPrefix, ticketLower, branchPrefix, ticketID, prCreateCmd, ticketID)
}

func buildReviewPrompt(repo string, prNumber int, prURL, branch string, failingChecks []ghCheck, hasConflicts bool) string {
	var b strings.Builder
	step := 1

	header := fmt.Sprintf("#%d", prNumber)
	if prURL != "" {
		header = fmt.Sprintf("%s (#%d)", prURL, prNumber)
	}
	b.WriteString(fmt.Sprintf("Review and address feedback on PR %s.\n\n", header))

	if hasConflicts {
		b.WriteString(fmt.Sprintf("%d. This PR has merge conflicts. Rebase onto trunk and resolve them:\n", step))
		b.WriteString("   jj rebase -d 'trunk()'\n")
		b.WriteString("   Then resolve any conflict markers in the affected files.\n")
		b.WriteString(fmt.Sprintf("   After resolving, push: jj git push --named %s=@\n\n", branch))
		step++
	}

	b.WriteString(fmt.Sprintf("%d. Fetch all review comments: gh -R %s pr view %d --comments\n", step, repo, prNumber))
	b.WriteString(fmt.Sprintf("   Also check inline review threads: gh api repos/%s/pulls/%d/comments --jq '.[] | {path, line, body, user: .user.login}'\n\n", repo, prNumber))
	step++

	b.WriteString(fmt.Sprintf("%d. For each unresolved comment:\n", step))
	b.WriteString("   - Understand the reviewer's feedback\n")
	b.WriteString("   - Make the requested change (or document why you disagree in the PR)\n")
	b.WriteString("   - If a comment is unclear, make a reasonable judgment call\n\n")
	step++

	if len(failingChecks) > 0 {
		b.WriteString(fmt.Sprintf("%d. Fix failing CI checks. These checks are FAILING:\n", step))
		for _, c := range failingChecks {
			b.WriteString(fmt.Sprintf("   - %s\n", c.Name))
		}
		b.WriteString("   Investigate and fix each failure:\n")
		b.WriteString(fmt.Sprintf("   - List failed runs: gh -R %s run list --branch %s --status failure --json databaseId,name --limit 5\n", repo, branch))
		b.WriteString(fmt.Sprintf("   - View failure logs: gh -R %s run view <run-id> --log-failed\n", repo))
		b.WriteString("   - Fix the root cause in the code\n\n")
		step++
	}

	suffix := ""
	if len(failingChecks) > 0 {
		suffix = " and CI failures"
	}
	b.WriteString(fmt.Sprintf("%d. After addressing all feedback%s, run checks: linting, type checking, and tests.\n\n", step, suffix))
	step++

	b.WriteString(fmt.Sprintf("%d. Describe and push:\n", step))
	b.WriteString("   jj describe -m \"<ticket>: address review feedback\"\n")
	b.WriteString(fmt.Sprintf("   jj git push --named %s=@\n\n", branch))
	step++

	b.WriteString(fmt.Sprintf("%d. Reply to the PR confirming what you addressed: gh -R %s pr comment %d --body \"<summary of changes made>\"", step, repo, prNumber))

	return b.String()
}
