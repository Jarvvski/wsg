package main

import (
	"fmt"
	"strings"
)

type AgentKind string

const (
	AgentClaude AgentKind = "claude"
	AgentCodex  AgentKind = "codex"
)

const delegationRules = `Delegated work is read-only.

- Use in-session background tasks or subagents only for independent exploration, documentation lookup, test or log analysis, or review.
- Explicitly tell every subagent not to edit tracked files or run jj commands.
- Do not use detached sessions, nested delegation, or worktree or workspace creation.
- Await all delegated work before finishing.
- If delegation is unavailable or fails, continue the work directly.
- The main agent alone owns tracked edits, jj operations, verification, and delivery.`

func parseAgent(value string) (AgentKind, error) {
	agent := AgentKind(strings.ToLower(strings.TrimSpace(value)))
	if agent == "" {
		return AgentClaude, nil
	}
	switch agent {
	case AgentClaude, AgentCodex:
		return agent, nil
	default:
		return "", fmt.Errorf("invalid agent %q (expected claude or codex)", value)
	}
}

func configuredAgent(r *RepoContext) (AgentKind, error) {
	cfg, err := loadPoolConfig(r.poolConfigFile())
	if err != nil {
		return "", fmt.Errorf("load pool config: %w", err)
	}
	return parseAgent(string(cfg.Agent))
}

func agentPtr(agent AgentKind) *AgentKind {
	return &agent
}

type agentInvocation struct {
	Agent        AgentKind
	Model        string
	SessionID    string
	Name         string
	SystemPrompt string
	Prompt       string
	Capabilities agentCapabilities
}

type agentCapabilities struct {
	MultiAgent          bool
	ForwardSubagentText bool
}

func withDelegationRules(sessionID, systemPrompt, prompt string) (string, string) {
	if sessionID != "" {
		return "", delegationRules + "\n\n" + prompt
	}
	if systemPrompt != "" {
		systemPrompt += "\n\n"
	}
	return systemPrompt + delegationRules, prompt
}

func (inv agentInvocation) Command() (string, []string, error) {
	agent, err := parseAgent(string(inv.Agent))
	if err != nil {
		return "", nil, err
	}
	if agent == AgentClaude {
		systemPrompt, prompt := withDelegationRules(inv.SessionID, inv.SystemPrompt, inv.Prompt)
		claude := claudeInvocation{
			Model:               inv.Model,
			SessionID:           inv.SessionID,
			Name:                inv.Name,
			SystemPrompt:        systemPrompt,
			Prompt:              prompt,
			ForwardSubagentText: inv.Capabilities.ForwardSubagentText,
		}
		return "claude", claude.Args(), nil
	}

	args := []string{
		"--sandbox", "workspace-write",
		"--ask-for-approval", "never",
	}
	if inv.Model != "" {
		args = append(args, "--model", inv.Model)
	}
	if inv.Capabilities.MultiAgent {
		args = append(args, "--enable", "multi_agent")
	}
	args = append(args, "exec")
	systemPrompt, prompt := withDelegationRules(inv.SessionID, inv.SystemPrompt, inv.Prompt)
	if inv.SessionID != "" {
		args = append(args, "resume", "--json", "--skip-git-repo-check", inv.SessionID, prompt)
		return "codex", args, nil
	}
	prompt = systemPrompt + "\n\n" + prompt
	args = append(args, "--json", "--skip-git-repo-check", prompt)
	return "codex", args, nil
}

func agentQuery(dir string, agent AgentKind, prompt, allowedTools string) (string, error) {
	resolved, err := parseAgent(string(agent))
	if err != nil {
		return "", err
	}
	if resolved == AgentClaude {
		return claudeQuery(dir, prompt, allowedTools)
	}
	return codexQuery(dir, prompt)
}

func codexQuery(dir, prompt string) (string, error) {
	output, stderr, err := runCapture(dir, "codex",
		"--sandbox", "read-only",
		"--ask-for-approval", "never",
		"exec",
		"--ephemeral",
		"--skip-git-repo-check",
		prompt,
	)
	if err != nil {
		diag := stderr
		if diag == "" {
			diag = output
		}
		return "", fmt.Errorf("codex failed: %s", diag)
	}
	return extractJSON(output), nil
}
