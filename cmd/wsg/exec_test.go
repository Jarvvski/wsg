package main

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// setupBusyHandle returns a wired-up handle whose state is already busy on
// the given log file. Use it when the test wants to exercise the launch /
// supervisor plumbing without going through Pool.Claim's atomic mark.
func setupBusyHandle(t *testing.T, dir, worker, logFile string) (*WorkerHandle, *RepoContext) {
	t.Helper()
	poolDir := filepath.Join(dir, ".jj", "pool")
	os.MkdirAll(poolDir, 0755)
	r := &RepoContext{Root: dir, BaseDir: dir + "-workspaces"}
	h, err := CreateIdleWorker(r, worker)
	if err != nil {
		t.Fatalf("CreateIdleWorker: %v", err)
	}
	h.state.MarkDispatched("AMBA-42", logFile, "amba-42")
	if err := h.save(); err != nil {
		t.Fatalf("save: %v", err)
	}
	return h, r
}

// Foreground runs now finalise from the log (the single WaitFinal path
// shared with background runs and checkLiveness), so the test command
// emits a stream-json result event the way claude does in production.

func TestRunClaudeFGSuccess(t *testing.T) {
	dir := t.TempDir()
	logFile := filepath.Join(dir, ".jj", "pool", "worker-1.log")
	h, _ := setupBusyHandle(t, dir, "worker-1", logFile)

	cmd := []string{"sh", "-c", `echo '{"type":"result","subtype":"success","is_error":false}'`}
	h.runFG(dir, logFile, cmd)

	if h.Status().Status != WorkerStatusDone {
		t.Errorf("status = %q, want done", h.Status().Status)
	}
	if h.Status().ExitCode == nil || *h.Status().ExitCode != 0 {
		t.Errorf("exitCode = %v, want 0", h.Status().ExitCode)
	}
	if h.Status().CompletedAt == nil {
		t.Error("completedAt should be set")
	}
}

func TestRunClaudeFGFailure(t *testing.T) {
	dir := t.TempDir()
	logFile := filepath.Join(dir, ".jj", "pool", "worker-1.log")
	h, _ := setupBusyHandle(t, dir, "worker-1", logFile)

	cmd := []string{"sh", "-c", `echo '{"type":"result","subtype":"error_during_execution","is_error":true}'`}
	h.runFG(dir, logFile, cmd)

	if h.Status().Status != WorkerStatusFailed {
		t.Errorf("status = %q, want failed", h.Status().Status)
	}
	if h.Status().ExitCode == nil || *h.Status().ExitCode != 1 {
		t.Errorf("exitCode = %v, want 1", h.Status().ExitCode)
	}
	if h.Status().Error == nil || *h.Status().Error != "error_during_execution" {
		t.Errorf("error = %v, want error_during_execution", h.Status().Error)
	}
}

