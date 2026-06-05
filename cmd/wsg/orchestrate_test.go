package main

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func writeLogFile(t *testing.T, lines ...string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "worker.log")
	var content string
	for _, l := range lines {
		content += l + "\n"
	}
	os.WriteFile(path, []byte(content), 0644)
	return path
}

func TestReadLogResultSuccess(t *testing.T) {
	path := writeLogFile(t,
		`{"type":"assistant","message":"working on it..."}`,
		`{"type":"result","subtype":"success","is_error":false,"result":"All done"}`,
	)
	r := readLogResult(path)
	if r == nil {
		t.Fatal("expected non-nil result")
	}
	if r.Status != "done" {
		t.Errorf("status = %q, want done", r.Status)
	}
	if r.ExitCode == nil || *r.ExitCode != 0 {
		t.Errorf("exitCode = %v, want 0", r.ExitCode)
	}
	if r.Error != nil {
		t.Errorf("error = %v, want nil", r.Error)
	}
}

func TestReadLogResultError(t *testing.T) {
	path := writeLogFile(t,
		`{"type":"assistant","message":"trying..."}`,
		`{"type":"result","subtype":"error","is_error":true,"result":"tool call failed"}`,
	)
	r := readLogResult(path)
	if r == nil {
		t.Fatal("expected non-nil result")
	}
	if r.Status != "failed" {
		t.Errorf("status = %q, want failed", r.Status)
	}
	if r.ExitCode == nil || *r.ExitCode != 1 {
		t.Errorf("exitCode = %v, want 1", r.ExitCode)
	}
	if r.Error == nil || *r.Error != "tool call failed" {
		t.Errorf("error = %v, want 'tool call failed'", r.Error)
	}
}

func TestReadLogResultErrorNoMessage(t *testing.T) {
	path := writeLogFile(t,
		`{"type":"result","subtype":"error","is_error":true,"result":""}`,
	)
	r := readLogResult(path)
	if r == nil {
		t.Fatal("expected non-nil result")
	}
	if r.Error == nil || *r.Error != "error" {
		t.Errorf("error = %v, want 'error' (falls back to subtype)", r.Error)
	}
}

func TestReadLogResultNotResult(t *testing.T) {
	path := writeLogFile(t,
		`{"type":"assistant","message":"still running..."}`,
	)
	r := readLogResult(path)
	if r != nil {
		t.Errorf("expected nil for non-result last line, got %+v", r)
	}
}

func TestReadLogResultEmptyFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "worker.log")
	os.WriteFile(path, []byte(""), 0644)
	r := readLogResult(path)
	if r != nil {
		t.Errorf("expected nil for empty file, got %+v", r)
	}
}

func TestReadLogResultMissingFile(t *testing.T) {
	r := readLogResult("/nonexistent/path/worker.log")
	if r != nil {
		t.Errorf("expected nil for missing file, got %+v", r)
	}
}

func TestReadLogResultInvalidJSON(t *testing.T) {
	path := writeLogFile(t, `not json at all`)
	r := readLogResult(path)
	if r != nil {
		t.Errorf("expected nil for invalid JSON, got %+v", r)
	}
}

func TestCheckWorkerLivenessDeadProcessSuccessLog(t *testing.T) {
	dir := t.TempDir()
	poolDir := filepath.Join(dir, ".jj", "pool")
	os.MkdirAll(poolDir, 0755)
	r := &RepoContext{Root: dir, BaseDir: dir + "-workspaces"}

	logFile := filepath.Join(poolDir, "worker-1.log")
	os.WriteFile(logFile, []byte(`{"type":"result","subtype":"success","is_error":false,"result":"done"}`+"\n"), 0644)

	pid := 99999999
	ws := newIdleWorkerState()
	ws.MarkDispatched("AMBA-42", logFile, "amba-42")
	ws.SetPID(pid)
	saveWorkerState(r.workerStateFile("worker-1"), ws)

	h, _ := OpenWorker(r.workerStateFile("worker-1"))
	h.CheckLiveness(r, "worker-1")

	if h.State().Status != "done" {
		t.Errorf("status = %q, want done", h.State().Status)
	}
	if h.State().ExitCode == nil || *h.State().ExitCode != 0 {
		t.Errorf("exitCode = %v, want 0", h.State().ExitCode)
	}
}

