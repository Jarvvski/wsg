package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

func setupTestPool(t *testing.T, workers map[string]*WorkerState) *RepoContext {
	t.Helper()
	dir := t.TempDir()
	poolDir := filepath.Join(dir, ".jj", "pool")
	os.MkdirAll(poolDir, 0755)

	r := &RepoContext{Root: dir, BaseDir: dir + "-workspaces"}

	var names []string
	for name := range workers {
		names = append(names, name)
	}

	cfg := &PoolConfig{
		Size:    len(workers),
		Workers: names,
	}
	savePoolConfig(r.poolConfigFile(), cfg)

	for name, ws := range workers {
		saveWorkerState(filepath.Join(poolDir, name+".json"), ws)
	}
	return r
}

func TestTUIViewRendersWorkerList(t *testing.T) {
	origTTY := isTTY
	isTTY = false
	defer func() { isTTY = origTTY }()

	ticket := "AMBA-42"
	startedAt := "2026-05-20T14:00:00Z"
	completedAt := "2026-05-20T14:05:30Z"
	branch := "adam/amba-42-fix-stuff"

	r := setupTestPool(t, map[string]*WorkerState{
		"worker-aaa": newIdleWorkerState(),
		"worker-bbb": {
			Status:    "busy",
			Ticket:    &ticket,
			StartedAt: &startedAt,
		},
		"worker-ccc": {
			Status:      "done",
			Ticket:      &ticket,
			StartedAt:   &startedAt,
			CompletedAt: &completedAt,
			BranchName:  &branch,
		},
	})

	m := newTUIModel(r)
	view := m.renderList()

	// Should contain all three workers (display names strip "worker-" prefix)
	if !strings.Contains(view, "aaa") {
		t.Errorf("view missing worker aaa:\n%s", view)
	}
	if !strings.Contains(view, "bbb") {
		t.Errorf("view missing worker bbb:\n%s", view)
	}
	if !strings.Contains(view, "ccc") {
		t.Errorf("view missing worker ccc:\n%s", view)
	}

	// Should show statuses
	if !strings.Contains(view, "idle") {
		t.Errorf("view missing idle status:\n%s", view)
	}
	if !strings.Contains(view, "busy") {
		t.Errorf("view missing busy status:\n%s", view)
	}
	if !strings.Contains(view, "done") {
		t.Errorf("view missing done status:\n%s", view)
	}

	// Should show ticket
	if !strings.Contains(view, "AMBA-42") {
		t.Errorf("view missing ticket AMBA-42:\n%s", view)
	}

	// Should show elapsed for completed worker
	if !strings.Contains(view, "5m 30s") {
		t.Errorf("view missing elapsed 5m 30s:\n%s", view)
	}

	// Should show key hints in status bar
	if !strings.Contains(view, "[f]") {
		t.Errorf("view missing key hints:\n%s", view)
	}
}

