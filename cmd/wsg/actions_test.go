package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestActionsResetIdleWorker(t *testing.T) {
	r := setupPoolWithStates(t, map[string]*WorkerState{
		"worker-1": newIdleWorkerState(),
	})

	if err := NewActions(r).Reset("worker-1"); err != nil {
		t.Fatalf("Reset: %v", err)
	}

	ws, _ := loadWorkerState(r.workerStateFile("worker-1"))
	if ws.Status != WorkerStatusIdle {
		t.Errorf("status = %q, want idle", ws.Status)
	}
}

func TestActionsResetClearsTerminalState(t *testing.T) {
	ws := newIdleWorkerState()
	ws.MarkDispatched("AMBA-42", "/tmp/w.log", "amba-42")
	ws.MarkFailed(1, "crashed")
	r := setupPoolWithStates(t, map[string]*WorkerState{"worker-1": ws})

	if err := NewActions(r).Reset("worker-1"); err != nil {
		t.Fatalf("Reset: %v", err)
	}

	loaded, _ := loadWorkerState(r.workerStateFile("worker-1"))
	if loaded.Status != WorkerStatusIdle {
		t.Errorf("status = %q, want idle", loaded.Status)
	}
	if loaded.Error != nil {
		t.Errorf("error = %v, want nil after reset", loaded.Error)
	}
	if loaded.Ticket != nil {
		t.Errorf("ticket = %v, want nil after reset", loaded.Ticket)
	}
}

func TestActionsResetMissingWorker(t *testing.T) {
	dir := t.TempDir()
	r := &RepoContext{Root: dir, BaseDir: dir + "-workspaces"}
	if err := NewActions(r).Reset("nonexistent"); err == nil {
		t.Fatal("expected error for missing worker")
	}
}

func TestActionsDismissIdleRemovesFromPool(t *testing.T) {
	r := setupPoolWithStates(t, map[string]*WorkerState{
		"worker-1": newIdleWorkerState(),
		"worker-2": newIdleWorkerState(),
	})

	size, err := NewActions(r).Dismiss("worker-2")
	if err != nil {
		t.Fatalf("Dismiss: %v", err)
	}
	if size != 1 {
		t.Errorf("size = %d, want 1", size)
	}

	cfg, _ := loadPoolConfig(r.poolConfigFile())
	if len(cfg.Workers) != 1 || cfg.Workers[0] != "worker-1" {
		t.Errorf("cfg.Workers = %v, want [worker-1]", cfg.Workers)
	}
}

func TestActionsDismissTerminalWorkerResetsInPlace(t *testing.T) {
	failedWS := &WorkerState{Status: WorkerStatusFailed}
	r := setupPoolWithStates(t, map[string]*WorkerState{
		"worker-1": newIdleWorkerState(),
		"worker-2": failedWS,
	})

	size, err := NewActions(r).Dismiss("worker-2")
	if err != nil {
		t.Fatalf("Dismiss: %v", err)
	}
	if size != -1 {
		t.Errorf("size = %d, want -1 (reset, not removed)", size)
	}

	cfg, _ := loadPoolConfig(r.poolConfigFile())
	if len(cfg.Workers) != 2 {
		t.Errorf("pool size = %d, want 2 (worker stays)", len(cfg.Workers))
	}
	ws, _ := loadWorkerState(r.workerStateFile("worker-2"))
	if ws.Status != WorkerStatusIdle {
		t.Errorf("status = %q, want idle", ws.Status)
	}
}

func TestActionsDismissBusyErrors(t *testing.T) {
	ticket := "AMBA-1"
	r := setupPoolWithStates(t, map[string]*WorkerState{
		"worker-1": {Status: WorkerStatusBusy, Ticket: &ticket},
	})
	if _, err := NewActions(r).Dismiss("worker-1"); err == nil {
		t.Fatal("expected error for busy worker")
	}
}

func TestActionsOpenPRNoBranchErrors(t *testing.T) {
	r := setupPoolWithStates(t, map[string]*WorkerState{
		"worker-1": newIdleWorkerState(),
	})
	err := NewActions(r).OpenPR("worker-1")
	if err == nil {
		t.Fatal("expected error for worker without branch")
	}
	if !strings.Contains(err.Error(), "no branch") {
		t.Errorf("error = %q, want it to mention no branch", err)
	}
}

func TestActionsRebaseNoBranchErrors(t *testing.T) {
	r := setupPoolWithStates(t, map[string]*WorkerState{
		"worker-1": newIdleWorkerState(),
	})
	err := NewActions(r).Rebase("worker-1")
	if err == nil {
		t.Fatal("expected error for worker without branch")
	}
	if !strings.Contains(err.Error(), "no branch") {
		t.Errorf("error = %q, want it to mention no branch", err)
	}
}

func TestActionsOpenPRMissingWorker(t *testing.T) {
	dir := t.TempDir()
	r := &RepoContext{Root: dir, BaseDir: dir + "-workspaces"}
	if err := NewActions(r).OpenPR("nope"); err == nil {
		t.Fatal("expected error for missing worker")
	}
}

func TestActionsResetWorkspaceMissingDoesNotErr(t *testing.T) {
	// Reset must succeed even if the workspace directory is gone (test
	// regression: the async jj restore path stat-checks the dir first).
	r := setupPoolWithStates(t, map[string]*WorkerState{
		"worker-1": newIdleWorkerState(),
	})

	wspath := r.workerDir("worker-1")
	if _, err := os.Stat(wspath); err == nil {
		t.Fatalf("workspace dir unexpectedly exists at %s", wspath)
	}

	if err := NewActions(r).Reset("worker-1"); err != nil {
		t.Fatalf("Reset with missing workspace: %v", err)
	}
}

func TestActionsDismissMissingWorker(t *testing.T) {
	dir := t.TempDir()
	poolDir := filepath.Join(dir, ".jj", "pool")
	os.MkdirAll(poolDir, 0755)
	r := &RepoContext{Root: dir, BaseDir: dir + "-workspaces"}
	if _, err := NewActions(r).Dismiss("nope"); err == nil {
		t.Fatal("expected error for missing worker")
	}
}
