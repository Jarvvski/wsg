package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestRunClaudeFGSuccess(t *testing.T) {
	dir := t.TempDir()
	poolDir := filepath.Join(dir, ".jj", "pool")
	os.MkdirAll(poolDir, 0755)

	sf := filepath.Join(poolDir, "worker-1.json")
	h, _ := CreateIdleWorker(sf)
	h.Dispatch("AMBA-42", filepath.Join(poolDir, "worker-1.log"), "amba-42")

	logFile := filepath.Join(dir, "test.log")
	h.RunFG(dir, logFile, []string{"true"})

	if h.State().Status != "done" {
		t.Errorf("status = %q, want done", h.State().Status)
	}
	if h.State().ExitCode == nil || *h.State().ExitCode != 0 {
		t.Errorf("exitCode = %v, want 0", h.State().ExitCode)
	}
	if h.State().CompletedAt == nil {
		t.Error("completedAt should be set")
	}
}

func TestRunClaudeFGFailure(t *testing.T) {
	dir := t.TempDir()
	poolDir := filepath.Join(dir, ".jj", "pool")
	os.MkdirAll(poolDir, 0755)

	sf := filepath.Join(poolDir, "worker-1.json")
	h, _ := CreateIdleWorker(sf)
	h.Dispatch("AMBA-42", filepath.Join(poolDir, "worker-1.log"), "amba-42")

	logFile := filepath.Join(dir, "test.log")
	h.RunFG(dir, logFile, []string{"false"})

	if h.State().Status != "failed" {
		t.Errorf("status = %q, want failed", h.State().Status)
	}
	if h.State().ExitCode == nil || *h.State().ExitCode != 1 {
		t.Errorf("exitCode = %v, want 1", h.State().ExitCode)
	}
}

func TestRunClaudeBGSuccess(t *testing.T) {
	dir := t.TempDir()
	poolDir := filepath.Join(dir, ".jj", "pool")
	os.MkdirAll(poolDir, 0755)

	sf := filepath.Join(poolDir, "worker-1.json")
	logFile := filepath.Join(poolDir, "worker-1.log")
	h, _ := CreateIdleWorker(sf)
	h.Dispatch("AMBA-42", logFile, "amba-42")

	r := &RepoContext{Root: dir}
	cmd := []string{"sh", "-c", `echo '{"type":"result","subtype":"success","is_error":false}'`}
	pid, err := h.RunBG(r, "worker-1", dir, logFile, cmd)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if pid <= 0 {
		t.Errorf("pid = %d, want > 0", pid)
	}

	loaded := awaitTerminal(t, sf)
	if loaded.Status != "done" {
		t.Errorf("status = %q, want done", loaded.Status)
	}
	if loaded.ExitCode == nil || *loaded.ExitCode != 0 {
		t.Errorf("exitCode = %v, want 0", loaded.ExitCode)
	}
}

// TestRunClaudeBGFailure covers a run that exits 0 but reports is_error in its
// final result event (e.g. an execution error). The worker must land on failed,
// not done, with the reported subtype as the error.
func TestRunClaudeBGFailure(t *testing.T) {
	dir := t.TempDir()
	poolDir := filepath.Join(dir, ".jj", "pool")
	os.MkdirAll(poolDir, 0755)

	sf := filepath.Join(poolDir, "worker-1.json")
	logFile := filepath.Join(poolDir, "worker-1.log")
	h, _ := CreateIdleWorker(sf)
	h.Dispatch("AMBA-42", logFile, "amba-42")

	r := &RepoContext{Root: dir}
	cmd := []string{"sh", "-c", `echo '{"type":"result","subtype":"error_during_execution","is_error":true}'`}
	if _, err := h.RunBG(r, "worker-1", dir, logFile, cmd); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	loaded := awaitTerminal(t, sf)
	if loaded.Status != "failed" {
		t.Errorf("status = %q, want failed", loaded.Status)
	}
	if loaded.Error == nil || *loaded.Error != "error_during_execution" {
		t.Errorf("error = %v, want error_during_execution", loaded.Error)
	}
}

// awaitTerminal polls a worker state file until it leaves the busy state.
func awaitTerminal(t *testing.T, sf string) *WorkerState {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		loaded, _ := loadWorkerState(sf)
		if loaded != nil && loaded.Status != "busy" {
			return loaded
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("timed out waiting for background completion")
	return nil
}

func TestUnwrapClaudeJSON(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "direct JSON",
			input: `{"tickets": ["AMBA-1"]}`,
			want:  `{"tickets": ["AMBA-1"]}`,
		},
		{
			name:  "wrapped in result",
			input: `{"result": "{\"tickets\": [\"AMBA-42\"]}"}`,
			want:  `{"tickets": ["AMBA-42"]}`,
		},
		{
			name:  "result with markdown",
			input: `{"result": "Here is the result:\n{\"key\": \"value\"}"}`,
			want:  `{"key": "value"}`,
		},
		{
			name:  "plain text",
			input: `not json`,
			want:  `not json`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := unwrapClaudeJSON(tt.input)
			if got != tt.want {
				t.Errorf("unwrapClaudeJSON(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestShellQuote(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"hello", "'hello'"},
		{"hello world", "'hello world'"},
		{"it's", "'it'\\''s'"},
		{"", "''"},
		{"a'b'c", "'a'\\''b'\\''c'"},
		{"$(rm -rf /)", "'$(rm -rf /)'"},
		{"; echo pwned", "'; echo pwned'"},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := shellQuote(tt.input)
			if got != tt.want {
				t.Errorf("shellQuote(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}
