package main

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestParseAgent(t *testing.T) {
	for _, tt := range []struct {
		input string
		want  AgentKind
	}{
		{"", AgentClaude},
		{"claude", AgentClaude},
		{"CODEX", AgentCodex},
	} {
		got, err := parseAgent(tt.input)
		if err != nil {
			t.Fatalf("parseAgent(%q): %v", tt.input, err)
		}
		if got != tt.want {
			t.Errorf("parseAgent(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
	if _, err := parseAgent("cx"); err == nil {
		t.Fatal("expected cx to be rejected")
	}
}

func TestConfiguredAgent(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, ".jj"), 0755)
	r := &RepoContext{Root: dir}

	if err := savePoolConfig(r.poolConfigFile(), &PoolConfig{}); err != nil {
		t.Fatal(err)
	}
	agent, err := configuredAgent(r)
	if err != nil || agent != AgentClaude {
		t.Fatalf("default agent = %q, %v; want claude", agent, err)
	}

	if err := savePoolConfig(r.poolConfigFile(), &PoolConfig{Agent: AgentCodex}); err != nil {
		t.Fatal(err)
	}
	agent, err = configuredAgent(r)
	if err != nil || agent != AgentCodex {
		t.Fatalf("configured agent = %q, %v; want codex", agent, err)
	}

	if err := savePoolConfig(r.poolConfigFile(), &PoolConfig{Agent: "other"}); err != nil {
		t.Fatal(err)
	}
	if _, err := configuredAgent(r); err == nil {
		t.Fatal("expected invalid configured agent to fail")
	}
}

func TestResolveDispatchOptsUsesAgentDefaults(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, ".jj"), 0755)
	r := &RepoContext{Root: dir}
	actions := NewActions(r)

	if err := savePoolConfig(r.poolConfigFile(), &PoolConfig{Agent: AgentCodex}); err != nil {
		t.Fatal(err)
	}
	codexOpts, err := actions.resolveDispatchOpts(DispatchOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if codexOpts.Agent != AgentCodex || codexOpts.Model != "" {
		t.Errorf("Codex opts = %+v, want inherited model", codexOpts)
	}

	if err := savePoolConfig(r.poolConfigFile(), &PoolConfig{}); err != nil {
		t.Fatal(err)
	}
	claudeOpts, err := actions.resolveDispatchOpts(DispatchOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if claudeOpts.Agent != AgentClaude || claudeOpts.Model != "opus" {
		t.Errorf("Claude opts = %+v, want opus default", claudeOpts)
	}
}

func TestDelegationRulesCoverWorkerContract(t *testing.T) {
	for _, want := range []string{
		"independent exploration",
		"documentation lookup",
		"test or log analysis",
		"review",
		"not to edit tracked files or run jj commands",
		"detached sessions",
		"nested delegation",
		"worktree or workspace creation",
		"Await all delegated work",
		"unavailable or fails",
		"main agent alone owns tracked edits, jj operations, verification, and delivery",
	} {
		if !strings.Contains(delegationRules, want) {
			t.Errorf("delegation rules missing %q", want)
		}
	}
}

func TestCodexInvocationDispatch(t *testing.T) {
	inv := agentInvocation{
		Agent:        AgentCodex,
		SystemPrompt: "system rules",
		Prompt:       "implement it",
		Capabilities: agentCapabilities{MultiAgent: true},
	}
	name, args, err := inv.Command()
	if err != nil {
		t.Fatal(err)
	}
	if name != "codex" {
		t.Fatalf("name = %q, want codex", name)
	}
	for _, want := range []string{"--sandbox", "workspace-write", "--ask-for-approval", "never", "exec", "--json", "--skip-git-repo-check"} {
		if !slices.Contains(args, want) {
			t.Errorf("args missing %q: %v", want, args)
		}
	}
	if slices.Contains(args, "--model") {
		t.Errorf("Codex should inherit its configured model: %v", args)
	}
	if idx := slices.Index(args, "--enable"); idx == -1 || args[idx+1] != "multi_agent" {
		t.Errorf("Codex supported feature flag missing: %v", args)
	}
	if got, want := args[len(args)-1], "system rules\n\n"+delegationRules+"\n\nimplement it"; got != want {
		t.Errorf("combined prompt = %q, want %q", got, want)
	}
}

func TestCodexInvocationResume(t *testing.T) {
	inv := agentInvocation{
		Agent:        AgentCodex,
		Model:        "gpt-test",
		SessionID:    "thread-123",
		SystemPrompt: "must not be repeated",
		Prompt:       "continue",
		Capabilities: agentCapabilities{MultiAgent: true},
	}
	_, args, err := inv.Command()
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(args, "resume") || !slices.Contains(args, "thread-123") {
		t.Errorf("resume args missing session: %v", args)
	}
	if idx := slices.Index(args, "--model"); idx == -1 || args[idx+1] != "gpt-test" {
		t.Errorf("model override missing: %v", args)
	}
	if idx := slices.Index(args, "--enable"); idx == -1 || args[idx+1] != "multi_agent" {
		t.Errorf("resume supported feature flag missing: %v", args)
	}
	if strings.Contains(args[len(args)-1], "must not be repeated") {
		t.Errorf("resume repeated system prompt: %v", args)
	}
	if got, want := args[len(args)-1], delegationRules+"\n\ncontinue"; got != want {
		t.Errorf("resume prompt = %q, want %q", got, want)
	}
}

func TestClaudeInvocationIncludesDelegationRules(t *testing.T) {
	for _, tt := range []struct {
		name      string
		sessionID string
	}{
		{name: "fresh"},
		{name: "resumed", sessionID: "session-123"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			inv := agentInvocation{
				Agent:        AgentClaude,
				SessionID:    tt.sessionID,
				SystemPrompt: "dispatch rules",
				Prompt:       "do the work",
				Capabilities: agentCapabilities{ForwardSubagentText: true},
			}
			_, args, err := inv.Command()
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Contains(args, "--forward-subagent-text") {
				t.Errorf("Claude supported forwarding flag missing: %v", args)
			}
			if tt.sessionID == "" {
				idx := slices.Index(args, "--append-system-prompt")
				if idx == -1 || args[idx+1] != "dispatch rules\n\n"+delegationRules {
					t.Errorf("fresh system prompt is missing exact delegation contract: %v", args)
				}
				if args[len(args)-1] != "do the work" {
					t.Errorf("fresh workload prompt = %q", args[len(args)-1])
				}
			} else {
				if slices.Contains(args, "--append-system-prompt") {
					t.Errorf("resume repeated system prompt: %v", args)
				}
				if args[len(args)-1] != delegationRules+"\n\ndo the work" {
					t.Errorf("resume follow-up is not prefixed by delegation contract: %q", args[len(args)-1])
				}
			}
		})
	}
}

