package main

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"
	"testing"
	"time"
)

func TestMarkDispatched(t *testing.T) {
	ws := newIdleWorkerState()
	ws.MarkDispatched("AMBA-42", "/tmp/worker-1.log", "amba-42")

	if ws.Status != WorkerStatusBusy {
		t.Errorf("status = %q, want busy", ws.Status)
	}
	if ws.Ticket == nil || *ws.Ticket != "AMBA-42" {
		t.Errorf("ticket = %v, want AMBA-42", ws.Ticket)
	}
	if ws.LogFile == nil || *ws.LogFile != "/tmp/worker-1.log" {
		t.Errorf("logFile = %v, want /tmp/worker-1.log", ws.LogFile)
	}
	if ws.Branch != "amba-42" {
		t.Errorf("Branch = %q, want amba-42 (placeholder)", ws.Branch)
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
		Status: WorkerStatusDone,
		Ticket: &ticket,
		Branch: branch,
	}

	ws.MarkResumed("/tmp/worker-1.log")

	if ws.Status != WorkerStatusBusy {
		t.Errorf("status = %q, want busy", ws.Status)
	}
	if ws.Ticket == nil || *ws.Ticket != "AMBA-42" {
		t.Error("ticket should be preserved")
	}
	if ws.Branch != "adam/amba-42-fix-login" {
		t.Errorf("Branch = %q, want adam/amba-42-fix-login (resolved branch preserved across resume)", ws.Branch)
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

	if ws.Status != WorkerStatusDone {
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

	if ws.Status != WorkerStatusFailed {
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

	if ws.Status != WorkerStatusIdle {
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
	if ws.Branch != "" {
		t.Errorf("Branch = %q, want empty after reset", ws.Branch)
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
	if loaded.Status != WorkerStatusBusy {
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
	if loaded.Status != WorkerStatusIdle {
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
		Status:    WorkerStatusBusy,
		Ticket:    &ticket,
		PID:       &pid,
		StartedAt: &startedAt,
		LogFile:   &logFile,
		Branch:    branch,
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "worker-1.json")
	saveWorkerState(path, ws)

	loaded, err := loadWorkerState(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if loaded.Status != WorkerStatusBusy {
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

// TestBranchNameWireCompat fences the on-disk shape jj-wsx reads: the JSON
// always carries a single "branch_name" key. WorkerState.Branch holds the
// best-known value (placeholder until WorkerHandle.Branch() resolves it),
// and the wire round-trips that single string through unchanged.
func TestBranchNameWireCompat(t *testing.T) {
	t.Run("placeholder marshals as branch_name", func(t *testing.T) {
		ws := &WorkerState{Status: WorkerStatusBusy, Branch: "amba-42"}
		data, err := json.Marshal(ws)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		var m map[string]any
		if err := json.Unmarshal(data, &m); err != nil {
			t.Fatalf("unmarshal to map: %v", err)
		}
		if m["branch_name"] != "amba-42" {
			t.Errorf("branch_name = %v, want amba-42", m["branch_name"])
		}
	})

	t.Run("resolved branch marshals as branch_name", func(t *testing.T) {
		ws := &WorkerState{Status: WorkerStatusDone, Branch: "adam/amba-42-fix"}
		data, _ := json.Marshal(ws)
		var m map[string]any
		json.Unmarshal(data, &m)
		if m["branch_name"] != "adam/amba-42-fix" {
			t.Errorf("branch_name = %v, want adam/amba-42-fix", m["branch_name"])
		}
	})

	t.Run("empty branch marshals null", func(t *testing.T) {
		ws := &WorkerState{Status: WorkerStatusIdle}
		data, _ := json.Marshal(ws)
		var m map[string]any
		json.Unmarshal(data, &m)
		val, exists := m["branch_name"]
		if !exists {
			t.Error("branch_name key missing")
		} else if val != nil {
			t.Errorf("branch_name = %v, want null", val)
		}
	})

	t.Run("unmarshal loads branch_name into Branch", func(t *testing.T) {
		raw := []byte(`{"status":"done","ticket":null,"pid":null,"started_at":null,"completed_at":null,"log_file":null,"branch_name":"adam/amba-42-fix","exit_code":null,"error":null}`)
		var ws WorkerState
		if err := json.Unmarshal(raw, &ws); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if ws.Branch != "adam/amba-42-fix" {
			t.Errorf("Branch = %q, want adam/amba-42-fix", ws.Branch)
		}
	})

	t.Run("unmarshal loads placeholder branch_name into Branch", func(t *testing.T) {
		raw := []byte(`{"status":"busy","ticket":null,"pid":null,"started_at":null,"completed_at":null,"log_file":null,"branch_name":"amba-42","exit_code":null,"error":null}`)
		var ws WorkerState
		json.Unmarshal(raw, &ws)
		if ws.Branch != "amba-42" {
			t.Errorf("Branch = %q, want amba-42", ws.Branch)
		}
	})
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

func strPtr(s string) *string { return &s }

// ── WorkerHandle tests ────────────────────────────────────────────
//
// The state-mutation primitives are exercised by the WorkerState tests
// above (TestMarkDispatched, TestMarkDone, TestMarkFailed, TestMarkResumed,
// TestSetPID, TestReset). These handle-level tests focus on the four
// public lifecycle verbs plus the constructors that wire them up.

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
	if h.Status().Status != WorkerStatusBusy {
		t.Errorf("status = %q, want busy", h.Status().Status)
	}
	if h.Status().Ticket == nil || *h.Status().Ticket != "AMBA-42" {
		t.Errorf("ticket = %v, want AMBA-42", h.Status().Ticket)
	}
}

func TestOpenWorkerMissingFile(t *testing.T) {
	_, err := OpenWorker("/nonexistent/worker.json")
	if err == nil {
		t.Fatal("expected error for missing file")
	}
}

func TestLoadLiveWorkerReconcilesDeadBusyWorker(t *testing.T) {
	dir := t.TempDir()
	poolDir := filepath.Join(dir, ".jj", "pool")
	os.MkdirAll(poolDir, 0755)
	r := &RepoContext{Root: dir, BaseDir: dir + "-workspaces"}

	logFile := filepath.Join(poolDir, "worker-1.log")
	os.WriteFile(logFile, []byte(`{"type":"result","subtype":"success","is_error":false,"result":"done"}`+"\n"), 0644)

	ws := newIdleWorkerState()
	ws.MarkDispatched("AMBA-42", logFile, "amba-42")
	ws.SetPID(99999999)
	path := r.workerStateFile("worker-1")
	saveWorkerState(path, ws)

	h, err := LoadLiveWorker(r, "worker-1")
	if err != nil {
		t.Fatalf("LoadLiveWorker: %v", err)
	}
	if h.Status().Status != WorkerStatusDone {
		t.Errorf("status = %q, want done (reconciled from dead PID)", h.Status().Status)
	}

	loaded, _ := loadWorkerState(path)
	if loaded.Status != WorkerStatusDone {
		t.Errorf("persisted status = %q, want done", loaded.Status)
	}
}

func TestLoadLiveWorkerLeavesIdleAlone(t *testing.T) {
	dir := t.TempDir()
	poolDir := filepath.Join(dir, ".jj", "pool")
	os.MkdirAll(poolDir, 0755)
	r := &RepoContext{Root: dir, BaseDir: dir + "-workspaces"}

	ws := newIdleWorkerState()
	saveWorkerState(r.workerStateFile("worker-1"), ws)

	h, err := LoadLiveWorker(r, "worker-1")
	if err != nil {
		t.Fatalf("LoadLiveWorker: %v", err)
	}
	if h.Status().Status != WorkerStatusIdle {
		t.Errorf("status = %q, want idle", h.Status().Status)
	}
}

func TestLoadLiveWorkerMissingFile(t *testing.T) {
	dir := t.TempDir()
	r := &RepoContext{Root: dir, BaseDir: dir + "-workspaces"}
	if _, err := LoadLiveWorker(r, "worker-missing"); err == nil {
		t.Fatal("expected error for missing worker")
	}
}

func TestCreateIdleWorker(t *testing.T) {
	dir := t.TempDir()
	poolDir := filepath.Join(dir, ".jj", "pool")
	os.MkdirAll(poolDir, 0755)
	r := &RepoContext{Root: dir, BaseDir: dir + "-workspaces"}

	h, err := CreateIdleWorker(r, "worker-1")
	if err != nil {
		t.Fatalf("CreateIdleWorker: %v", err)
	}
	if h.Status().Status != WorkerStatusIdle {
		t.Errorf("status = %q, want idle", h.Status().Status)
	}

	// Verify persisted to disk
	loaded, err := loadWorkerState(r.workerStateFile("worker-1"))
	if err != nil {
		t.Fatalf("load after create: %v", err)
	}
	if loaded.Status != WorkerStatusIdle {
		t.Errorf("persisted status = %q, want idle", loaded.Status)
	}
}

func TestReclaimKillsLivePIDAndResets(t *testing.T) {
	dir := t.TempDir()
	poolDir := filepath.Join(dir, ".jj", "pool")
	if err := os.MkdirAll(poolDir, 0755); err != nil {
		t.Fatalf("mkdir pool: %v", err)
	}
	r := &RepoContext{Root: dir, BaseDir: dir + "-workspaces"}

	cmd := exec.Command("sleep", "30")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start sleep: %v", err)
	}
	pid := cmd.Process.Pid
	go cmd.Wait()
	defer func() {
		_ = syscall.Kill(-pid, syscall.SIGKILL)
	}()

	path := r.workerStateFile("worker-1")
	ws := newIdleWorkerState()
	ws.MarkDispatched("AMBA-42", "/tmp/w.log", "amba-42")
	ws.SetPID(pid)
	if err := saveWorkerState(path, ws); err != nil {
		t.Fatalf("save: %v", err)
	}

	h, err := loadWorker(r, "worker-1")
	if err != nil {
		t.Fatalf("loadWorker: %v", err)
	}

	if !processAlive(pid) {
		t.Fatal("sleep should be alive before Reclaim")
	}

	if err := h.Reclaim(); err != nil {
		t.Fatalf("Reclaim: %v", err)
	}

	deadline := time.Now().Add(3 * time.Second)
	for processAlive(pid) && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	if processAlive(pid) {
		t.Errorf("process %d still alive after Reclaim", pid)
	}

	if h.Status().Status != WorkerStatusIdle {
		t.Errorf("status = %q, want idle", h.Status().Status)
	}
	if h.Status().PID != nil {
		t.Errorf("PID = %v, want nil after reset", h.Status().PID)
	}
	if h.Status().Ticket != nil {
		t.Errorf("ticket = %v, want nil after reset", h.Status().Ticket)
	}

	loaded, err := loadWorkerState(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if loaded.Status != WorkerStatusIdle {
		t.Errorf("persisted status = %q, want idle", loaded.Status)
	}
}

func TestReclaimNoPIDResetsCleanly(t *testing.T) {
	dir := t.TempDir()
	poolDir := filepath.Join(dir, ".jj", "pool")
	if err := os.MkdirAll(poolDir, 0755); err != nil {
		t.Fatalf("mkdir pool: %v", err)
	}
	r := &RepoContext{Root: dir, BaseDir: dir + "-workspaces"}

	path := r.workerStateFile("worker-1")
	ws := newIdleWorkerState()
	ws.MarkDispatched("AMBA-42", "/tmp/w.log", "amba-42")
	ws.MarkFailed(1, "crashed")
	if err := saveWorkerState(path, ws); err != nil {
		t.Fatalf("save: %v", err)
	}

	h, err := loadWorker(r, "worker-1")
	if err != nil {
		t.Fatalf("loadWorker: %v", err)
	}

	if err := h.Reclaim(); err != nil {
		t.Fatalf("Reclaim: %v", err)
	}

	if h.Status().Status != WorkerStatusIdle {
		t.Errorf("status = %q, want idle", h.Status().Status)
	}
	if h.Status().Error != nil {
		t.Errorf("error = %v, want nil after reset", h.Status().Error)
	}
}

// ── Pool tests ─────────────────────────────────────────────────────

// setupPoolWithStates builds a pool config + per-worker state files in a
// fresh tempdir and returns a *RepoContext pointing at it. workers is keyed
// by name with that worker's initial state.
func setupPoolWithStates(t *testing.T, workers map[string]*WorkerState) *RepoContext {
	t.Helper()
	dir := t.TempDir()
	poolDir := filepath.Join(dir, ".jj", "pool")
	if err := os.MkdirAll(poolDir, 0755); err != nil {
		t.Fatalf("mkdir pool: %v", err)
	}
	r := &RepoContext{Root: dir, BaseDir: dir + "-workspaces"}

	names := make([]string, 0, len(workers))
	for name := range workers {
		names = append(names, name)
	}
	// Sort for deterministic Workers ordering (map iteration is random).
	for i := 0; i < len(names); i++ {
		for j := i + 1; j < len(names); j++ {
			if names[i] > names[j] {
				names[i], names[j] = names[j], names[i]
			}
		}
	}

	cfg := &PoolConfig{
		Size:    len(names),
		Workers: names,
	}
	if err := savePoolConfig(r.poolConfigFile(), cfg); err != nil {
		t.Fatalf("save pool config: %v", err)
	}
	for name, ws := range workers {
		if err := saveWorkerState(r.workerStateFile(name), ws); err != nil {
			t.Fatalf("save worker %s: %v", name, err)
		}
	}
	return r
}

func TestOpenPoolMissingErrors(t *testing.T) {
	dir := t.TempDir()
	r := &RepoContext{Root: dir, BaseDir: dir + "-workspaces"}
	if _, err := OpenPool(r); err == nil {
		t.Fatal("expected error for missing pool")
	}
}

func TestPoolSnapshotCountsByStatus(t *testing.T) {
	ticket := "AMBA-42"
	r := setupPoolWithStates(t, map[string]*WorkerState{
		"worker-1": newIdleWorkerState(),
		"worker-2": {Status: WorkerStatusBusy, Ticket: &ticket},
		"worker-3": {Status: WorkerStatusDone},
		"worker-4": {Status: WorkerStatusFailed},
	})

	p, err := OpenPool(r)
	if err != nil {
		t.Fatalf("OpenPool: %v", err)
	}
	snap := p.Snapshot()
	if snap.Size != 4 {
		t.Errorf("Size = %d, want 4", snap.Size)
	}
	if snap.Idle != 1 || snap.Busy != 1 || snap.Done != 1 || snap.Failed != 1 {
		t.Errorf("counts = (idle=%d busy=%d done=%d failed=%d), want all 1", snap.Idle, snap.Busy, snap.Done, snap.Failed)
	}
}

func TestPoolClaimMarksWorkerBusyWithTicket(t *testing.T) {
	r := setupPoolWithStates(t, map[string]*WorkerState{
		"worker-1": newIdleWorkerState(),
	})
	p, _ := OpenPool(r)

	got, err := p.Claim("AMBA-99")
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}
	if got != "worker-1" {
		t.Errorf("claimed %q, want worker-1", got)
	}

	ws, _ := loadWorkerState(r.workerStateFile("worker-1"))
	if ws.Status != WorkerStatusBusy {
		t.Errorf("status = %q, want busy", ws.Status)
	}
	if ws.Ticket == nil || *ws.Ticket != "AMBA-99" {
		t.Errorf("ticket = %v, want AMBA-99", ws.Ticket)
	}
}

func TestPoolClaimNoIdleErrors(t *testing.T) {
	ticket := "AMBA-1"
	r := setupPoolWithStates(t, map[string]*WorkerState{
		"worker-1": {Status: WorkerStatusBusy, Ticket: &ticket},
	})
	p, _ := OpenPool(r)
	if _, err := p.Claim("AMBA-2"); err == nil {
		t.Fatal("expected no-idle error")
	}
}

func TestPoolReserveAlignsWorkersToTickets(t *testing.T) {
	r := setupPoolWithStates(t, map[string]*WorkerState{
		"worker-1": newIdleWorkerState(),
		"worker-2": newIdleWorkerState(),
		"worker-3": newIdleWorkerState(),
	})
	p, _ := OpenPool(r)

	tickets := []string{"AMBA-1", "AMBA-2"}
	workers, err := p.Reserve(tickets)
	if err != nil {
		t.Fatalf("Reserve: %v", err)
	}
	if len(workers) != len(tickets) {
		t.Fatalf("len(workers) = %d, want %d", len(workers), len(tickets))
	}
	for i, w := range workers {
		ws, lerr := loadWorkerState(r.workerStateFile(w))
		if lerr != nil {
			t.Fatalf("load %s: %v", w, lerr)
		}
		if ws.Status != WorkerStatusBusy {
			t.Errorf("workers[%d]=%q status = %q, want busy", i, w, ws.Status)
		}
		if ws.Ticket == nil || *ws.Ticket != tickets[i] {
			t.Errorf("workers[%d]=%q ticket = %v, want %q", i, w, ws.Ticket, tickets[i])
		}
	}

	// The third worker remains idle - Reserve is all-or-nothing for the
	// asked-for count, not greedy.
	ws3, _ := loadWorkerState(r.workerStateFile("worker-3"))
	if ws3.Status != WorkerStatusIdle {
		t.Errorf("worker-3 status = %q, want idle", ws3.Status)
	}
}

func TestPoolReserveReturnsPoolFullWithoutWriting(t *testing.T) {
	busy := "AMBA-busy"
	r := setupPoolWithStates(t, map[string]*WorkerState{
		"worker-1": newIdleWorkerState(),
		"worker-2": {Status: WorkerStatusBusy, Ticket: &busy},
	})
	p, _ := OpenPool(r)

	tickets := []string{"AMBA-X", "AMBA-Y", "AMBA-Z"}
	workers, err := p.Reserve(tickets)
	if err == nil {
		t.Fatalf("Reserve unexpectedly succeeded: workers=%v", workers)
	}
	var pf *PoolFull
	if !errors.As(err, &pf) {
		t.Fatalf("Reserve err = %v (%T), want *PoolFull", err, err)
	}
	if pf.Need != 3 || pf.Have != 1 || pf.Gap() != 2 {
		t.Errorf("PoolFull = %+v, want Need=3 Have=1 Gap=2", pf)
	}

	// Crucially, the idle worker we could have claimed was NOT written.
	ws1, _ := loadWorkerState(r.workerStateFile("worker-1"))
	if ws1.Status != WorkerStatusIdle {
		t.Errorf("worker-1 status = %q, want idle (Reserve must not mutate on PoolFull)", ws1.Status)
	}
}

func TestPoolGrowAndReserveSkipsGrowWhenEnoughIdle(t *testing.T) {
	r := setupPoolWithStates(t, map[string]*WorkerState{
		"worker-1": newIdleWorkerState(),
		"worker-2": newIdleWorkerState(),
	})
	p, _ := OpenPool(r)
	initialSize := p.Config().Size

	workers, err := p.GrowAndReserve([]string{"AMBA-A", "AMBA-B"})
	if err != nil {
		t.Fatalf("GrowAndReserve: %v", err)
	}
	if len(workers) != 2 {
		t.Fatalf("len(workers) = %d, want 2", len(workers))
	}

	// No grow was needed - size must be unchanged.
	cfg, _ := loadPoolConfig(r.poolConfigFile())
	if cfg.Size != initialSize {
		t.Errorf("Size = %d after GrowAndReserve, want %d (no grow expected)", cfg.Size, initialSize)
	}
	if len(cfg.Workers) != initialSize {
		t.Errorf("len(Workers) = %d, want %d", len(cfg.Workers), initialSize)
	}
}

func TestPoolRemoveDropsWorkerAndShrinksSize(t *testing.T) {
	r := setupPoolWithStates(t, map[string]*WorkerState{
		"worker-1": newIdleWorkerState(),
		"worker-2": newIdleWorkerState(),
	})
	p, _ := OpenPool(r)

	size, err := p.Remove("worker-2")
	if err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if size != 1 {
		t.Errorf("size = %d, want 1", size)
	}

	cfg, _ := loadPoolConfig(r.poolConfigFile())
	if len(cfg.Workers) != 1 || cfg.Workers[0] != "worker-1" {
		t.Errorf("cfg.Workers = %v, want [worker-1]", cfg.Workers)
	}
	if _, err := os.Stat(r.workerStateFile("worker-2")); !os.IsNotExist(err) {
		t.Errorf("worker-2 state file should be gone, stat err = %v", err)
	}
}

func TestPoolRemoveBusyErrors(t *testing.T) {
	ticket := "AMBA-1"
	r := setupPoolWithStates(t, map[string]*WorkerState{
		"worker-1": {Status: WorkerStatusBusy, Ticket: &ticket},
	})
	p, _ := OpenPool(r)
	if _, err := p.Remove("worker-1"); err == nil {
		t.Fatal("expected busy error")
	}
}

// TestPoolClaimSerialisesWithShrink is the regression test for the resize-
// vs-claim race the Pool aggregate was introduced to fix. The setup pits
// the two paths against the SAME idle worker: with the head two workers
// already busy, Claim wants worker-3 (first idle from head) and Resize(2)
// wants worker-3 (last idle when scanning from tail toward newSize). If
// Resize takes no lock (the pre-Pool bug), the window between its idle-
// snapshot and its cfg write lets Claim mark worker-3 busy; Resize then
// tears the workspace down and writes cfg without it, leaving an orphan
// busy state file pointing at a deleted workspace.
//
// Under the lock the two paths serialise: whichever wins commits cleanly,
// the other observes the new state (Resize fails "cannot shrink", or
// Claim fails "no idle workers"). The invariant: whenever Claim returns
// a worker, that worker is still in cfg.Workers and still busy with the
// test ticket.
func TestPoolClaimSerialisesWithShrink(t *testing.T) {
	busyTicket := "AMBA-busy"
	const rounds = 50
	for round := 0; round < rounds; round++ {
		r := setupPoolWithStates(t, map[string]*WorkerState{
			"worker-1": {Status: WorkerStatusBusy, Ticket: &busyTicket},
			"worker-2": {Status: WorkerStatusBusy, Ticket: &busyTicket},
			"worker-3": newIdleWorkerState(),
			"worker-4": newIdleWorkerState(),
		})

		// Pre-create workspace dirs so tearDownWorker's stat finds them.
		for _, w := range []string{"worker-3", "worker-4"} {
			if err := os.MkdirAll(r.workerDir(w), 0755); err != nil {
				t.Fatalf("mkdir worker dir: %v", err)
			}
		}

		// Independent Pool handles to mimic two processes contending.
		p1, _ := OpenPool(r)
		p2, _ := OpenPool(r)

		var claimedWorker string
		var claimErr, resizeErr error
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			claimedWorker, claimErr = p1.Claim("AMBA-RACE")
		}()
		go func() {
			defer wg.Done()
			resizeErr = p2.Resize(2)
		}()
		wg.Wait()

		cfg, _ := loadPoolConfig(r.poolConfigFile())

		if claimErr == nil {
			// Claim won (at least for one of the idle workers). The
			// worker must still be a member of the pool.
			stillPresent := false
			for _, w := range cfg.Workers {
				if w == claimedWorker {
					stillPresent = true
					break
				}
			}
			if !stillPresent {
				t.Fatalf("round %d: claimed %q removed by concurrent shrink (cfg.Workers=%v, resizeErr=%v)", round, claimedWorker, cfg.Workers, resizeErr)
			}

			ws, err := loadWorkerState(r.workerStateFile(claimedWorker))
			if err != nil {
				t.Fatalf("round %d: claimed worker state file gone: %v", round, err)
			}
			if ws.Status != WorkerStatusBusy || ws.Ticket == nil || *ws.Ticket != "AMBA-RACE" {
				t.Errorf("round %d: claimed %q state = %+v, want busy AMBA-RACE", round, claimedWorker, ws)
			}
		}
		// If Claim errored, Resize won the lock first - that's a valid
		// outcome; we don't assert anything about its specific shape.
	}
}

// TestWaitFinalSerialisesWithCheckLiveness is the regression test for the
// supervisor-vs-checkLiveness race that WaitFinal fences with the worker
// flock. Setup: a busy worker with a dead PID and a log file holding a
// success result event. Many goroutines simultaneously LoadLiveWorker
// (which calls checkLiveness -> finalizeUnderLock) alongside a direct
// WaitFinal. Before the lock, the two writers could clobber each other's
// temp file (path+".tmp") or land inconsistent terminal state. Under the
// lock the first writer transitions busy -> done and saves; every later
// writer re-reads under lock, sees done, and exits cleanly. The invariant:
// the persisted state is done, with a single exit_code field still set.
func TestWaitFinalSerialisesWithCheckLiveness(t *testing.T) {
	dir := t.TempDir()
	poolDir := filepath.Join(dir, ".jj", "pool")
	if err := os.MkdirAll(poolDir, 0755); err != nil {
		t.Fatalf("mkdir pool: %v", err)
	}
	r := &RepoContext{Root: dir, BaseDir: dir + "-workspaces"}

	logFile := filepath.Join(poolDir, "worker-1.log")
	if err := os.WriteFile(logFile,
		[]byte(`{"type":"result","subtype":"success","is_error":false}`+"\n"),
		0644); err != nil {
		t.Fatalf("write log: %v", err)
	}

	ws := newIdleWorkerState()
	ws.MarkDispatched("AMBA-42", logFile, "amba-42")
	ws.SetPID(99999999) // dead PID: kernel reports !alive immediately
	if err := saveWorkerState(r.workerStateFile("worker-1"), ws); err != nil {
		t.Fatalf("save worker: %v", err)
	}

	const N = 32
	var wg sync.WaitGroup
	wg.Add(N + 1)
	go func() {
		defer wg.Done()
		h, err := loadWorker(r, "worker-1")
		if err != nil {
			t.Errorf("loadWorker: %v", err)
			return
		}
		h.WaitFinal()
	}()
	for i := 0; i < N; i++ {
		go func() {
			defer wg.Done()
			if _, err := LoadLiveWorker(r, "worker-1"); err != nil {
				t.Errorf("LoadLiveWorker: %v", err)
			}
		}()
	}
	wg.Wait()

	loaded, err := loadWorkerState(r.workerStateFile("worker-1"))
	if err != nil {
		t.Fatalf("load after race: %v", err)
	}
	if loaded.Status != WorkerStatusDone {
		t.Errorf("status = %q, want done", loaded.Status)
	}
	if loaded.ExitCode == nil || *loaded.ExitCode != 0 {
		t.Errorf("exit_code = %v, want 0", loaded.ExitCode)
	}
	if loaded.CompletedAt == nil || *loaded.CompletedAt == "" {
		t.Error("completed_at should be set")
	}
}

// TestWaitFinalIdempotent confirms WaitFinal can be called repeatedly on a
// terminal worker without re-writing it - the re-read under lock observes
// the terminal status and exits, so the persisted completed_at timestamp
// remains stable across calls.
func TestWaitFinalIdempotent(t *testing.T) {
	dir := t.TempDir()
	poolDir := filepath.Join(dir, ".jj", "pool")
	if err := os.MkdirAll(poolDir, 0755); err != nil {
		t.Fatalf("mkdir pool: %v", err)
	}
	r := &RepoContext{Root: dir, BaseDir: dir + "-workspaces"}

	logFile := filepath.Join(poolDir, "worker-1.log")
	os.WriteFile(logFile,
		[]byte(`{"type":"result","subtype":"success","is_error":false}`+"\n"),
		0644)

	ws := newIdleWorkerState()
	ws.MarkDispatched("AMBA-42", logFile, "amba-42")
	ws.SetPID(99999999)
	saveWorkerState(r.workerStateFile("worker-1"), ws)

	h1, _ := loadWorker(r, "worker-1")
	h1.WaitFinal()
	firstCompleted := h1.Status().CompletedAt
	if firstCompleted == nil {
		t.Fatal("first WaitFinal should populate completed_at")
	}

	h2, _ := loadWorker(r, "worker-1")
	h2.WaitFinal()
	if h2.Status().Status != WorkerStatusDone {
		t.Errorf("second WaitFinal status = %q, want done", h2.Status().Status)
	}
	if h2.Status().CompletedAt == nil || *h2.Status().CompletedAt != *firstCompleted {
		t.Errorf("completed_at changed across calls: %v -> %v", firstCompleted, h2.Status().CompletedAt)
	}
}
