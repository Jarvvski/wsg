package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestMarkDispatched(t *testing.T) {
	ws := newIdleWorkerState()
	ws.MarkDispatched("AMBA-42", "/tmp/worker-1.log", "amba-42")

	if ws.Status != "busy" {
		t.Errorf("status = %q, want busy", ws.Status)
	}
	if ws.Ticket == nil || *ws.Ticket != "AMBA-42" {
		t.Errorf("ticket = %v, want AMBA-42", ws.Ticket)
	}
	if ws.LogFile == nil || *ws.LogFile != "/tmp/worker-1.log" {
		t.Errorf("logFile = %v, want /tmp/worker-1.log", ws.LogFile)
	}
	if ws.BranchName == nil || *ws.BranchName != "amba-42" {
		t.Errorf("branchName = %v, want amba-42", ws.BranchName)
	}
	if ws.StartedAt == nil || *ws.StartedAt == "" {
		t.Error("startedAt should be set")
	}
	if ws.CompletedAt != nil {
		t.Error("completedAt should be nil")
	}
	if ws.ExitCode != nil {
		t.Error("exitCode should be nil")
	}
	if ws.Error != nil {
		t.Error("error should be nil")
	}
	if ws.PID != nil {
		t.Error("pid should be nil")
	}
}

func TestMarkResumed(t *testing.T) {
	ticket := "AMBA-42"
	branch := "adam/amba-42-fix-login"
	ws := &WorkerState{
		Status:     "done",
		Ticket:     &ticket,
		BranchName: &branch,
	}

	ws.MarkResumed("/tmp/worker-1.log")

	if ws.Status != "busy" {
		t.Errorf("status = %q, want busy", ws.Status)
	}
	if ws.Ticket == nil || *ws.Ticket != "AMBA-42" {
		t.Error("ticket should be preserved")
	}
	if ws.BranchName == nil || *ws.BranchName != "adam/amba-42-fix-login" {
		t.Error("branchName should be preserved")
	}
	if ws.LogFile == nil || *ws.LogFile != "/tmp/worker-1.log" {
		t.Errorf("logFile = %v, want /tmp/worker-1.log", ws.LogFile)
	}
	if ws.StartedAt == nil || *ws.StartedAt == "" {
		t.Error("startedAt should be set")
	}
	if ws.CompletedAt != nil {
		t.Error("completedAt should be cleared")
	}
	if ws.ExitCode != nil {
		t.Error("exitCode should be cleared")
	}
	if ws.Error != nil {
		t.Error("error should be cleared")
	}
}

func TestMarkDone(t *testing.T) {
	ws := newIdleWorkerState()
	ws.MarkDispatched("AMBA-42", "/tmp/w.log", "amba-42")

	ws.MarkDone(0)

	if ws.Status != "done" {
		t.Errorf("status = %q, want done", ws.Status)
	}
	if ws.CompletedAt == nil || *ws.CompletedAt == "" {
		t.Error("completedAt should be set")
	}
	if ws.ExitCode == nil || *ws.ExitCode != 0 {
		t.Errorf("exitCode = %v, want 0", ws.ExitCode)
	}
	if ws.Ticket == nil || *ws.Ticket != "AMBA-42" {
		t.Error("ticket should be preserved")
	}
}

func TestMarkFailed(t *testing.T) {
	ws := newIdleWorkerState()
	ws.MarkDispatched("AMBA-42", "/tmp/w.log", "amba-42")

	ws.MarkFailed(1, "process crashed")

	if ws.Status != "failed" {
		t.Errorf("status = %q, want failed", ws.Status)
	}
	if ws.CompletedAt == nil || *ws.CompletedAt == "" {
		t.Error("completedAt should be set")
	}
	if ws.ExitCode == nil || *ws.ExitCode != 1 {
		t.Errorf("exitCode = %v, want 1", ws.ExitCode)
	}
	if ws.Error == nil || *ws.Error != "process crashed" {
		t.Errorf("error = %v, want 'process crashed'", ws.Error)
	}
}

func TestSetPID(t *testing.T) {
	ws := newIdleWorkerState()
	ws.MarkDispatched("AMBA-42", "/tmp/w.log", "amba-42")

	ws.SetPID(12345)

	if ws.PID == nil || *ws.PID != 12345 {
		t.Errorf("pid = %v, want 12345", ws.PID)
	}
	if ws.Status != "busy" {
		t.Error("status should remain busy")
	}
}

