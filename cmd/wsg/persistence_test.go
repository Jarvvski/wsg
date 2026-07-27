package main

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestWorkerSaveRejectsAStaleHandle(t *testing.T) {
	dir := t.TempDir()
	poolDir := filepath.Join(dir, ".jj", "pool")
	if err := os.MkdirAll(poolDir, 0755); err != nil {
		t.Fatal(err)
	}
	r := &RepoContext{Root: dir, BaseDir: dir + "-workspaces"}
	if err := saveWorkerState(r.workerStateFile("worker-1"), newIdleWorkerState()); err != nil {
		t.Fatal(err)
	}

	first, err := loadWorker(r, "worker-1")
	if err != nil {
		t.Fatal(err)
	}
	stale, err := loadWorker(r, "worker-1")
	if err != nil {
		t.Fatal(err)
	}
	first.state.MarkDispatched("AMBA-1", filepath.Join(poolDir, "worker-1.log"), "amba-1")
	if err := first.save(); err != nil {
		t.Fatalf("first save: %v", err)
	}
	stale.state.MarkDispatched("AMBA-2", filepath.Join(poolDir, "worker-1.log"), "amba-2")
	if err := stale.save(); err == nil {
		t.Fatal("stale save succeeded")
	}

	persisted, err := loadWorkerState(r.workerStateFile("worker-1"))
	if err != nil {
		t.Fatal(err)
	}
	if persisted.Ticket == nil || *persisted.Ticket != "AMBA-1" {
		t.Fatalf("ticket = %v, want AMBA-1", persisted.Ticket)
	}
}

func TestWorkerResetRejectsAStaleHandle(t *testing.T) {
	dir := t.TempDir()
	poolDir := filepath.Join(dir, ".jj", "pool")
	if err := os.MkdirAll(poolDir, 0755); err != nil {
		t.Fatal(err)
	}
	r := &RepoContext{Root: dir, BaseDir: dir + "-workspaces"}
	if err := saveWorkerState(r.workerStateFile("worker-1"), newIdleWorkerState()); err != nil {
		t.Fatal(err)
	}

	current, err := loadWorker(r, "worker-1")
	if err != nil {
		t.Fatal(err)
	}
	stale, err := loadWorker(r, "worker-1")
	if err != nil {
		t.Fatal(err)
	}
	current.state.MarkDispatched("AMBA-1", filepath.Join(poolDir, "worker-1.log"), "amba-1")
	if err := current.save(); err != nil {
		t.Fatal(err)
	}
	if err := stale.reset(); !errors.Is(err, errStateConflict) {
		t.Fatalf("stale reset error = %v, want state conflict", err)
	}

	persisted, err := loadWorkerState(r.workerStateFile("worker-1"))
	if err != nil {
		t.Fatal(err)
	}
	if persisted.Status != WorkerStatusBusy || persisted.Ticket == nil || *persisted.Ticket != "AMBA-1" {
		t.Fatalf("persisted state = %#v, want AMBA-1 busy", persisted)
	}
}

func TestWorkerMutationPreservesUnknownFields(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "worker.json")
	source := []byte(`{"status":"idle","agent":null,"ticket":null,"pid":null,"started_at":null,"completed_at":null,"log_file":null,"branch_name":null,"exit_code":null,"error":null,"future":{"enabled":true}}`)
	if err := os.WriteFile(path, source, 0644); err != nil {
		t.Fatal(err)
	}
	h, err := OpenWorker(path)
	if err != nil {
		t.Fatal(err)
	}
	h.state.MarkDispatched("AMBA-1", "/tmp/worker.log", "amba-1")
	if err := h.save(); err != nil {
		t.Fatal(err)
	}
	var document map[string]json.RawMessage
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &document); err != nil {
		t.Fatal(err)
	}
	if _, ok := document["future"]; !ok {
		t.Fatal("unknown field was discarded")
	}
}

