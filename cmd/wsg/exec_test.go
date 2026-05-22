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
	ws := newIdleWorkerState()
	ws.MarkDispatched("AMBA-42", filepath.Join(poolDir, "worker-1.log"), "amba-42")
	saveWorkerState(sf, ws)

	logFile := filepath.Join(dir, "test.log")
	runClaudeFG(dir, logFile, sf, ws, []string{"true"})

	loaded, _ := loadWorkerState(sf)
	if loaded.Status != "done" {
		t.Errorf("status = %q, want done", loaded.Status)
	}
	if loaded.ExitCode == nil || *loaded.ExitCode != 0 {
		t.Errorf("exitCode = %v, want 0", loaded.ExitCode)
	}
	if loaded.CompletedAt == nil {
		t.Error("completedAt should be set")
	}
}

func TestRunClaudeFGFailure(t *testing.T) {
	dir := t.TempDir()
	poolDir := filepath.Join(dir, ".jj", "pool")
	os.MkdirAll(poolDir, 0755)

	sf := filepath.Join(poolDir, "worker-1.json")
	ws := newIdleWorkerState()
	ws.MarkDispatched("AMBA-42", filepath.Join(poolDir, "worker-1.log"), "amba-42")
	saveWorkerState(sf, ws)

	logFile := filepath.Join(dir, "test.log")
	runClaudeFG(dir, logFile, sf, ws, []string{"false"})

	loaded, _ := loadWorkerState(sf)
	if loaded.Status != "failed" {
		t.Errorf("status = %q, want failed", loaded.Status)
	}
	if loaded.ExitCode == nil || *loaded.ExitCode != 1 {
		t.Errorf("exitCode = %v, want 1", loaded.ExitCode)
	}
}

func TestRunClaudeBGSuccess(t *testing.T) {
	dir := t.TempDir()
	poolDir := filepath.Join(dir, ".jj", "pool")
	os.MkdirAll(poolDir, 0755)

	sf := filepath.Join(poolDir, "worker-1.json")
	ws := newIdleWorkerState()
	ws.MarkDispatched("AMBA-42", filepath.Join(poolDir, "worker-1.log"), "amba-42")
	saveWorkerState(sf, ws)

	logFile := filepath.Join(dir, "test.log")
	pid, err := runClaudeBG(dir, logFile, sf, ws, []string{"true"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if pid <= 0 {
		t.Errorf("pid = %d, want > 0", pid)
	}

	// Wait for background goroutine to complete
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		loaded, _ := loadWorkerState(sf)
		if loaded.Status != "busy" {
			if loaded.Status != "done" {
				t.Errorf("status = %q, want done", loaded.Status)
			}
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Error("timed out waiting for background completion")
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