func TestReset(t *testing.T) {
	ws := newIdleWorkerState()
	ws.MarkDispatched("AMBA-42", "/tmp/w.log", "amba-42")
	ws.SetPID(12345)
	ws.MarkDone(0)

	ws.Reset()

	if ws.Status != "idle" {
		t.Errorf("status = %q, want idle", ws.Status)
	}
	if ws.Ticket != nil {
		t.Error("ticket should be nil")
	}
	if ws.PID != nil {
		t.Error("pid should be nil")
	}
	if ws.StartedAt != nil {
		t.Error("startedAt should be nil")
	}
	if ws.CompletedAt != nil {
		t.Error("completedAt should be nil")
	}
	if ws.LogFile != nil {
		t.Error("logFile should be nil")
	}
	if ws.BranchName != nil {
		t.Error("branchName should be nil")
	}
	if ws.ExitCode != nil {
		t.Error("exitCode should be nil")
	}
	if ws.Error != nil {
		t.Error("error should be nil")
	}
}

func TestMarkDispatchedJSONRoundTrip(t *testing.T) {
	ws := newIdleWorkerState()
	ws.MarkDispatched("AMBA-42", "/tmp/w.log", "amba-42")
	ws.SetPID(9999)

	dir := t.TempDir()
	path := filepath.Join(dir, "worker.json")
	saveWorkerState(path, ws)

	raw, _ := os.ReadFile(path)
	var m map[string]any
	json.Unmarshal(raw, &m)

	if m["status"] != "busy" {
		t.Errorf("JSON status = %v, want busy", m["status"])
	}
	if m["ticket"] != "AMBA-42" {
		t.Errorf("JSON ticket = %v, want AMBA-42", m["ticket"])
	}
	if m["pid"] != float64(9999) {
		t.Errorf("JSON pid = %v, want 9999", m["pid"])
	}
	for _, key := range []string{"completed_at", "exit_code", "error"} {
		val, exists := m[key]
		if !exists {
			t.Errorf("key %q missing from JSON", key)
		} else if val != nil {
			t.Errorf("key %q should be null, got %v", key, val)
		}
	}

	loaded, _ := loadWorkerState(path)
	if loaded.Status != "busy" {
		t.Errorf("loaded status = %q, want busy", loaded.Status)
	}
	if loaded.PID == nil || *loaded.PID != 9999 {
		t.Errorf("loaded pid = %v, want 9999", loaded.PID)
	}
}

func TestResetJSONRoundTrip(t *testing.T) {
	ws := newIdleWorkerState()
	ws.MarkDispatched("AMBA-42", "/tmp/w.log", "amba-42")
	ws.MarkDone(0)
	ws.Reset()

	dir := t.TempDir()
	path := filepath.Join(dir, "worker.json")
	saveWorkerState(path, ws)

	raw, _ := os.ReadFile(path)
	var m map[string]any
	json.Unmarshal(raw, &m)

	if m["status"] != "idle" {
		t.Errorf("JSON status = %v, want idle", m["status"])
	}
	for _, key := range []string{"ticket", "pid", "started_at", "completed_at", "log_file", "branch_name", "exit_code", "error"} {
		val, exists := m[key]
		if !exists {
			t.Errorf("key %q missing from JSON", key)
		} else if val != nil {
			t.Errorf("key %q should be null, got %v", key, val)
		}
	}
}

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
	if len(name) != 13 || name[:7] != "worker-" {
		t.Errorf("expected worker-XXXXXX, got %q (len %d)", name, len(name))
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

// ── WorkerHandle tests ────────────────────────────────────────────

func TestOpenWorkerLoadsState(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "worker.json")

	ws := newIdleWorkerState()
	ws.MarkDispatched("AMBA-42", "/tmp/w.log", "amba-42")
	saveWorkerState(path, ws)

	h, err := OpenWorker(path)
	if err != nil {
		t.Fatalf("OpenWorker: %v", err)
	}
	if h.State().Status != "busy" {
		t.Errorf("status = %q, want busy", h.State().Status)
	}
	if h.State().Ticket == nil || *h.State().Ticket != "AMBA-42" {
		t.Errorf("ticket = %v, want AMBA-42", h.State().Ticket)
	}
}

func TestOpenWorkerMissingFile(t *testing.T) {
	_, err := OpenWorker("/nonexistent/worker.json")
	if err == nil {
		t.Fatal("expected error for missing file")
	}
}

func TestCreateIdleWorker(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "worker.json")

	h, err := CreateIdleWorker(path)
	if err != nil {
		t.Fatalf("CreateIdleWorker: %v", err)
	}
	if h.State().Status != "idle" {
		t.Errorf("status = %q, want idle", h.State().Status)
	}

	// Verify persisted to disk
	loaded, err := loadWorkerState(path)
	if err != nil {
		t.Fatalf("load after create: %v", err)
	}
	if loaded.Status != "idle" {
		t.Errorf("persisted status = %q, want idle", loaded.Status)
	}
}