func TestPoolMutationPreservesUnknownFields(t *testing.T) {
	dir := t.TempDir()
	poolDir := filepath.Join(dir, ".jj", "pool")
	if err := os.MkdirAll(poolDir, 0755); err != nil {
		t.Fatal(err)
	}
	r := &RepoContext{Root: dir, BaseDir: dir + "-workspaces"}
	source := []byte(`{"size":1,"gh_repo":"owner/repo","workers":["worker-1"],"created_at":"now","future":{"enabled":true}}`)
	if err := os.WriteFile(r.poolConfigFile(), source, 0644); err != nil {
		t.Fatal(err)
	}
	pool, err := OpenPool(r)
	if err != nil {
		t.Fatal(err)
	}
	if err := pool.SetName("worker-1", "one"); err != nil {
		t.Fatal(err)
	}
	var document map[string]json.RawMessage
	data, err := os.ReadFile(r.poolConfigFile())
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &document); err != nil {
		t.Fatal(err)
	}
	if _, ok := document["future"]; !ok {
		t.Fatal("unknown pool field was discarded")
	}
}

func TestDispatchGroupMutationPreservesUnknownFields(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".jj", "pool"), 0755); err != nil {
		t.Fatal(err)
	}
	r := &RepoContext{Root: dir, BaseDir: dir + "-workspaces"}
	path := dispatchGroupFile(r, "AMBA-9")
	source := []byte(`{"parent":"AMBA-9","created_at":"now","gh_repo":"owner/repo","sub_issues":{"AMBA-10":{"title":"child","status":"pending","blocked_by":[],"worker":null,"branch":null,"dispatched_at":null,"completed_at":null,"retries":0,"future_child":true}},"opts":{"model":"test","future_opt":true},"future_group":true}`)
	if err := os.WriteFile(path, source, 0644); err != nil {
		t.Fatal(err)
	}
	group, err := loadDispatchGroup(path)
	if err != nil {
		t.Fatal(err)
	}
	group.GHRepo = "updated/repo"
	if err := group.Save(r); err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &document); err != nil {
		t.Fatal(err)
	}
	if _, ok := document["future_group"]; !ok {
		t.Fatal("unknown Dispatch Group field was discarded")
	}
	opts := document["opts"].(map[string]any)
	if _, ok := opts["future_opt"]; !ok {
		t.Fatal("unknown Dispatch Group option was discarded")
	}
	subIssues := document["sub_issues"].(map[string]any)
	child := subIssues["AMBA-10"].(map[string]any)
	if _, ok := child["future_child"]; !ok {
		t.Fatal("unknown Sub-issue field was discarded")
	}
}

func TestDispatchGroupSaveRejectsAStaleSnapshot(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".jj", "pool"), 0755); err != nil {
		t.Fatal(err)
	}
	r := &RepoContext{Root: dir, BaseDir: dir + "-workspaces"}
	seed := &DispatchGroup{Parent: "AMBA-9", SubIssues: map[string]*SubIssueState{}, Opts: DispatchGroupOpts{}}
	if err := seed.Save(r); err != nil {
		t.Fatal(err)
	}
	first, err := loadDispatchGroup(dispatchGroupFile(r, "AMBA-9"))
	if err != nil {
		t.Fatal(err)
	}
	stale, err := loadDispatchGroup(dispatchGroupFile(r, "AMBA-9"))
	if err != nil {
		t.Fatal(err)
	}
	first.GHRepo = "first/repo"
	if err := first.Save(r); err != nil {
		t.Fatalf("first save: %v", err)
	}
	stale.GHRepo = "stale/repo"
	if err := stale.Save(r); err == nil {
		t.Fatal("stale Dispatch Group save succeeded")
	}
	persisted, err := loadDispatchGroup(dispatchGroupFile(r, "AMBA-9"))
	if err != nil {
		t.Fatal(err)
	}
	if persisted.GHRepo != "first/repo" {
		t.Fatalf("gh_repo = %q, want first/repo", persisted.GHRepo)
	}
}