func TestRunClaudeBGSuccess(t *testing.T) {
	dir := t.TempDir()
	logFile := filepath.Join(dir, ".jj", "pool", "worker-1.log")
	h, r := setupBusyHandle(t, dir, "worker-1", logFile)

	cmd := []string{"sh", "-c", `echo '{"type":"result","subtype":"success","is_error":false}'`}
	pid, err := h.runBG(dir, logFile, cmd)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if pid <= 0 {
		t.Errorf("pid = %d, want > 0", pid)
	}

	loaded := awaitTerminal(t, r.workerStateFile("worker-1"))
	if loaded.Status != WorkerStatusDone {
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
	logFile := filepath.Join(dir, ".jj", "pool", "worker-1.log")
	h, r := setupBusyHandle(t, dir, "worker-1", logFile)

	cmd := []string{"sh", "-c", `echo '{"type":"result","subtype":"error_during_execution","is_error":true}'`}
	if _, err := h.runBG(dir, logFile, cmd); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	loaded := awaitTerminal(t, r.workerStateFile("worker-1"))
	if loaded.Status != WorkerStatusFailed {
		t.Errorf("status = %q, want failed", loaded.Status)
	}
	if loaded.Error == nil || *loaded.Error != "error_during_execution" {
		t.Errorf("error = %v, want error_during_execution", loaded.Error)
	}
}

func TestWorkerLaunchEnablesSupportedCodexDelegation(t *testing.T) {
	dir := t.TempDir()
	logFile := filepath.Join(dir, ".jj", "pool", "worker-1.log")
	h, r := setupBusyHandle(t, dir, "worker-1", logFile)
	if err := os.MkdirAll(r.workerDir("worker-1"), 0755); err != nil {
		t.Fatal(err)
	}

	binDir := t.TempDir()
	argsFile := filepath.Join(dir, "codex-args")
	exe := filepath.Join(binDir, "codex")
	script := "#!/bin/sh\nif [ \"$1\" = features ]; then\n  printf '%s\\n' 'multi_agent stable false'\n  exit 0\nfi\nprintf '%s\\n' \"$@\" > \"$ARGS_FILE\"\nprintf '%s\\n' '{\"type\":\"turn.completed\",\"usage\":{}}'\n"
	if err := os.WriteFile(exe, []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("ARGS_FILE", argsFile)

	if _, err := h.launch(agentInvocation{Agent: AgentCodex, Prompt: "work"}, true); err != nil {
		t.Fatal(err)
	}
	args, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(args), "--enable\nmulti_agent") {
		t.Errorf("worker launch args missing multi_agent enablement: %s", args)
	}
}

func TestWorkerLaunchContinuesWhenCapabilityProbeFails(t *testing.T) {
	dir := t.TempDir()
	logFile := filepath.Join(dir, ".jj", "pool", "worker-1.log")
	h, r := setupBusyHandle(t, dir, "worker-1", logFile)
	if err := os.MkdirAll(r.workerDir("worker-1"), 0755); err != nil {
		t.Fatal(err)
	}

	binDir := t.TempDir()
	argsFile := filepath.Join(dir, "codex-args")
	exe := filepath.Join(binDir, "codex")
	script := "#!/bin/sh\nif [ \"$1\" = features ]; then\n  exit 1\nfi\nprintf '%s\\n' \"$@\" > \"$ARGS_FILE\"\nprintf '%s\\n' '{\"type\":\"turn.completed\",\"usage\":{}}'\n"
	if err := os.WriteFile(exe, []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("ARGS_FILE", argsFile)

	if _, err := h.launch(agentInvocation{Agent: AgentCodex, Prompt: "work"}, true); err != nil {
		t.Fatal(err)
	}
	args, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(args), "multi_agent") {
		t.Errorf("worker launch should omit unsupported optional flags: %s", args)
	}
}

func TestWorkerWithChildProcessRemainsBusyUntilRootResult(t *testing.T) {
	dir := t.TempDir()
	logFile := filepath.Join(dir, ".jj", "pool", "worker-1.log")
	h, r := setupBusyHandle(t, dir, "worker-1", logFile)
	childPIDFile := filepath.Join(dir, "child.pid")
	releaseFile := filepath.Join(dir, "release")
	script := "sleep 30 & child=$!; printf '%s' \"$child\" > \"$CHILD_PID_FILE\"; while [ ! -f \"$RELEASE_FILE\" ]; do sleep 0.05; done; kill \"$child\"; wait \"$child\" 2>/dev/null; printf '%s\\n' '{\"type\":\"result\",\"subtype\":\"success\",\"is_error\":false}'"
	t.Setenv("CHILD_PID_FILE", childPIDFile)
	t.Setenv("RELEASE_FILE", releaseFile)

	rootPID, err := h.runBG(dir, logFile, []string{"sh", "-c", script})
	if err != nil {
		t.Fatal(err)
	}
	defer killProcess(rootPID)
	childPID := awaitPIDFile(t, childPIDFile)
	if !processAlive(rootPID) || !processAlive(childPID) {
		t.Fatalf("root %d and child %d should be alive", rootPID, childPID)
	}
	busy, err := loadWorkerState(r.workerStateFile("worker-1"))
	if err != nil {
		t.Fatal(err)
	}
	if busy.Status != WorkerStatusBusy {
		t.Fatalf("status before root result = %q, want busy", busy.Status)
	}

	if err := os.WriteFile(releaseFile, nil, 0644); err != nil {
		t.Fatal(err)
	}
	terminal := awaitTerminal(t, r.workerStateFile("worker-1"))
	if terminal.Status != WorkerStatusDone {
		t.Errorf("status after root result = %q, want done", terminal.Status)
	}
}

func TestResetTerminatesRuntimeProcessGroup(t *testing.T) {
	dir := t.TempDir()
	logFile := filepath.Join(dir, ".jj", "pool", "worker-1.log")
	h, r := setupBusyHandle(t, dir, "worker-1", logFile)
	childPIDFile := filepath.Join(dir, "child.pid")
	script := "sleep 30 & child=$!; printf '%s' \"$child\" > \"$CHILD_PID_FILE\"; wait"
	t.Setenv("CHILD_PID_FILE", childPIDFile)

	rootPID, err := h.runBG(dir, logFile, []string{"sh", "-c", script})
	if err != nil {
		t.Fatal(err)
	}
	defer killProcess(rootPID)
	childPID := awaitPIDFile(t, childPIDFile)

	if err := NewActions(r).Reset("worker-1"); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for (processAlive(rootPID) || processAlive(childPID)) && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	if processAlive(rootPID) || processAlive(childPID) {
		t.Errorf("runtime process group still alive after reset: root=%v child=%v", processAlive(rootPID), processAlive(childPID))
	}
	idle, err := loadWorkerState(r.workerStateFile("worker-1"))
	if err != nil {
		t.Fatal(err)
	}
	if idle.Status != WorkerStatusIdle {
		t.Errorf("worker status = %q, want idle", idle.Status)
	}
}

func awaitPIDFile(t *testing.T, path string) int {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(path)
		if err == nil {
			pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
			if err == nil && pid > 0 {
				return pid
			}
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for PID file %s", path)
	return 0
}

// awaitTerminal polls a worker state file until it leaves the busy state.
func awaitTerminal(t *testing.T, sf string) *WorkerState {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		loaded, _ := loadWorkerState(sf)
		if loaded != nil && loaded.Status != WorkerStatusBusy {
			return loaded
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("timed out waiting for background completion")
	return nil
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
