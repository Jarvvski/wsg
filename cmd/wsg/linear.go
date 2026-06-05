package main

import (
	"encoding/json"
	"fmt"
)

// linear.go is the single seam onto Linear-via-claude queries. Every Linear
// MCP prompt outside this file is a bug - call one of the verbs below
// instead.
//
// The verbs build the prompt, drive claudeQuery, and return parsed values.
// The Linear MCP allowed-tools list and the response shapes live here so
// callers do not re-state them.

const linearAllowedTools = "mcp__claude_ai_Linear__list_issues,mcp__claude_ai_Linear__get_issue"

// linearReadyTickets queries Linear for Todo-state issues with the given
// label on the Ameba team and returns the bare ticket identifiers.
func linearReadyTickets(r *RepoContext, label string) ([]string, error) {
	prompt := fmt.Sprintf(
		"Use the Linear MCP list_issues tool to find issues with label '%s' that are in 'Todo' state for the Ameba team. Return ONLY the issue identifiers (e.g. AMBA-42) as a JSON array in this exact format: {\"tickets\": [\"AMBA-1\", \"AMBA-2\"]}",
		label,
	)
	output, err := claudeQuery(r.Root, prompt, linearAllowedTools)
	if err != nil {
		return nil, err
	}
	return parseLinearTickets(output), nil
}

func parseLinearTickets(output string) []string {
	var payload struct {
		Tickets []string `json:"tickets"`
	}
	if err := json.Unmarshal([]byte(output), &payload); err != nil {
		return nil
	}
	return payload.Tickets
}

// linearSubIssueEntry is one child in the sub-issue graph returned by
// linearSubIssueGraph. The shape mirrors the prompt's JSON exactly;
// callers fold it into their own domain types (e.g. SubIssueState).
type linearSubIssueEntry struct {
	Title     string   `json:"title"`
	Status    string   `json:"status"`
	BlockedBy []string `json:"blocked_by"`
	CrossRepo bool     `json:"cross_repo"`
}

// linearSubIssueGraph fetches the parent-child sub-issue graph for parent.
// The returned map is keyed by direct-child ticket ID; an empty map means
// parent has no sub-issues. repo is the gh repo slug, embedded in the
// prompt so cross_repo detection has a reference point.
func linearSubIssueGraph(r *RepoContext, parent, repo string) (map[string]linearSubIssueEntry, error) {
	prompt := fmt.Sprintf(`Fetch the parent-child sub-issue graph for Linear issue %s.

Steps:
1. Call list_issues with parentId="%s" to enumerate the DIRECT CHILDREN of %s via the parent-child relationship.
2. If step 1 returns zero issues, respond with exactly: {"sub_issues": {}} and stop. Do not call any other tools.
3. Otherwise, for each child returned in step 1, call get_issue with includeRelations=true to read its blockedBy relations.
4. Return ONLY a JSON object (no markdown, no explanation) in this format:

{
  "sub_issues": {
    "AMBA-17": {
      "title": "Short title from issue",
      "status": "Backlog",
      "blocked_by": [],
      "cross_repo": false
    },
    "AMBA-18": {
      "title": "Short title from issue",
      "status": "In Progress",
      "blocked_by": ["AMBA-17"],
      "cross_repo": false
    }
  }
}

CRITICAL constraints (read carefully):
- Only include issues whose parent is %s (i.e. issues returned by step 1's list_issues call).
- Do NOT include %s itself in sub_issues.
- Do NOT include issues from %s's own blocks / blockedBy / relatedTo relations. Those are siblings or cousins of %s, not its children. A common mistake is to call get_issue on %s and treat its "blocks" list as sub-issues - never do that.
- blocked_by must contain ONLY sibling IDs that also appear as keys in sub_issues. Drop any blockedBy entry that is not a child of %s.
- status is the exact Linear status name (e.g. "Backlog", "Todo", "Planned", "In Progress", "In Review", "Done").
- cross_repo is true if the sub-issue targets a different codebase than %s (look for repo/service names in the title or description).
- Include ALL children from step 1 even if they have no blockers.`, parent, parent, parent, parent, parent, parent, parent, parent, parent, repo)

	output, err := claudeQuery(r.Root, prompt, linearAllowedTools)
	if err != nil {
		return nil, err
	}
	var resp struct {
		SubIssues map[string]linearSubIssueEntry `json:"sub_issues"`
	}
	if err := json.Unmarshal([]byte(output), &resp); err != nil {
		return nil, fmt.Errorf("failed to parse dependency graph: %v\nraw: %s", err, output)
	}
	return resp.SubIssues, nil
}