func TestTUIViewSelectedRowHighlighted(t *testing.T) {
	r := setupTestPool(t, map[string]*WorkerState{
		"worker-aaa": newIdleWorkerState(),
		"worker-bbb": newIdleWorkerState(),
	})

	m := newTUIModel(r)
	// cursor defaults to 0; the first worker row should be visually distinct
	view := m.renderList()
	lines := strings.Split(view, "\n")

	// Find the row with the first worker - it should have the selection indicator
	found := false
	for _, line := range lines {
		if strings.Contains(line, ">") && (strings.Contains(line, "aaa") || strings.Contains(line, "bbb")) {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("no selected row indicator found in view:\n%s", view)
	}
}

func keyPress(r rune) tea.KeyPressMsg {
	return tea.KeyPressMsg{Code: r, Text: string(r)}
}

func TestTUICursorNavigation(t *testing.T) {
	r := setupTestPool(t, map[string]*WorkerState{
		"worker-aaa": newIdleWorkerState(),
		"worker-bbb": newIdleWorkerState(),
		"worker-ccc": newIdleWorkerState(),
	})

	m := newTUIModel(r)
	if m.cursor != 0 {
		t.Fatalf("initial cursor = %d, want 0", m.cursor)
	}

	// j moves down
	updated, _ := m.Update(keyPress('j'))
	m = updated.(tuiModel)
	if m.cursor != 1 {
		t.Errorf("after j: cursor = %d, want 1", m.cursor)
	}

	// k moves up
	updated, _ = m.Update(keyPress('k'))
	m = updated.(tuiModel)
	if m.cursor != 0 {
		t.Errorf("after k: cursor = %d, want 0", m.cursor)
	}

	// k at top clamps to 0
	updated, _ = m.Update(keyPress('k'))
	m = updated.(tuiModel)
	if m.cursor != 0 {
		t.Errorf("k at top: cursor = %d, want 0", m.cursor)
	}

	// move to bottom
	updated, _ = m.Update(keyPress('j'))
	m = updated.(tuiModel)
	updated, _ = m.Update(keyPress('j'))
	m = updated.(tuiModel)

	// j at bottom clamps
	if m.cursor != 2 {
		t.Errorf("at bottom: cursor = %d, want 2", m.cursor)
	}
	updated, _ = m.Update(keyPress('j'))
	m = updated.(tuiModel)
	if m.cursor != 2 {
		t.Errorf("j at bottom: cursor = %d, want 2", m.cursor)
	}
}

func TestTUIRebaseGatingBusyWorker(t *testing.T) {
	ticket := "AMBA-42"
	startedAt := "2026-05-20T14:00:00Z"

	r := setupTestPool(t, map[string]*WorkerState{
		"worker-aaa": {Status: "busy", Ticket: &ticket, StartedAt: &startedAt},
	})

	m := newTUIModel(r)
	updated, _ := m.Update(keyPress('g'))
	m = updated.(tuiModel)

	if !strings.Contains(m.status, "busy") {
		t.Errorf("expected error about busy worker in status, got: %q", m.status)
	}
}

func TestTUIRebaseAllowedOnDoneWorker(t *testing.T) {
	ticket := "AMBA-42"
	startedAt := "2026-05-20T14:00:00Z"
	completedAt := "2026-05-20T14:05:00Z"
	branch := "adam/amba-42-fix"

	r := setupTestPool(t, map[string]*WorkerState{
		"worker-aaa": {
			Status:      "done",
			Ticket:      &ticket,
			StartedAt:   &startedAt,
			CompletedAt: &completedAt,
			BranchName:  &branch,
		},
	})

	m := newTUIModel(r)
	updated, cmd := m.Update(keyPress('g'))
	m = updated.(tuiModel)

	// Should not show an error - should show a rebase-related message or produce a command
	if strings.Contains(m.status, "busy") {
		t.Errorf("done worker should not trigger busy error, got: %q", m.status)
	}
	_ = cmd
}

func TestTUIReviewGatingNoBranch(t *testing.T) {
	r := setupTestPool(t, map[string]*WorkerState{
		"worker-aaa": newIdleWorkerState(),
	})

	m := newTUIModel(r)
	updated, _ := m.Update(keyPress('r'))
	m = updated.(tuiModel)

	if !strings.Contains(m.status, "branch") && !strings.Contains(m.status, "PR") && !strings.Contains(m.status, "dispatch") {
		t.Errorf("expected error about no branch/PR in status, got: %q", m.status)
	}
}

func TestTUISendGatingNoSession(t *testing.T) {
	r := setupTestPool(t, map[string]*WorkerState{
		"worker-aaa": newIdleWorkerState(),
	})

	m := newTUIModel(r)
	updated, _ := m.Update(keyPress('s'))
	m = updated.(tuiModel)

	if !strings.Contains(m.status, "session") && !strings.Contains(m.status, "no ") {
		t.Errorf("expected error about no session in status, got: %q", m.status)
	}
}

func TestTUIResetBlockedForBusyWorker(t *testing.T) {
	ticket := "AMBA-42"
	startedAt := "2026-05-20T14:00:00Z"
	r := setupTestPool(t, map[string]*WorkerState{
		"worker-aaa": {Status: "busy", Ticket: &ticket, StartedAt: &startedAt},
	})

	m := newTUIModel(r)
	updated, _ := m.Update(keyPress('d'))
	m = updated.(tuiModel)

	if !strings.Contains(m.status, "busy") {
		t.Errorf("expected error about busy worker, got: %q", m.status)
	}
}

func TestTUIResetDoneWorker(t *testing.T) {
	ticket := "AMBA-42"
	startedAt := "2026-05-20T14:00:00Z"
	completedAt := "2026-05-20T14:05:00Z"
	branch := "adam/amba-42-fix"

	r := setupTestPool(t, map[string]*WorkerState{
		"worker-aaa": {
			Status:      "done",
			Ticket:      &ticket,
			StartedAt:   &startedAt,
			CompletedAt: &completedAt,
			BranchName:  &branch,
		},
	})

	m := newTUIModel(r)
	updated, _ := m.Update(keyPress('d'))
	m = updated.(tuiModel)

	// Should show a reset-related status, not an error
	if strings.Contains(m.status, "busy") {
		t.Errorf("done worker should not trigger busy error, got: %q", m.status)
	}

	// Worker state on disk should be idle
	ws, err := loadWorkerState(r.workerStateFile("worker-aaa"))
	if err != nil {
		t.Fatalf("failed to load worker state: %v", err)
	}
	if ws.Status != "idle" {
		t.Errorf("worker status = %q, want idle", ws.Status)
	}
}

func TestTUIDispatchOpensTicketInput(t *testing.T) {
	r := setupTestPool(t, map[string]*WorkerState{
		"worker-aaa": newIdleWorkerState(),
	})

	m := newTUIModel(r)
	updated, _ := m.Update(keyPress('n'))
	m = updated.(tuiModel)

	if m.view != viewDispatch {
		t.Errorf("view = %d, want viewDispatch (%d)", m.view, viewDispatch)
	}
}

func TestTUIDispatchNoIdleWorkers(t *testing.T) {
	ticket := "AMBA-1"
	startedAt := "2026-05-20T14:00:00Z"
	r := setupTestPool(t, map[string]*WorkerState{
		"worker-aaa": {Status: "busy", Ticket: &ticket, StartedAt: &startedAt},
	})

	m := newTUIModel(r)
	updated, _ := m.Update(keyPress('n'))
	m = updated.(tuiModel)

	// Should still open the dispatch input - pool resize happens on submit
	if m.view != viewDispatch {
		t.Errorf("view = %d, want viewDispatch (%d)", m.view, viewDispatch)
	}
}

func TestTUIDispatchEscCancels(t *testing.T) {
	r := setupTestPool(t, map[string]*WorkerState{
		"worker-aaa": newIdleWorkerState(),
	})

	m := newTUIModel(r)
	updated, _ := m.Update(keyPress('n'))
	m = updated.(tuiModel)

	updated, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	m = updated.(tuiModel)

	if m.view != viewList {
		t.Errorf("after esc: view = %d, want viewList (%d)", m.view, viewList)
	}
}

func TestTUIFollowSwitchesToTailView(t *testing.T) {
	logFile := "/tmp/test.log"
	r := setupTestPool(t, map[string]*WorkerState{
		"worker-aaa": {Status: "busy", LogFile: &logFile},
	})

	m := newTUIModel(r)
	updated, _ := m.Update(keyPress('f'))
	m = updated.(tuiModel)

	if m.view != viewTail {
		t.Errorf("view = %d, want viewTail (%d)", m.view, viewTail)
	}
}

func TestTUITailViewQReturnsToList(t *testing.T) {
	logFile := "/tmp/test.log"
	r := setupTestPool(t, map[string]*WorkerState{
		"worker-aaa": {Status: "busy", LogFile: &logFile},
	})

	m := newTUIModel(r)
	// Enter tail view
	updated, _ := m.Update(keyPress('f'))
	m = updated.(tuiModel)
	if m.view != viewTail {
		t.Fatalf("expected viewTail, got %d", m.view)
	}

	// q returns to list
	updated, _ = m.Update(keyPress('q'))
	m = updated.(tuiModel)
	if m.view != viewList {
		t.Errorf("after q in tail: view = %d, want viewList (%d)", m.view, viewList)
	}
}

func TestTUITailViewEscReturnsToList(t *testing.T) {
	logFile := "/tmp/test.log"
	r := setupTestPool(t, map[string]*WorkerState{
		"worker-aaa": {Status: "busy", LogFile: &logFile},
	})

	m := newTUIModel(r)
	updated, _ := m.Update(keyPress('f'))
	m = updated.(tuiModel)

	// Esc returns to list
	updated, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	m = updated.(tuiModel)
	if m.view != viewList {
		t.Errorf("after esc in tail: view = %d, want viewList (%d)", m.view, viewList)
	}
}

func TestTUISendOpensInputView(t *testing.T) {
	logFile := "/tmp/test.log"
	ticket := "AMBA-1"
	r := setupTestPool(t, map[string]*WorkerState{
		"worker-aaa": {Status: "done", Ticket: &ticket, LogFile: &logFile},
	})

	m := newTUIModel(r)
	updated, _ := m.Update(keyPress('s'))
	m = updated.(tuiModel)

	if m.view != viewInput {
		t.Errorf("view = %d, want viewInput (%d)", m.view, viewInput)
	}
}

func TestTUIInputViewEscCancels(t *testing.T) {
	logFile := "/tmp/test.log"
	ticket := "AMBA-1"
	r := setupTestPool(t, map[string]*WorkerState{
		"worker-aaa": {Status: "done", Ticket: &ticket, LogFile: &logFile},
	})

	m := newTUIModel(r)
	// Enter input view
	updated, _ := m.Update(keyPress('s'))
	m = updated.(tuiModel)
	if m.view != viewInput {
		t.Fatalf("expected viewInput, got %d", m.view)
	}

	// Esc cancels back to list
	updated, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	m = updated.(tuiModel)
	if m.view != viewList {
		t.Errorf("after esc in input: view = %d, want viewList (%d)", m.view, viewList)
	}
}

func TestTUITickRefreshesWorkerState(t *testing.T) {
	r := setupTestPool(t, map[string]*WorkerState{
		"worker-aaa": newIdleWorkerState(),
	})

	m := newTUIModel(r)
	if m.workers[0].state.Status != "idle" {
		t.Fatalf("initial status = %q, want idle", m.workers[0].state.Status)
	}

	// Simulate worker becoming busy by writing new state to disk
	ticket := "AMBA-99"
	startedAt := "2026-05-22T10:00:00Z"
	saveWorkerState(r.workerStateFile("worker-aaa"), &WorkerState{
		Status:    "busy",
		Ticket:    &ticket,
		StartedAt: &startedAt,
	})

	// Send tick message
	updated, _ := m.Update(tickMsg{})
	m = updated.(tuiModel)

	if m.workers[0].state.Status != "busy" {
		t.Errorf("after tick: status = %q, want busy", m.workers[0].state.Status)
	}
	if m.workers[0].state.Ticket == nil || *m.workers[0].state.Ticket != "AMBA-99" {
		t.Errorf("after tick: ticket = %v, want AMBA-99", m.workers[0].state.Ticket)
	}
}

func TestTUINoPoolQuitsImmediately(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, ".jj"), 0755)
	r := &RepoContext{Root: dir, BaseDir: dir + "-workspaces"}

	m := newTUIModel(r)
	if !m.quitting {
		t.Errorf("model should be quitting when no pool exists")
	}
	if !strings.Contains(m.status, "No pool") {
		t.Errorf("status should mention no pool, got: %q", m.status)
	}
}