func TestProbeAgentCapabilitiesDetectsCodexMultiAgent(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "codex")
	if err := os.WriteFile(exe, []byte("#!/bin/sh\nprintf '%s\\n' 'multi_agent stable false'\n"), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	got := probeAgentCapabilities(dir, AgentCodex)
	if !got.MultiAgent {
		t.Errorf("capabilities = %+v, want multi_agent support", got)
	}
}

func TestProbeAgentCapabilitiesDetectsClaudeForwarding(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "claude")
	if err := os.WriteFile(exe, []byte("#!/bin/sh\nprintf '%s\\n' 'Usage: claude --forward-subagent-text'\n"), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	got := probeAgentCapabilities(dir, AgentClaude)
	if !got.ForwardSubagentText {
		t.Errorf("capabilities = %+v, want subagent forwarding support", got)
	}
}

func TestProbeAgentCapabilitiesFallsBackWhenProbeFails(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"claude", "codex"} {
		exe := filepath.Join(dir, name)
		if err := os.WriteFile(exe, []byte("#!/bin/sh\nexit 1\n"), 0755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	for _, agent := range []AgentKind{AgentClaude, AgentCodex} {
		if got := probeAgentCapabilities(dir, agent); got != (agentCapabilities{}) {
			t.Errorf("%s capabilities = %+v after failed probe, want none", agent, got)
		}
	}
}

func TestCodexQueryExtractsJSON(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "codex")
	argsFile := filepath.Join(dir, "args")
	if err := os.WriteFile(exe, []byte("#!/bin/sh\nprintf '%s\\n' \"$@\" > \"$ARGS_FILE\"\nprintf '%s\\n' 'result: {\"tickets\":[\"AMBA-1\"]}'\n"), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("ARGS_FILE", argsFile)

	got, err := codexQuery(dir, "find tickets")
	if err != nil {
		t.Fatal(err)
	}
	if got != `{"tickets":["AMBA-1"]}` {
		t.Errorf("got %q", got)
	}
	args, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"--sandbox\nread-only", "--ask-for-approval\nnever", "exec", "--ephemeral", "--skip-git-repo-check"} {
		if !strings.Contains(string(args), want) {
			t.Errorf("query args missing %q: %s", want, args)
		}
	}
	if strings.Contains(string(args), "multi_agent") {
		t.Errorf("query path should not enable delegation: %s", args)
	}
}