func TestHandleDispatch(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "worker.json")

	h, _ := CreateIdleWorker(path)
	if err := h.Dispatch("AMBA-42", "/tmp/w.log", "amba-42"); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}

	if h.State().Status != "busy" {
		t.Errorf("status = %q, want busy", h.State().Status)
	}
	if h.State().Ticket == nil || *h.State().Ticket != "AMBA-42" {
		t.Errorf("ticket = %v, want AMBA-42", h.State().Ticket)
	}

	// Verify persisted
	loaded, _ := loadWorkerState(path)
	if loaded.Status != "busy" {
		t.Errorf("persisted status = %q, want busy", loaded.Status)
	}
	if loaded.Ticket == nil || *loaded.Ticket != "AMBA-42" {
		t.Errorf("persisted ticket = %v, want AMBA-42", loaded.Ticket)
	}
}

func TestHandleDone(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "worker.json")

	h, _ := CreateIdleWorker(path)
	h.Dispatch("AMBA-42", "/tmp/w.log", "amba-42")

	if err := h.Done(0); err != nil {
		t.Fatalf("Done: %v", err)
	}

	if h.State().Status != "done" {
		t.Errorf("status = %q, want done", h.State().Status)
	}
	if h.State().ExitCode == nil || *h.State().ExitCode != 0 {
		t.Errorf("exitCode = %v, want 0", h.State().ExitCode)
	}

	loaded, _ := loadWorkerState(path)
	if loaded.Status != "done" {
		t.Errorf("persisted status = %q, want done", loaded.Status)
	}
}

func TestHandleFailed(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "worker.json")

	h, _ := CreateIdleWorker(path)
	h.Dispatch("AMBA-42", "/tmp/w.log", "amba-42")

	if err := h.Failed(1, "process crashed"); err != nil {
		t.Fatalf("Failed: %v", err)
	}

	if h.State().Status != "failed" {
		t.Errorf("status = %q, want failed", h.State().Status)
	}
	if h.State().Error == nil || *h.State().Error != "process crashed" {
		t.Errorf("error = %v, want 'process crashed'", h.State().Error)
	}

	loaded, _ := loadWorkerState(path)
	if loaded.Status != "failed" {
		t.Errorf("persisted status = %q, want failed", loaded.Status)
	}
}

func TestHandleResume(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "worker.json")

	h, _ := CreateIdleWorker(path)
	h.Dispatch("AMBA-42", "/tmp/w.log", "amba-42")
	h.Done(0)

	if err := h.Resume("/tmp/w2.log"); err != nil {
		t.Fatalf("Resume: %v", err)
	}

	if h.State().Status != "busy" {
		t.Errorf("status = %q, want busy", h.State().Status)
	}
	if h.State().Ticket == nil || *h.State().Ticket != "AMBA-42" {
		t.Error("ticket should be preserved after resume")
	}
	if h.State().LogFile == nil || *h.State().LogFile != "/tmp/w2.log" {
		t.Errorf("logFile = %v, want /tmp/w2.log", h.State().LogFile)
	}

	loaded, _ := loadWorkerState(path)
	if loaded.Status != "busy" {
		t.Errorf("persisted status = %q, want busy", loaded.Status)
	}
}

func TestHandleSetPID(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "worker.json")

	h, _ := CreateIdleWorker(path)
	h.Dispatch("AMBA-42", "/tmp/w.log", "amba-42")

	if err := h.SetPID(12345); err != nil {
		t.Fatalf("SetPID: %v", err)
	}

	if h.State().PID == nil || *h.State().PID != 12345 {
		t.Errorf("pid = %v, want 12345", h.State().PID)
	}

	loaded, _ := loadWorkerState(path)
	if loaded.PID == nil || *loaded.PID != 12345 {
		t.Errorf("persisted pid = %v, want 12345", loaded.PID)
	}
}

func TestHandleReset(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "worker.json")

	h, _ := CreateIdleWorker(path)
	h.Dispatch("AMBA-42", "/tmp/w.log", "amba-42")
	h.Done(0)

	if err := h.Reset(); err != nil {
		t.Fatalf("Reset: %v", err)
	}

	if h.State().Status != "idle" {
		t.Errorf("status = %q, want idle", h.State().Status)
	}
	if h.State().Ticket != nil {
		t.Error("ticket should be nil after reset")
	}

	loaded, _ := loadWorkerState(path)
	if loaded.Status != "idle" {
		t.Errorf("persisted status = %q, want idle", loaded.Status)
	}
}