func TestCheckWorkerLivenessDeadProcessFailLog(t *testing.T) {
	dir := t.TempDir()
	poolDir := filepath.Join(dir, ".jj", "pool")
	os.MkdirAll(poolDir, 0755)
	r := &RepoContext{Root: dir, BaseDir: dir + "-workspaces"}

	logFile := filepath.Join(poolDir, "worker-1.log")
	os.WriteFile(logFile, []byte(`{"type":"result","subtype":"error","is_error":true,"result":"crashed"}`+"\n"), 0644)

	pid := 99999999
	ws := newIdleWorkerState()
	ws.MarkDispatched("AMBA-42", logFile, "amba-42")
	ws.SetPID(pid)
	saveWorkerState(r.workerStateFile("worker-1"), ws)

	h, _ := OpenWorker(r.workerStateFile("worker-1"))
	h.CheckLiveness(r, "worker-1")

	if h.State().Status != "failed" {
		t.Errorf("status = %q, want failed", h.State().Status)
	}
	if h.State().Error == nil || *h.State().Error != "crashed" {
		t.Errorf("error = %v, want 'crashed'", h.State().Error)
	}
}

func TestCheckWorkerLivenessDeadProcessNoLog(t *testing.T) {
	dir := t.TempDir()
	poolDir := filepath.Join(dir, ".jj", "pool")
	os.MkdirAll(poolDir, 0755)
	r := &RepoContext{Root: dir, BaseDir: dir + "-workspaces"}

	logFile := filepath.Join(poolDir, "worker-1.log")
	os.WriteFile(logFile, []byte(`{"type":"assistant","message":"still going"}`+"\n"), 0644)

	pid := 99999999
	ws := newIdleWorkerState()
	ws.MarkDispatched("AMBA-42", logFile, "amba-42")
	ws.SetPID(pid)
	saveWorkerState(r.workerStateFile("worker-1"), ws)

	h, _ := OpenWorker(r.workerStateFile("worker-1"))
	h.CheckLiveness(r, "worker-1")

	if h.State().Status != "failed" {
		t.Errorf("status = %q, want failed (process exited unexpectedly)", h.State().Status)
	}
	if h.State().Error == nil || *h.State().Error != "Process exited unexpectedly" {
		t.Errorf("error = %v, want 'Process exited unexpectedly'", h.State().Error)
	}
}

func TestCheckWorkerLivenessIdleWorkerUnchanged(t *testing.T) {
	dir := t.TempDir()
	poolDir := filepath.Join(dir, ".jj", "pool")
	os.MkdirAll(poolDir, 0755)
	r := &RepoContext{Root: dir, BaseDir: dir + "-workspaces"}

	ws := newIdleWorkerState()
	saveWorkerState(r.workerStateFile("worker-1"), ws)

	h, _ := OpenWorker(r.workerStateFile("worker-1"))
	h.CheckLiveness(r, "worker-1")

	if h.State().Status != "idle" {
		t.Errorf("status = %q, want idle (should not change)", h.State().Status)
	}
}

func TestReadyToDispatchNoDeps(t *testing.T) {
	dg := &DispatchGroup{
		SubIssues: map[string]*SubIssueState{
			"AMBA-10": {Status: "pending", BlockedBy: nil},
			"AMBA-11": {Status: "pending", BlockedBy: nil},
		},
	}
	ready := dg.Ready()
	if len(ready) != 2 {
		t.Fatalf("got %d ready, want 2: %v", len(ready), ready)
	}
	if ready[0] != "AMBA-10" || ready[1] != "AMBA-11" {
		t.Errorf("ready = %v, want [AMBA-10 AMBA-11] (sorted)", ready)
	}
}