// Ensure the model loads workers in the order from pool config
func TestTUIModelLoadsFromDisk(t *testing.T) {
	dir := t.TempDir()
	poolDir := filepath.Join(dir, ".jj", "pool")
	os.MkdirAll(poolDir, 0755)

	r := &RepoContext{Root: dir, BaseDir: dir + "-workspaces"}

	cfg := &PoolConfig{
		Size:    2,
		Workers: []string{"worker-first", "worker-second"},
	}
	data, _ := json.MarshalIndent(cfg, "", "  ")
	os.WriteFile(r.poolConfigFile(), data, 0644)

	saveWorkerState(filepath.Join(poolDir, "worker-first.json"), newIdleWorkerState())
	ticket := "AMBA-1"
	saveWorkerState(filepath.Join(poolDir, "worker-second.json"), &WorkerState{
		Status: "busy",
		Ticket: &ticket,
	})

	m := newTUIModel(r)
	if len(m.workers) != 2 {
		t.Fatalf("workers count = %d, want 2", len(m.workers))
	}
	if m.workers[0].name != "worker-first" {
		t.Errorf("first worker = %q, want worker-first", m.workers[0].name)
	}
	if m.workers[1].state.Status != "busy" {
		t.Errorf("second worker status = %q, want busy", m.workers[1].state.Status)
	}
}
