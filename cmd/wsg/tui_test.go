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
			Status:    WorkerStatusBusy,
			Ticket:    &ticket,
			StartedAt: &startedAt,
		},
		"worker-ccc": {
			Status:      WorkerStatusDone,
			Ticket:      &ticket,
			StartedAt:   &startedAt,
			CompletedAt: &completedAt,
			Branch:      branch,
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

func shiftEnter() tea.KeyPressMsg {
	return tea.KeyPressMsg{Code: tea.KeyEnter, Mod: tea.ModShift}
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
		"worker-aaa": {Status: WorkerStatusBusy, Ticket: &ticket, StartedAt: &startedAt},
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
			Status:      WorkerStatusDone,
			Ticket:      &ticket,
			StartedAt:   &startedAt,
			CompletedAt: &completedAt,
			Branch:      branch,
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

func TestTUIResetBlockedForBusyWorker(t *testing.T) {
	ticket := "AMBA-42"
	startedAt := "2026-05-20T14:00:00Z"
	r := setupTestPool(t, map[string]*WorkerState{
		"worker-aaa": {Status: WorkerStatusBusy, Ticket: &ticket, StartedAt: &startedAt},
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
			Status:      WorkerStatusDone,
			Ticket:      &ticket,
			StartedAt:   &startedAt,
			CompletedAt: &completedAt,
			Branch:      branch,
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
	if ws.Status != WorkerStatusIdle {
		t.Errorf("worker status = %q, want idle", ws.Status)
	}
}

func TestTUIKillBusyWorker(t *testing.T) {
	ticket := "AMBA-42"
	startedAt := "2026-05-20T14:00:00Z"
	logFile := "/tmp/test.log"
	r := setupTestPool(t, map[string]*WorkerState{
		"worker-aaa": {
			Status:    WorkerStatusBusy,
			Ticket:    &ticket,
			StartedAt: &startedAt,
			LogFile:   &logFile,
		},
	})

	m := newTUIModel(r)
	updated, cmd := m.Update(tea.KeyPressMsg{Code: 'K', Text: "K"})
	m = updated.(tuiModel)

	if !strings.Contains(m.status, "Killing") {
		t.Errorf("expected killing status, got: %q", m.status)
	}
	if cmd == nil {
		t.Fatal("expected a command to be returned for kill")
	}

	// Execute the kill command (no PID set, so no real process kill happens)
	msg := cmd()
	updated, _ = m.Update(msg)
	m = updated.(tuiModel)

	if !strings.Contains(m.status, "Killed") {
		t.Errorf("expected killed status, got: %q", m.status)
	}

	ws, err := loadWorkerState(r.workerStateFile("worker-aaa"))
	if err != nil {
		t.Fatalf("failed to load worker state: %v", err)
	}
	if ws.Status != WorkerStatusIdle {
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
	// [n] targets the selected worker, so the dispatch is pinned to it.
	if m.dispatchWorker != "worker-aaa" {
		t.Errorf("dispatchWorker = %q, want worker-aaa", m.dispatchWorker)
	}
}

func TestTUIDispatchSelectedBusyWorkerBlocked(t *testing.T) {
	ticket := "AMBA-1"
	startedAt := "2026-05-20T14:00:00Z"
	r := setupTestPool(t, map[string]*WorkerState{
		"worker-aaa": {Status: WorkerStatusBusy, Ticket: &ticket, StartedAt: &startedAt},
	})

	m := newTUIModel(r)
	updated, _ := m.Update(keyPress('n'))
	m = updated.(tuiModel)

	// [n] dispatches to the selected worker; a busy one is refused and the
	// view stays on the list.
	if m.view != viewList {
		t.Errorf("view = %d, want viewList (%d)", m.view, viewList)
	}
	if !strings.Contains(m.status, "busy") {
		t.Errorf("expected busy status, got: %q", m.status)
	}
}

func TestTUIDispatchAnyIdleOpensInput(t *testing.T) {
	r := setupTestPool(t, map[string]*WorkerState{
		"worker-aaa": newIdleWorkerState(),
	})

	m := newTUIModel(r)
	updated, _ := m.Update(keyPress('N'))
	m = updated.(tuiModel)

	// [N] is the any-idle path: opens the input with no pinned worker.
	if m.view != viewDispatch {
		t.Errorf("view = %d, want viewDispatch (%d)", m.view, viewDispatch)
	}
	if m.dispatchWorker != "" {
		t.Errorf("dispatchWorker = %q, want empty (any idle)", m.dispatchWorker)
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
		"worker-aaa": {Status: WorkerStatusBusy, LogFile: &logFile},
	})

	m := newTUIModel(r)
	updated, _ := m.Update(keyPress('f'))
	m = updated.(tuiModel)

	if m.view != viewTail {
		t.Errorf("view = %d, want viewTail (%d)", m.view, viewTail)
	}
}

func TestTUITailPreservesClaudeAgentAssociationAcrossPolls(t *testing.T) {
	logFile := filepath.Join(t.TempDir(), "worker.log")
	spawn := `{"type":"assistant","parent_tool_use_id":null,"message":{"content":[{"type":"tool_use","name":"Agent","id":"a1","input":{"description":"First"}}]}}`
	if err := os.WriteFile(logFile, []byte(spawn+"\n"), 0644); err != nil {
		t.Fatal(err)
	}
	r := setupTestPool(t, map[string]*WorkerState{
		"worker-aaa": {Status: WorkerStatusBusy, LogFile: &logFile},
	})

	m := newTUIModel(r)
	updated, _ := m.Update(keyPress('f'))
	m = updated.(tuiModel)

	f, err := os.OpenFile(logFile, os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		t.Fatal(err)
	}
	followUp := `{"type":"assistant","parent_tool_use_id":"a1","message":{"content":[{"type":"text","text":"from first"}]}}` + "\n" +
		`{"type":"user","parent_tool_use_id":null,"message":{"content":[{"type":"tool_result","tool_use_id":"a1","content":"done"}]}}` + "\n"
	if _, err := f.WriteString(followUp); err != nil {
		f.Close()
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	m.loadTailLines()
	got := strings.Join(m.tailLines, "\n")
	if !strings.Contains(got, "[First] from first") || !strings.Contains(got, "Agent First completed") {
		t.Errorf("incremental tail lost agent association:\n%s", got)
	}
}

func TestTUITailViewQReturnsToList(t *testing.T) {
	logFile := "/tmp/test.log"
	r := setupTestPool(t, map[string]*WorkerState{
		"worker-aaa": {Status: WorkerStatusBusy, LogFile: &logFile},
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
		"worker-aaa": {Status: WorkerStatusBusy, LogFile: &logFile},
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
		"worker-aaa": {Status: WorkerStatusDone, Ticket: &ticket, LogFile: &logFile},
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
		"worker-aaa": {Status: WorkerStatusDone, Ticket: &ticket, LogFile: &logFile},
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

func TestTUIInputShiftEnterInsertsVisibleNewline(t *testing.T) {
	logFile := "/tmp/test.log"
	ticket := "AMBA-1"
	r := setupTestPool(t, map[string]*WorkerState{
		"worker-aaa": {Status: WorkerStatusDone, Ticket: &ticket, LogFile: &logFile},
	})

	m := newTUIModel(r)
	updated, _ := m.Update(keyPress('s'))
	m = updated.(tuiModel)
	m.textArea.SetValue("first line")

	updated, _ = m.Update(shiftEnter())
	m = updated.(tuiModel)
	if m.view != viewInput {
		t.Fatalf("shift+enter submitted input: view = %d, want viewInput (%d)", m.view, viewInput)
	}
	if got := m.textArea.Value(); got != "first line\n" {
		t.Fatalf("textarea value = %q, want %q", got, "first line\n")
	}
	if got := m.textArea.Height(); got != 2 {
		t.Fatalf("textarea height = %d, want 2", got)
	}

	updated, _ = m.Update(keyPress('x'))
	m = updated.(tuiModel)
	view := m.renderInput()
	if !strings.Contains(view, "first line") || !strings.Contains(view, "x") {
		t.Fatalf("input view does not show both lines:\n%s", view)
	}
}

func TestTUIInputShowsAllEnteredLines(t *testing.T) {
	logFile := "/tmp/test.log"
	ticket := "AMBA-1"
	r := setupTestPool(t, map[string]*WorkerState{
		"worker-aaa": {Status: WorkerStatusDone, Ticket: &ticket, LogFile: &logFile},
	})

	m := newTUIModel(r)
	updated, _ := m.Update(keyPress('s'))
	m = updated.(tuiModel)
	m.textArea.SetValue("one\ntwo\nthree\nfour")

	view := m.renderInput()
	for _, line := range []string{"one", "two", "three", "four"} {
		if !strings.Contains(view, line) {
			t.Fatalf("input view does not show line %q:\n%s", line, view)
		}
	}
	if got := m.textArea.Height(); got != 4 {
		t.Fatalf("textarea height = %d, want 4", got)
	}
}

func TestTUIDispatchShiftEnterInsertsVisibleNewline(t *testing.T) {
	r := setupTestPool(t, map[string]*WorkerState{
		"worker-aaa": newIdleWorkerState(),
	})

	m := newTUIModel(r)
	updated, _ := m.Update(keyPress('N'))
	m = updated.(tuiModel)
	m.dispatchArea.SetValue("AMBA-1")

	updated, _ = m.Update(shiftEnter())
	m = updated.(tuiModel)
	if m.view != viewDispatch {
		t.Fatalf("shift+enter submitted dispatch: view = %d, want viewDispatch (%d)", m.view, viewDispatch)
	}
	if got := m.dispatchArea.Value(); got != "AMBA-1\n" {
		t.Fatalf("dispatch textarea value = %q, want %q", got, "AMBA-1\n")
	}
	if got := m.dispatchArea.Height(); got != 2 {
		t.Fatalf("dispatch textarea height = %d, want 2", got)
	}

	updated, _ = m.Update(keyPress('x'))
	m = updated.(tuiModel)
	view := m.renderDispatch()
	if !strings.Contains(view, "AMBA-1") || !strings.Contains(view, "x") {
		t.Fatalf("dispatch view does not show both lines:\n%s", view)
	}
}

func TestTUIDispatchShowsAllEnteredLines(t *testing.T) {
	r := setupTestPool(t, map[string]*WorkerState{
		"worker-aaa": newIdleWorkerState(),
	})

	m := newTUIModel(r)
	updated, _ := m.Update(keyPress('N'))
	m = updated.(tuiModel)
	m.dispatchArea.SetValue("AMBA-1\nAMBA-2")

	view := m.renderDispatch()
	for _, line := range []string{"AMBA-1", "AMBA-2"} {
		if !strings.Contains(view, line) {
			t.Fatalf("dispatch view does not show line %q:\n%s", line, view)
		}
	}
	if got := m.dispatchArea.Height(); got != 2 {
		t.Fatalf("dispatch textarea height = %d, want 2", got)
	}
}

func TestTUIMultilineEditorsCapVisibleHeightWithoutTruncatingContent(t *testing.T) {
	m := tuiModel{width: 80}
	ta := m.newMultilineTextArea("message")
	lines := []string{"1", "2", "3", "4", "5", "6", "7", "8", "9"}
	ta.SetValue(strings.Join(lines, "\n"))

	if got := ta.Height(); got != maxEditorHeight {
		t.Fatalf("textarea height = %d, want %d", got, maxEditorHeight)
	}
	if got := ta.Value(); got != strings.Join(lines, "\n") {
		t.Fatalf("textarea content was truncated: got %q", got)
	}
}

func TestTUITickRefreshesWorkerState(t *testing.T) {
	r := setupTestPool(t, map[string]*WorkerState{
		"worker-aaa": newIdleWorkerState(),
	})

	m := newTUIModel(r)
	if m.workers[0].state.Status != WorkerStatusIdle {
		t.Fatalf("initial status = %q, want idle", m.workers[0].state.Status)
	}

	// Simulate worker becoming busy by writing new state to disk
	ticket := "AMBA-99"
	startedAt := "2026-05-22T10:00:00Z"
	saveWorkerState(r.workerStateFile("worker-aaa"), &WorkerState{
		Status:    WorkerStatusBusy,
		Ticket:    &ticket,
		StartedAt: &startedAt,
	})

	// Send tick message
	updated, _ := m.Update(tickMsg{})
	m = updated.(tuiModel)

	if m.workers[0].state.Status != WorkerStatusBusy {
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
func TestSplitTickets(t *testing.T) {
	tests := []struct {
		input string
		want  []string
	}{
		{"AMBA-42", []string{"AMBA-42"}},
		{"amba-42", []string{"AMBA-42"}},
		{"AMBA-42 AMBA-43", []string{"AMBA-42", "AMBA-43"}},
		{"AMBA-42, AMBA-43", []string{"AMBA-42", "AMBA-43"}},
		{"AMBA-42,AMBA-43,AMBA-44", []string{"AMBA-42", "AMBA-43", "AMBA-44"}},
		{"  AMBA-42  AMBA-43  ", []string{"AMBA-42", "AMBA-43"}},
		{"AMBA-42 AMBA-42", []string{"AMBA-42"}},
		{"", nil},
		{"  ", nil},
	}

	for _, tt := range tests {
		got := splitTickets(tt.input)
		if len(got) != len(tt.want) {
			t.Errorf("splitTickets(%q) = %v, want %v", tt.input, got, tt.want)
			continue
		}
		for i := range got {
			if got[i] != tt.want[i] {
				t.Errorf("splitTickets(%q)[%d] = %q, want %q", tt.input, i, got[i], tt.want[i])
			}
		}
	}
}

func TestTUIFetchAllKeybind(t *testing.T) {
	r := setupTestPool(t, map[string]*WorkerState{
		"worker-aaa": newIdleWorkerState(),
	})

	m := newTUIModel(r)
	updated, cmd := m.Update(keyPress('A'))
	m = updated.(tuiModel)

	if !strings.Contains(m.status, "Fetching") {
		t.Errorf("expected fetching status, got: %q", m.status)
	}
	if cmd == nil {
		t.Error("expected a command to be returned for fetch-all")
	}
}

func TestTUIDispatchAnyIdlePlaceholderShowsMultiple(t *testing.T) {
	r := setupTestPool(t, map[string]*WorkerState{
		"worker-aaa": newIdleWorkerState(),
	})

	m := newTUIModel(r)
	updated, _ := m.Update(keyPress('N'))
	m = updated.(tuiModel)

	view := m.renderDispatch()
	if !strings.Contains(view, "ticket(s)") {
		t.Errorf("dispatch view should mention ticket(s), got:\n%s", view)
	}
}

func TestTUIRenameOpensView(t *testing.T) {
	r := setupTestPool(t, map[string]*WorkerState{
		"worker-aaa": newIdleWorkerState(),
	})

	m := newTUIModel(r)
	updated, _ := m.Update(keyPress('a'))
	m = updated.(tuiModel)

	if m.view != viewRename {
		t.Errorf("view = %d, want viewRename (%d)", m.view, viewRename)
	}
	if m.renameWorker != "worker-aaa" {
		t.Errorf("renameWorker = %q, want worker-aaa", m.renameWorker)
	}
}

func TestTUIRenameEscCancels(t *testing.T) {
	r := setupTestPool(t, map[string]*WorkerState{
		"worker-aaa": newIdleWorkerState(),
	})

	m := newTUIModel(r)
	updated, _ := m.Update(keyPress('a'))
	m = updated.(tuiModel)
	if m.view != viewRename {
		t.Fatalf("expected viewRename, got %d", m.view)
	}

	updated, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	m = updated.(tuiModel)
	if m.view != viewList {
		t.Errorf("after esc in rename: view = %d, want viewList (%d)", m.view, viewList)
	}
	// Esc must not write a name.
	cfg, _ := loadPoolConfig(r.poolConfigFile())
	if _, ok := cfg.Names["worker-aaa"]; ok {
		t.Errorf("esc should not persist a name, got %q", cfg.Names["worker-aaa"])
	}
}

func TestTUIRenameSavesName(t *testing.T) {
	r := setupTestPool(t, map[string]*WorkerState{
		"worker-aaa": newIdleWorkerState(),
	})

	m := newTUIModel(r)
	updated, _ := m.Update(keyPress('a'))
	m = updated.(tuiModel)

	m.textArea.SetValue("backend")
	updated, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = updated.(tuiModel)

	if m.view != viewList {
		t.Errorf("after save: view = %d, want viewList (%d)", m.view, viewList)
	}
	if !strings.Contains(m.status, "backend") {
		t.Errorf("status should mention the new name, got: %q", m.status)
	}
	cfg, _ := loadPoolConfig(r.poolConfigFile())
	if cfg.Names["worker-aaa"] != "backend" {
		t.Errorf("persisted name = %q, want backend", cfg.Names["worker-aaa"])
	}
	// The list reload picks up the alias for display.
	if m.workers[0].alias != "backend" {
		t.Errorf("worker alias = %q, want backend", m.workers[0].alias)
	}
}

func TestTUIViewRendersWorkerName(t *testing.T) {
	origTTY := isTTY
	isTTY = false
	defer func() { isTTY = origTTY }()

	r := setupTestPool(t, map[string]*WorkerState{
		"worker-aaa": newIdleWorkerState(),
	})
	if err := NewActions(r).Rename("worker-aaa", "backend"); err != nil {
		t.Fatalf("Rename: %v", err)
	}

	m := newTUIModel(r)
	view := m.renderList()
	if !strings.Contains(view, "NAME") {
		t.Errorf("view missing NAME column header:\n%s", view)
	}
	if !strings.Contains(view, "backend") {
		t.Errorf("view missing alias backend:\n%s", view)
	}
}

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
		Status: WorkerStatusBusy,
		Ticket: &ticket,
	})

	m := newTUIModel(r)
	if len(m.workers) != 2 {
		t.Fatalf("workers count = %d, want 2", len(m.workers))
	}
	if m.workers[0].name != "worker-first" {
		t.Errorf("first worker = %q, want worker-first", m.workers[0].name)
	}
	if m.workers[1].state.Status != WorkerStatusBusy {
		t.Errorf("second worker status = %q, want busy", m.workers[1].state.Status)
	}
}