func TestReadyToDispatchWithDeps(t *testing.T) {
	dg := &DispatchGroup{
		SubIssues: map[string]*SubIssueState{
			"AMBA-10": {Status: "pending", BlockedBy: nil},
			"AMBA-11": {Status: "pending", BlockedBy: []string{"AMBA-10"}},
			"AMBA-12": {Status: "pending", BlockedBy: []string{"AMBA-10", "AMBA-11"}},
		},
	}
	ready := dg.Ready()
	if len(ready) != 1 || ready[0] != "AMBA-10" {
		t.Errorf("ready = %v, want [AMBA-10]", ready)
	}
}

func TestReadyToDispatchDepsResolved(t *testing.T) {
	dg := &DispatchGroup{
		SubIssues: map[string]*SubIssueState{
			"AMBA-10": {Status: "done"},
			"AMBA-11": {Status: "pending", BlockedBy: []string{"AMBA-10"}},
			"AMBA-12": {Status: "pending", BlockedBy: []string{"AMBA-10"}},
		},
	}
	ready := dg.Ready()
	if len(ready) != 2 {
		t.Fatalf("got %d ready, want 2: %v", len(ready), ready)
	}
}

func TestReadyToDispatchSkippedUnblocks(t *testing.T) {
	dg := &DispatchGroup{
		SubIssues: map[string]*SubIssueState{
			"AMBA-10": {Status: "skipped"},
			"AMBA-11": {Status: "pending", BlockedBy: []string{"AMBA-10"}},
		},
	}
	ready := dg.Ready()
	if len(ready) != 1 || ready[0] != "AMBA-11" {
		t.Errorf("ready = %v, want [AMBA-11] (skipped deps should unblock)", ready)
	}
}

func TestReadyToDispatchAlreadyDispatched(t *testing.T) {
	dg := &DispatchGroup{
		SubIssues: map[string]*SubIssueState{
			"AMBA-10": {Status: "dispatched"},
			"AMBA-11": {Status: "done"},
		},
	}
	ready := dg.Ready()
	if len(ready) != 0 {
		t.Errorf("ready = %v, want [] (dispatched/done should not be ready)", ready)
	}
}

func TestReadyToDispatchFailedBlocksDownstream(t *testing.T) {
	dg := &DispatchGroup{
		SubIssues: map[string]*SubIssueState{
			"AMBA-10": {Status: "failed"},
			"AMBA-11": {Status: "pending", BlockedBy: []string{"AMBA-10"}},
		},
	}
	ready := dg.Ready()
	if len(ready) != 0 {
		t.Errorf("ready = %v, want [] (failed dep should block)", ready)
	}
}

