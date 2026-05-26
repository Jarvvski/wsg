package main

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestClaudeArgsDispatch(t *testing.T) {
	inv := claudeInvocation{
		Model:        "opus",
		Budget:       "20",
		Name:         "pool:worker-abc:AMBA-42",
		SystemPrompt: "you are an agent",
		Prompt:       "implement the thing",
	}
	args := inv.Args()

	if args[0] != "-p" {
		t.Errorf("first arg = %q, want -p", args[0])
	}
	if !slices.Contains(args, "--model") {
		t.Error("missing --model")
	}
	if idx := slices.Index(args, "--model"); args[idx+1] != "opus" {
		t.Errorf("--model value = %q, want opus", args[idx+1])
	}
	if !slices.Contains(args, "--name") {
		t.Error("missing --name for dispatch")
	}
	if !slices.Contains(args, "--append-system-prompt") {
		t.Error("missing --append-system-prompt")
	}
	if !slices.Contains(args, "--verbose") {
		t.Error("missing --verbose")
	}
	if args[len(args)-1] != "implement the thing" {
		t.Errorf("last arg = %q, want prompt", args[len(args)-1])
	}
	if slices.Contains(args, "--resume") {
		t.Error("dispatch should not have --resume")
	}
}

func TestClaudeArgsResume(t *testing.T) {
	inv := claudeInvocation{
		Budget:    "5",
		SessionID: "sess-abc-123",
		Prompt:    "fix the tests",
	}
	args := inv.Args()

	if !slices.Contains(args, "--resume") {
		t.Error("missing --resume")
	}
	if idx := slices.Index(args, "--resume"); args[idx+1] != "sess-abc-123" {
		t.Errorf("--resume value = %q, want sess-abc-123", args[idx+1])
	}
	if !slices.Contains(args, "--fork-session") {
		t.Error("missing --fork-session")
	}
	if slices.Contains(args, "--model") {
		t.Error("resume should not have --model")
	}
	if slices.Contains(args, "--append-system-prompt") {
		t.Error("resume should not have --append-system-prompt")
	}
	if slices.Contains(args, "--name") {
		t.Error("resume should not have --name")
	}
}

func TestClaudeArgsFreshSend(t *testing.T) {
	inv := claudeInvocation{
		Budget:       "5",
		SystemPrompt: "you are an agent",
		Prompt:       "do the thing",
	}
	args := inv.Args()

	if !slices.Contains(args, "--append-system-prompt") {
		t.Error("fresh send should have --append-system-prompt")
	}
	if slices.Contains(args, "--resume") {
		t.Error("fresh send should not have --resume")
	}
	if slices.Contains(args, "--model") {
		t.Error("fresh send should not have --model (no model set)")
	}
}

func TestSyncDispatchGroup(t *testing.T) {
	dir := t.TempDir()
	poolDir := filepath.Join(dir, ".jj", "pool")
	os.MkdirAll(poolDir, 0755)

	r := &RepoContext{Root: dir, BaseDir: dir + "-workspaces"}

	worker := "worker-1"
	branch := "adam/amba-10-thing"
	dg := &DispatchGroup{
		Parent: "AMBA-9",
		SubIssues: map[string]*SubIssueState{
			"AMBA-10": {
				Title:  "Do the thing",
				Status: "dispatched",
				Worker: &worker,
			},
		},
	}

	ws := &WorkerState{Status: "done", BranchName: &branch}
	saveWorkerState(filepath.Join(poolDir, worker+".json"), ws)

	dgFile := filepath.Join(poolDir, "dispatch-amba-9.json")
	saveDispatchGroup(dgFile, dg)

	synced := syncExistingGroup(r, dgFile)
	if synced == nil {
		t.Fatal("expected non-nil group")
	}
	if synced.SubIssues["AMBA-10"].Status != "done" {
		t.Errorf("sub-issue status = %q, want done", synced.SubIssues["AMBA-10"].Status)
	}
}

func TestSyncDispatchGroupMissing(t *testing.T) {
	dir := t.TempDir()
	poolDir := filepath.Join(dir, ".jj", "pool")
	os.MkdirAll(poolDir, 0755)

	r := &RepoContext{Root: dir, BaseDir: dir + "-workspaces"}

	synced := syncExistingGroup(r, filepath.Join(poolDir, "dispatch-nope.json"))
	if synced != nil {
		t.Error("expected nil for missing group file")
	}
}

func TestSendSystemPrompt(t *testing.T) {
	prompt := sendSystemPrompt("owner/repo")
	if prompt == "" {
		t.Error("expected non-empty prompt")
	}
	if !strings.Contains(prompt, "owner/repo") {
		t.Error("prompt should contain repo name")
	}
	if !strings.Contains(prompt, "jj") {
		t.Error("prompt should mention jj")
	}
}

func TestParseTicketResponse(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  []string
	}{
		{
			name:  "direct JSON",
			input: `{"tickets": ["AMBA-1", "AMBA-2", "AMBA-3"]}`,
			want:  []string{"AMBA-1", "AMBA-2", "AMBA-3"},
		},
		{
			name:  "empty tickets",
			input: `{"tickets": []}`,
			want:  nil,
		},
		{
			name:  "invalid JSON",
			input: `not json at all`,
			want:  nil,
		},
		{
			name:  "result with no tickets key",
			input: `{"result": "{\"something\": \"else\"}"}`,
			want:  nil,
		},
		{
			name:  "single ticket",
			input: `{"tickets": ["AMBA-99"]}`,
			want:  []string{"AMBA-99"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseTicketResponse(tt.input)
			if len(got) != len(tt.want) {
				t.Fatalf("got %d tickets, want %d: %v", len(got), len(tt.want), got)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("ticket %d: got %q, want %q", i, got[i], tt.want[i])
				}
			}
		})
	}
}
