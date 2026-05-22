package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestWorkerStateJSONRoundTrip(t *testing.T) {
	ws := newIdleWorkerState()
	dir := t.TempDir()
	path := filepath.Join(dir, "worker-1.json")

	if err := saveWorkerState(path, ws); err != nil {
		t.Fatalf("save: %v", err)
	}

	// Verify raw JSON has null fields (not omitted)
	raw, _ := os.ReadFile(path)
	var m map[string]any
	json.Unmarshal(raw, &m)

	for _, key := range []string{"ticket", "pid", "started_at", "completed_at", "log_file", "branch_name", "exit_code", "error"} {
		val, exists := m[key]
		if !exists {
			t.Errorf("key %q missing from JSON", key)
		} else if val != nil {
			t.Errorf("key %q should be null, got %v", key, val)
		}
	}
	if m["status"] != "idle" {
		t.Errorf("status = %v, want idle", m["status"])
	}

	loaded, err := loadWorkerState(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if loaded.Status != "idle" {
		t.Errorf("loaded status = %q, want idle", loaded.Status)
	}
	if loaded.Ticket != nil {
		t.Errorf("loaded ticket should be nil")
	}
}

func TestWorkerStateBusyRoundTrip(t *testing.T) {
	ticket := "AMBA-42"
	pid := 12345
	startedAt := "2026-05-20T14:13:49Z"
	logFile := "/path/to/worker-1.log"
	branch := "amba-42"

	ws := &WorkerState{
		Status:    "busy",
		Ticket:    &ticket,
		PID:       &pid,
		StartedAt: &startedAt,
		LogFile:   &logFile,
		BranchName: &branch,
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "worker-1.json")
	saveWorkerState(path, ws)

	loaded, err := loadWorkerState(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if loaded.Status != "busy" {
		t.Errorf("status = %q, want busy", loaded.Status)
	}
	if loaded.Ticket == nil || *loaded.Ticket != "AMBA-42" {
		t.Errorf("ticket = %v, want AMBA-42", loaded.Ticket)
	}
	if loaded.PID == nil || *loaded.PID != 12345 {
		t.Errorf("pid = %v, want 12345", loaded.PID)
	}
	if loaded.CompletedAt != nil {
		t.Errorf("completed_at should be nil")
	}
	if loaded.ExitCode != nil {
		t.Errorf("exit_code should be nil")
	}
}

func TestPoolConfigRoundTrip(t *testing.T) {
	cfg := &PoolConfig{
		Size:      4,
		GHRepo:    "AmebaAI/mono.git",
		Workers:   []string{"worker-1", "worker-2", "worker-3", "worker-4"},
		CreatedAt: "2026-05-20T14:09:02Z",
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "pool.json")

	if err := savePoolConfig(path, cfg); err != nil {
		t.Fatalf("save: %v", err)
	}

	loaded, err := loadPoolConfig(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if loaded.Size != 4 {
		t.Errorf("size = %d, want 4", loaded.Size)
	}
	if loaded.GHRepo != "AmebaAI/mono.git" {
		t.Errorf("gh_repo = %q, want AmebaAI/mono.git", loaded.GHRepo)
	}
	if len(loaded.Workers) != 4 {
		t.Errorf("workers count = %d, want 4", len(loaded.Workers))
	}
}

func TestGenerateWorkerName(t *testing.T) {
	name := generateWorkerName()
	if len(name) != 6 {
		t.Errorf("expected 6-char hex, got %q (len %d)", name, len(name))
	}
	name2 := generateWorkerName()
	if name == name2 {
		t.Errorf("expected unique names, got %q twice", name)
	}
}

func TestElapsedDisplay(t *testing.T) {
	tests := []struct {
		name        string
		startedAt   string
		completedAt *string
		wantPrefix  string
	}{
		{
			name:       "completed run",
			startedAt:  "2026-05-20T14:00:00Z",
			completedAt: strPtr("2026-05-20T14:07:55Z"),
			wantPrefix: "7m 55s",
		},
		{
			name:       "zero duration",
			startedAt:  "2026-05-20T14:00:00Z",
			completedAt: strPtr("2026-05-20T14:00:00Z"),
			wantPrefix: "0m 0s",
		},
		{
			name:       "invalid start",
			startedAt:  "not-a-date",
			completedAt: nil,
			wantPrefix: "-",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := elapsedDisplay(tt.startedAt, tt.completedAt)
			if got != tt.wantPrefix {
				t.Errorf("got %q, want %q", got, tt.wantPrefix)
			}
		})
	}
}

func TestFindIdleWorker(t *testing.T) {
	dir := t.TempDir()
	poolDir := filepath.Join(dir, ".jj", "pool")
	os.MkdirAll(poolDir, 0755)

	r := &RepoContext{Root: dir, BaseDir: dir + "-workspaces"}

	cfg := &PoolConfig{
		Size:    2,
		Workers: []string{"worker-1", "worker-2"},
	}
	savePoolConfig(r.poolConfigFile(), cfg)

	busy := &WorkerState{Status: "busy"}
	saveWorkerState(filepath.Join(poolDir, "worker-1.json"), busy)

	idle := newIdleWorkerState()
	saveWorkerState(filepath.Join(poolDir, "worker-2.json"), idle)

	got, err := findIdleWorker(r)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "worker-2" {
		t.Errorf("got %q, want worker-2", got)
	}
}

func strPtr(s string) *string { return &s }
