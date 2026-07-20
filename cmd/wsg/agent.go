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
}

func (inv agentInvocation) Command() (string, []string, error) {
	agent, err := parseAgent(string(inv.Agent))
	if err != nil {
		return "", nil, err
	}
	if agent == AgentClaude {
		claude := claudeInvocation{
			Model:        inv.Model,
			SessionID:    inv.SessionID,
			Name:         inv.Name,
			SystemPrompt: inv.SystemPrompt,
			Prompt:       inv.Prompt,
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
	args = append(args, "exec")
	prompt := inv.Prompt
	if inv.SessionID != "" {
		args = append(args, "resume", "--json", "--skip-git-repo-check", inv.SessionID, prompt)
		return "codex", args, nil
	}
	if inv.SystemPrompt != "" {
		prompt = inv.SystemPrompt + "\n\n" + prompt
	}
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