func TestIsGroupTerminal(t *testing.T) {
	tests := []struct {
		name     string
		statuses []string
		want     bool
	}{
		{"all done", []string{"done", "done"}, true},
		{"all skipped", []string{"skipped", "skipped"}, true},
		{"mixed terminal", []string{"done", "failed", "skipped"}, true},
		{"has pending", []string{"done", "pending"}, false},
		{"has dispatched", []string{"done", "dispatched"}, false},
		{"empty", nil, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dg := &DispatchGroup{SubIssues: make(map[string]*SubIssueState)}
			for i, s := range tt.statuses {
				dg.SubIssues[fmt.Sprintf("AMBA-%d", i)] = &SubIssueState{Status: s}
			}
			if got := dg.Terminal(); got != tt.want {
				t.Errorf("Terminal() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestCountGroupStatuses(t *testing.T) {
	dg := &DispatchGroup{
		SubIssues: map[string]*SubIssueState{
			"AMBA-1": {Status: "done"},
			"AMBA-2": {Status: "done"},
			"AMBA-3": {Status: "failed"},
			"AMBA-4": {Status: "skipped"},
			"AMBA-5": {Status: "skipped"},
			"AMBA-6": {Status: "skipped"},
		},
	}
	done, failed, skipped := dg.CountStatuses()
	if done != 2 {
		t.Errorf("done = %d, want 2", done)
	}
	if failed != 1 {
		t.Errorf("failed = %d, want 1", failed)
	}
	if skipped != 3 {
		t.Errorf("skipped = %d, want 3", skipped)
	}
}

func TestMaxWaveSize(t *testing.T) {
	tests := []struct {
		name string
		dg   *DispatchGroup
		want int
	}{
		{
			name: "all independent",
			dg: &DispatchGroup{SubIssues: map[string]*SubIssueState{
				"A": {Status: "pending"},
				"B": {Status: "pending"},
				"C": {Status: "pending"},
			}},
			want: 3,
		},
		{
			name: "linear chain",
			dg: &DispatchGroup{SubIssues: map[string]*SubIssueState{
				"A": {Status: "pending"},
				"B": {Status: "pending", BlockedBy: []string{"A"}},
				"C": {Status: "pending", BlockedBy: []string{"B"}},
			}},
			want: 1,
		},
		{
			name: "diamond",
			dg: &DispatchGroup{SubIssues: map[string]*SubIssueState{
				"A": {Status: "pending"},
				"B": {Status: "pending", BlockedBy: []string{"A"}},
				"C": {Status: "pending", BlockedBy: []string{"A"}},
				"D": {Status: "pending", BlockedBy: []string{"B", "C"}},
			}},
			want: 2,
		},
		{
			name: "skipped nodes excluded",
			dg: &DispatchGroup{SubIssues: map[string]*SubIssueState{
				"A": {Status: "skipped"},
				"B": {Status: "pending", BlockedBy: []string{"A"}},
				"C": {Status: "pending", BlockedBy: []string{"A"}},
			}},
			want: 2,
		},
		{
			name: "empty graph",
			dg:   &DispatchGroup{SubIssues: map[string]*SubIssueState{}},
			want: 0,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.dg.MaxWaveSize()
			if got != tt.want {
				t.Errorf("MaxWaveSize() = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestBaseBranchesForIssue(t *testing.T) {
	branch1 := "adam/amba-10-auth"
	branch2 := "adam/amba-11-api"
	dg := &DispatchGroup{
		SubIssues: map[string]*SubIssueState{
			"AMBA-10": {Status: "done", Branch: &branch1},
			"AMBA-11": {Status: "done", Branch: &branch2},
			"AMBA-12": {Status: "pending", BlockedBy: []string{"AMBA-10", "AMBA-11"}},
			"AMBA-13": {Status: "pending"},
		},
	}

	branches := dg.BaseBranchesFor("AMBA-12")
	if len(branches) != 2 {
		t.Fatalf("got %d branches, want 2", len(branches))
	}

	noBranches := dg.BaseBranchesFor("AMBA-13")
	if len(noBranches) != 0 {
		t.Errorf("got %d branches for no-dep issue, want 0", len(noBranches))
	}

	missing := dg.BaseBranchesFor("AMBA-99")
	if len(missing) != 0 {
		t.Errorf("got %d branches for missing issue, want 0", len(missing))
	}
}

func TestBuildDepContextNoDeps(t *testing.T) {
	dg := &DispatchGroup{
		SubIssues: map[string]*SubIssueState{
			"AMBA-10": {Status: "pending"},
		},
	}
	ctx := dg.DepContextFor("AMBA-10")
	if ctx != nil {
		t.Errorf("expected nil for no-dep issue, got %+v", ctx)
	}
}

func TestBuildDepContextAllMain(t *testing.T) {
	main := "main"
	dg := &DispatchGroup{
		SubIssues: map[string]*SubIssueState{
			"AMBA-10": {Status: "done", Branch: &main},
			"AMBA-11": {Status: "pending", BlockedBy: []string{"AMBA-10"}},
		},
	}
	ctx := dg.DepContextFor("AMBA-11")
	if ctx != nil {
		t.Errorf("expected nil when all deps are on main, got %+v", ctx)
	}
}

func TestBuildDepContextWithBranch(t *testing.T) {
	branch := "adam/amba-10-auth"
	dg := &DispatchGroup{
		SubIssues: map[string]*SubIssueState{
			"AMBA-10": {Status: "done", Branch: &branch, Title: "Add auth"},
			"AMBA-11": {Status: "pending", BlockedBy: []string{"AMBA-10"}},
		},
	}
	ctx := dg.DepContextFor("AMBA-11")
	if ctx == nil {
		t.Fatal("expected non-nil context for branch dep")
	}
	if len(ctx.BaseBranches) != 1 || ctx.BaseBranches[0] != branch {
		t.Errorf("BaseBranches = %v, want [%s]", ctx.BaseBranches, branch)
	}
	if ctx.PRBase != branch {
		t.Errorf("PRBase = %q, want %q", ctx.PRBase, branch)
	}
	if ctx.Context == "" {
		t.Error("Context should be non-empty")
	}
}

func setupPoolDir(t *testing.T) (string, *RepoContext) {
	t.Helper()
	dir := t.TempDir()
	poolDir := filepath.Join(dir, ".jj", "pool")
	os.MkdirAll(poolDir, 0755)
	r := &RepoContext{Root: dir, BaseDir: dir + "-workspaces"}
	return poolDir, r
}

func TestSyncGroupFromWorkersCompleted(t *testing.T) {
	poolDir, r := setupPoolDir(t)

	branch := "adam/amba-10-auth"
	ws := &WorkerState{Status: "done", BranchName: &branch}
	saveWorkerState(filepath.Join(poolDir, "worker-1.json"), ws)

	worker := "worker-1"
	dg := &DispatchGroup{
		SubIssues: map[string]*SubIssueState{
			"AMBA-10": {Status: "dispatched", Worker: &worker, Title: "Auth"},
		},
	}

	changed := dg.SyncFromWorkers(r)
	if !changed {
		t.Error("expected changed = true")
	}
	if dg.SubIssues["AMBA-10"].Status != "done" {
		t.Errorf("status = %q, want done", dg.SubIssues["AMBA-10"].Status)
	}
	if dg.SubIssues["AMBA-10"].Branch == nil || *dg.SubIssues["AMBA-10"].Branch != branch {
		t.Errorf("branch = %v, want %s", dg.SubIssues["AMBA-10"].Branch, branch)
	}
	if dg.SubIssues["AMBA-10"].CompletedAt == nil {
		t.Error("completedAt should be set")
	}
}

func TestSyncGroupFromWorkersFirstFailureRetries(t *testing.T) {
	poolDir, r := setupPoolDir(t)

	errMsg := "build failed"
	ws := &WorkerState{Status: "failed", Error: &errMsg}
	saveWorkerState(filepath.Join(poolDir, "worker-1.json"), ws)

	worker := "worker-1"
	dg := &DispatchGroup{
		SubIssues: map[string]*SubIssueState{
			"AMBA-10": {Status: "dispatched", Worker: &worker, Title: "Auth", Retries: 0},
		},
	}

	changed := dg.SyncFromWorkers(r)
	if !changed {
		t.Error("expected changed = true")
	}
	si := dg.SubIssues["AMBA-10"]
	if si.Status != "pending" {
		t.Errorf("status = %q, want pending (auto-retry)", si.Status)
	}
	if si.Worker != nil {
		t.Error("worker should be cleared for retry")
	}
	if si.DispatchedAt != nil {
		t.Error("dispatchedAt should be cleared for retry")
	}
	if si.Retries != 1 {
		t.Errorf("retries = %d, want 1", si.Retries)
	}
}

func TestSyncGroupFromWorkersSecondFailurePermanent(t *testing.T) {
	poolDir, r := setupPoolDir(t)

	errMsg := "build failed again"
	ws := &WorkerState{Status: "failed", Error: &errMsg}
	saveWorkerState(filepath.Join(poolDir, "worker-1.json"), ws)

	worker := "worker-1"
	dg := &DispatchGroup{
		SubIssues: map[string]*SubIssueState{
			"AMBA-10": {Status: "dispatched", Worker: &worker, Title: "Auth", Retries: 1},
		},
	}

	changed := dg.SyncFromWorkers(r)
	if !changed {
		t.Error("expected changed = true")
	}
	si := dg.SubIssues["AMBA-10"]
	if si.Status != "failed" {
		t.Errorf("status = %q, want failed (no more retries)", si.Status)
	}
	if si.CompletedAt == nil {
		t.Error("completedAt should be set on permanent failure")
	}
}

func TestSyncGroupFromWorkersStillBusy(t *testing.T) {
	poolDir, r := setupPoolDir(t)

	pid := os.Getpid()
	ws := &WorkerState{Status: "busy", PID: &pid}
	saveWorkerState(filepath.Join(poolDir, "worker-1.json"), ws)

	worker := "worker-1"
	dg := &DispatchGroup{
		SubIssues: map[string]*SubIssueState{
			"AMBA-10": {Status: "dispatched", Worker: &worker},
		},
	}

	changed := dg.SyncFromWorkers(r)
	if changed {
		t.Error("expected changed = false for still-busy worker")
	}
	if dg.SubIssues["AMBA-10"].Status != "dispatched" {
		t.Errorf("status = %q, want dispatched", dg.SubIssues["AMBA-10"].Status)
	}
}

func TestDispatchGroupRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "dispatch-amba-9.json")

	branch := "adam/amba-10-auth"
	worker := "worker-1"
	dg := &DispatchGroup{
		Parent:    "AMBA-9",
		CreatedAt: "2026-05-20T14:00:00Z",
		GHRepo:    "AmebaAI/mono",
		SubIssues: map[string]*SubIssueState{
			"AMBA-10": {
				Title:     "Auth module",
				Status:    "done",
				BlockedBy: nil,
				Worker:    &worker,
				Branch:    &branch,
			},
		},
		Opts: DispatchGroupOpts{Model: "opus"},
	}

	saveDispatchGroup(path, dg)
	loaded, err := loadDispatchGroup(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if loaded.Parent != "AMBA-9" {
		t.Errorf("parent = %q, want AMBA-9", loaded.Parent)
	}
	if loaded.Opts.Model != "opus" {
		t.Errorf("model = %q, want opus", loaded.Opts.Model)
	}
	si := loaded.SubIssues["AMBA-10"]
	if si == nil {
		t.Fatal("AMBA-10 missing from loaded group")
	}
	if si.Branch == nil || *si.Branch != branch {
		t.Errorf("branch = %v, want %s", si.Branch, branch)
	}
}

func TestIsDispatchableStatus(t *testing.T) {
	dispatchable := []string{"Backlog", "Todo", "Triage", "backlog", "todo", "triage"}
	for _, s := range dispatchable {
		if !isDispatchableStatus(s) {
			t.Errorf("isDispatchableStatus(%q) = false, want true", s)
		}
	}
	notDispatchable := []string{"In Progress", "In Review", "Done", "Merged", "Cancelled", ""}
	for _, s := range notDispatchable {
		if isDispatchableStatus(s) {
			t.Errorf("isDispatchableStatus(%q) = true, want false", s)
		}
	}
}

func TestIsMergedStatus(t *testing.T) {
	merged := []string{"Merged", "Done", "merged", "done"}
	for _, s := range merged {
		if !isMergedStatus(s) {
			t.Errorf("isMergedStatus(%q) = false, want true", s)
		}
	}
	notMerged := []string{"Backlog", "Todo", "In Progress", "In Review", "Cancelled", ""}
	for _, s := range notMerged {
		if isMergedStatus(s) {
			t.Errorf("isMergedStatus(%q) = true, want false", s)
		}
	}
}

func TestExtractJSON(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "pure JSON",
			input: `{"tickets": ["AMBA-1"]}`,
			want:  `{"tickets": ["AMBA-1"]}`,
		},
		{
			name:  "JSON with leading text",
			input: `Here is the result: {"key": "value"}`,
			want:  `{"key": "value"}`,
		},
		{
			name:  "JSON with trailing text",
			input: `{"key": "value"} some trailing text`,
			want:  `{"key": "value"}`,
		},
		{
			name:  "nested braces",
			input: `prefix {"outer": {"inner": 1}} suffix`,
			want:  `{"outer": {"inner": 1}}`,
		},
		{
			name:  "no JSON",
			input: `just plain text`,
			want:  `just plain text`,
		},
		{
			name:  "empty string",
			input: ``,
			want:  ``,
		},
		{
			name:  "markdown code block",
			input: "```json\n{\"sub_issues\": {}}\n```",
			want:  `{"sub_issues": {}}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractJSON(tt.input)
			if got != tt.want {
				t.Errorf("extractJSON(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}