func TestPoolReserveReloadsWorkerAfterAcquiringItsLock(t *testing.T) {
	dir := t.TempDir()
	poolDir := filepath.Join(dir, ".jj", "pool")
	if err := os.MkdirAll(poolDir, 0755); err != nil {
		t.Fatal(err)
	}
	r := &RepoContext{Root: dir, BaseDir: dir + "-workspaces"}
	cfg := &PoolConfig{Size: 1, Workers: []string{"worker-1"}}
	if err := savePoolConfig(r.poolConfigFile(), cfg); err != nil {
		t.Fatal(err)
	}
	if err := saveWorkerState(r.workerStateFile("worker-1"), newIdleWorkerState()); err != nil {
		t.Fatal(err)
	}
	pool, err := OpenPool(r)
	if err != nil {
		t.Fatal(err)
	}

	locked := make(chan struct{})
	release := make(chan struct{})
	holderDone := make(chan error, 1)
	go func() {
		holderDone <- withStateLock(r.workerStateFile("worker-1")+".lock", func() error {
			close(locked)
			<-release
			busy := newIdleWorkerState()
			busy.MarkDispatched("AMBA-OTHER", filepath.Join(poolDir, "worker-1.log"), "amba-other")
			return saveWorkerState(r.workerStateFile("worker-1"), busy)
		})
	}()
	<-locked

	reserveDone := make(chan error, 1)
	go func() {
		_, err := pool.Reserve([]string{"AMBA-1"})
		reserveDone <- err
	}()
	select {
	case err := <-reserveDone:
		close(release)
		<-holderDone
		t.Fatalf("Reserve completed before Worker lock was released: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	if err := <-holderDone; err != nil {
		t.Fatal(err)
	}
	var full *PoolFull
	if err := <-reserveDone; !errors.As(err, &full) {
		t.Fatalf("Reserve error = %v, want PoolFull", err)
	}
	persisted, err := loadWorkerState(r.workerStateFile("worker-1"))
	if err != nil {
		t.Fatal(err)
	}
	if persisted.Ticket == nil || *persisted.Ticket != "AMBA-OTHER" {
		t.Fatalf("ticket = %v, want AMBA-OTHER", persisted.Ticket)
	}
}

func TestPoolDestroyPreservesStateLockSidecars(t *testing.T) {
	dir := t.TempDir()
	poolDir := filepath.Join(dir, ".jj", "pool")
	if err := os.MkdirAll(poolDir, 0755); err != nil {
		t.Fatal(err)
	}
	r := &RepoContext{Root: dir, BaseDir: dir + "-workspaces"}
	if err := savePoolConfig(r.poolConfigFile(), &PoolConfig{Workers: []string{}}); err != nil {
		t.Fatal(err)
	}
	groupLock := filepath.Join(poolDir, "dispatch-amba-9.json.lock")
	if err := os.WriteFile(groupLock, nil, 0644); err != nil {
		t.Fatal(err)
	}
	pool, err := OpenPool(r)
	if err != nil {
		t.Fatal(err)
	}
	if err := pool.Destroy(); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{filepath.Join(poolDir, ".dispatch.lock"), groupLock} {
		if !fileExists(path) {
			t.Fatalf("lock sidecar %s was removed", path)
		}
	}
	if fileExists(r.poolConfigFile()) {
		t.Fatal("pool config was not removed")
	}
}

func TestStateLockSubprocessHelper(t *testing.T) {
	mode := os.Getenv("WSG_STATE_HELPER_MODE")
	if mode == "" {
		return
	}
	if mode == "rewrite" {
		state, err := rewriteTypedStateForHelper(
			os.Getenv("WSG_STATE_HELPER_KIND"),
			os.Getenv("WSG_STATE_HELPER_STATE"),
		)
		if err != nil {
			t.Fatal(err)
		}
		data, err := json.Marshal(state)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(os.Getenv("WSG_STATE_HELPER_RESULT"), data, 0644); err != nil {
			t.Fatal(err)
		}
		return
	}

	lockPath := os.Getenv("WSG_STATE_HELPER_LOCK")
	readyPath := os.Getenv("WSG_STATE_HELPER_READY")
	err := withStateLock(lockPath, func() error {
		if err := os.WriteFile(readyPath, []byte("locked"), 0644); err != nil {
			return err
		}
		if mode == "hold" {
			time.Sleep(30 * time.Second)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func rewriteTypedStateForHelper(kind, path string) (any, error) {
	switch kind {
	case "pool":
		state, err := loadPoolConfig(path)
		if err != nil {
			return nil, err
		}
		expected, err := currentRevision(path)
		if err != nil {
			return nil, err
		}
		lockPath := filepath.Join(filepath.Dir(path), "pool", ".dispatch.lock")
		if _, err := commitJSONState(path, lockPath, expected, state); err != nil {
			return nil, err
		}
		return state, nil
	case "worker":
		handle, err := OpenWorker(path)
		if err != nil {
			return nil, err
		}
		if err := handle.save(); err != nil {
			return nil, err
		}
		return handle.state, nil
	case "dispatch":
		state, err := loadDispatchGroup(path)
		if err != nil {
			return nil, err
		}
		if _, err := commitJSONState(path, path+".lock", state.revision, state); err != nil {
			return nil, err
		}
		return state, nil
	default:
		return nil, errors.New("unknown state helper kind")
	}
}

func TestStateHelperReadsAndRewritesTypedState(t *testing.T) {
	dir := t.TempDir()
	poolDir := filepath.Join(dir, ".jj", "pool")
	if err := os.MkdirAll(poolDir, 0755); err != nil {
		t.Fatal(err)
	}

	ticket := "AMBA-1"
	workerPath := filepath.Join(poolDir, "worker-1.json")
	if err := saveWorkerState(workerPath, &WorkerState{Status: WorkerStatusBusy, Ticket: &ticket}); err != nil {
		t.Fatal(err)
	}
	workerResult := runRewriteHelper(t, "worker", workerPath)
	var worker WorkerState
	if err := json.Unmarshal(workerResult, &worker); err != nil {
		t.Fatal(err)
	}
	if worker.Status != WorkerStatusBusy || worker.Ticket == nil || *worker.Ticket != ticket {
		t.Fatalf("worker result = %#v", worker)
	}

	poolPath := filepath.Join(dir, ".jj", "pool.json")
	if err := savePoolConfig(poolPath, &PoolConfig{Size: 1, Workers: []string{"worker-1"}}); err != nil {
		t.Fatal(err)
	}
	poolResult := runRewriteHelper(t, "pool", poolPath)
	var pool PoolConfig
	if err := json.Unmarshal(poolResult, &pool); err != nil {
		t.Fatal(err)
	}
	if pool.Size != 1 || len(pool.Workers) != 1 || pool.Workers[0] != "worker-1" {
		t.Fatalf("pool result = %#v", pool)
	}

	dispatchPath := filepath.Join(poolDir, "dispatch-amba-9.json")
	group := &DispatchGroup{Parent: "AMBA-9", SubIssues: map[string]*SubIssueState{}}
	if err := saveDispatchGroup(dispatchPath, group); err != nil {
		t.Fatal(err)
	}
	dispatchResult := runRewriteHelper(t, "dispatch", dispatchPath)
	var dispatch DispatchGroup
	if err := json.Unmarshal(dispatchResult, &dispatch); err != nil {
		t.Fatal(err)
	}
	if dispatch.Parent != "AMBA-9" {
		t.Fatalf("dispatch result = %#v", dispatch)
	}
}

func runRewriteHelper(t *testing.T, kind, statePath string) []byte {
	t.Helper()
	resultPath := filepath.Join(t.TempDir(), "result.json")
	command := exec.Command(os.Args[0], "-test.run", "^TestStateLockSubprocessHelper$")
	command.Env = append(os.Environ(),
		"WSG_STATE_HELPER_MODE=rewrite",
		"WSG_STATE_HELPER_KIND="+kind,
		"WSG_STATE_HELPER_STATE="+statePath,
		"WSG_STATE_HELPER_RESULT="+resultPath,
	)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("rewrite %s helper: %v\n%s", kind, err, output)
	}
	result, err := os.ReadFile(resultPath)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func TestStateLockSerializesIndependentProcesses(t *testing.T) {
	for _, lockName := range []string{".dispatch.lock", "worker-1.json.lock", "dispatch-amba-9.json.lock"} {
		t.Run(lockName, func(t *testing.T) {
			dir := t.TempDir()
			lockPath := filepath.Join(dir, lockName)
			firstReady := filepath.Join(dir, "first-ready")
			secondReady := filepath.Join(dir, "second-ready")
			command := func(mode string, ready string) *exec.Cmd {
				cmd := exec.Command(os.Args[0], "-test.run", "^TestStateLockSubprocessHelper$")
				cmd.Env = append(os.Environ(), "WSG_STATE_HELPER_MODE="+mode, "WSG_STATE_HELPER_LOCK="+lockPath, "WSG_STATE_HELPER_READY="+ready)
				return cmd
			}
			first := command("hold", firstReady)
			if err := first.Start(); err != nil {
				t.Fatal(err)
			}
			defer func() {
				_ = first.Process.Kill()
				_, _ = first.Process.Wait()
			}()
			deadline := time.Now().Add(3 * time.Second)
			for !fileExists(firstReady) && time.Now().Before(deadline) {
				time.Sleep(10 * time.Millisecond)
			}
			if !fileExists(firstReady) {
				t.Fatal("first helper did not acquire lock")
			}
			second := command("once", secondReady)
			if err := second.Start(); err != nil {
				t.Fatal(err)
			}
			time.Sleep(50 * time.Millisecond)
			if fileExists(secondReady) {
				t.Fatal("second helper acquired a held lock")
			}
			if err := first.Process.Kill(); err != nil {
				t.Fatal(err)
			}
			_, _ = first.Process.Wait()
			deadline = time.Now().Add(3 * time.Second)
			for !fileExists(secondReady) && time.Now().Before(deadline) {
				time.Sleep(10 * time.Millisecond)
			}
			if !fileExists(secondReady) {
				_ = second.Process.Kill()
				t.Fatal("second helper did not acquire released lock")
			}
			if err := second.Wait(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func TestAtomicStateWritesUseIndependentTemporaryFiles(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "worker.json")
	const writers = 32
	errs := make(chan error, writers)
	var wg sync.WaitGroup
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- saveWorkerState(path, newIdleWorkerState())
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent write: %v", err)
		}
	}
	if _, err := loadWorkerState(path); err != nil {
		t.Fatalf("final state: %v", err)
	}
}

type failingStateTemporary struct {
	path  string
	stage string
}

func (temporary *failingStateTemporary) Name() string            { return temporary.path }
func (temporary *failingStateTemporary) Chmod(os.FileMode) error { return nil }
func (temporary *failingStateTemporary) Write(data []byte) (int, error) {
	if temporary.stage == "write" {
		return 0, errors.New("injected write failure")
	}
	if temporary.stage == "short-write" {
		return len(data) - 1, nil
	}
	return len(data), nil
}
func (temporary *failingStateTemporary) Sync() error {
	if temporary.stage == "sync" {
		return errors.New("injected sync failure")
	}
	return nil
}
func (temporary *failingStateTemporary) Close() error {
	if temporary.stage == "close" {
		return errors.New("injected close failure")
	}
	return nil
}

func TestFailedAtomicStateWritePreservesPreviousFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	previous := []byte("{\"status\":\"idle\"}\n")
	if err := os.WriteFile(path, previous, 0644); err != nil {
		t.Fatal(err)
	}

	originalCreate := createStateTemporary
	originalRename := renameStateFile
	defer func() {
		createStateTemporary = originalCreate
		renameStateFile = originalRename
	}()

	for _, stage := range []string{"write", "short-write", "sync", "close"} {
		t.Run(stage, func(t *testing.T) {
			createStateTemporary = func(parent, pattern string) (stateTemporary, error) {
				return &failingStateTemporary{path: filepath.Join(parent, pattern), stage: stage}, nil
			}
			renameStateFile = func(_, _ string) error { return nil }
			if err := writeJSONAtomic(path, newIdleWorkerState()); err == nil {
				t.Fatalf("%s failure succeeded", stage)
			}
			assertFileBytes(t, path, previous)
		})
	}

	createStateTemporary = originalCreate
	renameStateFile = func(_, _ string) error { return errors.New("injected rename failure") }
	if err := writeJSONAtomic(path, newIdleWorkerState()); err == nil {
		t.Fatal("rename failure succeeded")
	}
	assertFileBytes(t, path, previous)

	if err := writeJSONAtomic(path, make(chan int)); err == nil {
		t.Fatal("serialization failure succeeded")
	}
	assertFileBytes(t, path, previous)
}

func assertFileBytes(t *testing.T, path string, expected []byte) {
	t.Helper()
	actual, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(actual) != string(expected) {
		t.Fatalf("file changed to %q, want %q", actual, expected)
	}
}
