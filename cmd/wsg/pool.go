package main

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

type PoolConfig struct {
	Size       int      `json:"size"`
	GHRepo     string   `json:"gh_repo"`
	Workers    []string `json:"workers"`
	CreatedAt  string   `json:"created_at"`
	Foreground *bool    `json:"foreground,omitempty"`
}

type WorkerState struct {
	Status      string  `json:"status"`
	Ticket      *string `json:"ticket"`
	PID         *int    `json:"pid"`
	StartedAt   *string `json:"started_at"`
	CompletedAt *string `json:"completed_at"`
	LogFile     *string `json:"log_file"`
	BranchName  *string `json:"branch_name"`
	ExitCode    *int    `json:"exit_code"`
	Error       *string `json:"error"`
}

func resolveForeground(r *RepoContext, flag *bool) bool {
	if flag != nil {
		return *flag
	}
	if cfg, err := loadPoolConfig(r.poolConfigFile()); err == nil && cfg.Foreground != nil {
		return *cfg.Foreground
	}
	return false
}

func newIdleWorkerState() *WorkerState {
	return &WorkerState{Status: "idle"}
}

func (ws *WorkerState) MarkDispatched(ticket, logFile, branchName string) {
	now := nowUTC()
	ws.Status = "busy"
	ws.Ticket = &ticket
	ws.StartedAt = &now
	ws.LogFile = &logFile
	ws.BranchName = &branchName
	ws.CompletedAt = nil
	ws.ExitCode = nil
	ws.Error = nil
	ws.PID = nil
}

func (ws *WorkerState) MarkDone(exitCode int) {
	now := nowUTC()
	ws.Status = "done"
	ws.CompletedAt = &now
	ws.ExitCode = &exitCode
}

func (ws *WorkerState) MarkFailed(exitCode int, errMsg string) {
	now := nowUTC()
	ws.Status = "failed"
	ws.CompletedAt = &now
	ws.ExitCode = &exitCode
	ws.Error = &errMsg
}

func (ws *WorkerState) SetPID(pid int) {
	ws.PID = &pid
}

func (ws *WorkerState) Reset() {
	*ws = WorkerState{Status: "idle"}
}

func (ws *WorkerState) MarkResumed(logFile string) {
	now := nowUTC()
	ws.Status = "busy"
	ws.StartedAt = &now
	ws.LogFile = &logFile
	ws.CompletedAt = nil
	ws.ExitCode = nil
	ws.Error = nil
}

// ── WorkerHandle ──────────────────────────────────────────────────

type WorkerHandle struct {
	path  string
	state *WorkerState
}

func OpenWorker(path string) (*WorkerHandle, error) {
	ws, err := loadWorkerState(path)
	if err != nil {
		return nil, err
	}
	return &WorkerHandle{path: path, state: ws}, nil
}

func CreateIdleWorker(path string) (*WorkerHandle, error) {
	ws := newIdleWorkerState()
	h := &WorkerHandle{path: path, state: ws}
	if err := h.save(); err != nil {
		return nil, err
	}
	return h, nil
}

func (h *WorkerHandle) State() *WorkerState {
	return h.state
}

func (h *WorkerHandle) Dispatch(ticket, logFile, branchName string) error {
	h.state.MarkDispatched(ticket, logFile, branchName)
	return h.save()
}

func (h *WorkerHandle) Done(exitCode int) error {
	h.state.MarkDone(exitCode)
	return h.save()
}

func (h *WorkerHandle) Failed(exitCode int, errMsg string) error {
	h.state.MarkFailed(exitCode, errMsg)
	return h.save()
}

func (h *WorkerHandle) Resume(logFile string) error {
	h.state.MarkResumed(logFile)
	return h.save()
}

func (h *WorkerHandle) SetPID(pid int) error {
	h.state.SetPID(pid)
	return h.save()
}

func (h *WorkerHandle) Reset() error {
	h.state.Reset()
	return h.save()
}

func (h *WorkerHandle) save() error {
	return saveWorkerState(h.path, h.state)
}

func loadPoolConfig(path string) (*PoolConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var cfg PoolConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}

func savePoolConfig(path string, cfg *PoolConfig) error {
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func loadWorkerState(path string) (*WorkerState, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var ws WorkerState
	if err := json.Unmarshal(data, &ws); err != nil {
		return nil, err
	}
	return &ws, nil
}

func saveWorkerState(path string, ws *WorkerState) error {
	data, err := json.MarshalIndent(ws, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func (h *WorkerHandle) CheckLiveness(r *RepoContext, worker string) {
	ws := h.state
	if ws.Status != "busy" || ws.PID == nil {
		return
	}
	if processAlive(*ws.PID) {
		return
	}
	if ws.LogFile != nil {
		if result := readLogResult(*ws.LogFile); result != nil {
			if result.Status == "done" {
				ec := 0
				if result.ExitCode != nil {
					ec = *result.ExitCode
				}
				h.Done(ec)
				resolveWorkerBranch(r, worker, ws)
				h.save()
			} else {
				ec := 1
				if result.ExitCode != nil {
					ec = *result.ExitCode
				}
				errMsg := ""
				if result.Error != nil {
					errMsg = *result.Error
				}
				h.Failed(ec, errMsg)
			}
			return
		}
	}
	h.Failed(1, "Process exited unexpectedly")
}

func (h *WorkerHandle) RunFG(wspath, logFile string, claudeArgs []string) {
	exitCode, err := startForeground(wspath, logFile, claudeArgs[0], claudeArgs[1:]...)
	if err != nil {
		h.Failed(1, err.Error())
	} else if exitCode == 0 {
		h.Done(exitCode)
	} else {
		h.Failed(exitCode, "")
	}
}

func (h *WorkerHandle) RunBG(wspath, logFile string, claudeArgs []string) (int, error) {
	pid, err := startBackground(wspath, logFile, claudeArgs[0], claudeArgs[1:]...)
	if err != nil {
		h.Failed(1, err.Error())
		return 0, err
	}
	h.SetPID(pid)

	path := h.path
	go func() {
		waitForProcess(pid)
		h, err := OpenWorker(path)
		if err != nil {
			return
		}
		if h.State().Status == "busy" {
			h.Done(0)
		}
	}()

	return pid, nil
}

func resolveWorkerBranch(r *RepoContext, worker string, ws *WorkerState) {
	if ws.BranchName == nil || *ws.BranchName == "" {
		return
	}
	prefix := "adam/" + *ws.BranchName + "-"
	wspath := r.workerDir(worker)
	output, err := run(wspath, "jj", "bookmark", "list", "--template", `name ++ "\n"`)
	if err != nil {
		return
	}
	for _, line := range strings.Split(output, "\n") {
		if strings.HasPrefix(line, prefix) {
			ws.BranchName = &line
			return
		}
	}
}

func countIdleWorkers(r *RepoContext) int {
	cfg, err := loadPoolConfig(r.poolConfigFile())
	if err != nil {
		return 0
	}
	count := 0
	for _, worker := range cfg.Workers {
		ws, err := loadWorkerState(r.workerStateFile(worker))
		if err != nil {
			continue
		}
		if ws.Status == "idle" {
			count++
		}
	}
	return count
}

func findIdleWorker(r *RepoContext) (string, error) {
	cfg, err := loadPoolConfig(r.poolConfigFile())
	if err != nil {
		return "", err
	}
	for _, worker := range cfg.Workers {
		ws, err := loadWorkerState(r.workerStateFile(worker))
		if err != nil {
			continue
		}
		if ws.Status == "idle" {
			return worker, nil
		}
	}
	return "", fmt.Errorf("no idle workers")
}

func ghRepo(r *RepoContext) string {
	configFile := r.poolConfigFile()
	if cfg, err := loadPoolConfig(configFile); err == nil && cfg.GHRepo != "" {
		return strings.TrimSuffix(cfg.GHRepo, ".git")
	}
	output, err := run(r.Root, "jj", "git", "remote", "list")
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(output, "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 2 && fields[0] == "origin" {
			url := fields[1]
			url = strings.TrimSuffix(url, ".git")
			if idx := strings.LastIndex(url, ":"); idx != -1 {
				url = url[idx+1:]
			} else if idx := strings.LastIndex(url, "/"); idx != -1 {
				parts := strings.Split(url, "/")
				if len(parts) >= 2 {
					url = parts[len(parts)-2] + "/" + parts[len(parts)-1]
				}
			}
			return url
		}
	}
	return ""
}

func nowUTC() string {
	return time.Now().UTC().Format("2006-01-02T15:04:05Z")
}

func elapsedDisplay(startedAt string, completedAt *string) string {
	start, err := time.Parse("2006-01-02T15:04:05Z", startedAt)
	if err != nil {
		return "-"
	}
	var end time.Time
	if completedAt != nil && *completedAt != "" {
		end, err = time.Parse("2006-01-02T15:04:05Z", *completedAt)
		if err != nil {
			end = time.Now().UTC()
		}
	} else {
		end = time.Now().UTC()
	}
	diff := end.Sub(start)
	mins := int(diff.Minutes())
	secs := int(diff.Seconds()) % 60
	return fmt.Sprintf("%dm %ds", mins, secs)
}

// ── Pool commands ──────────────────────────────────────────────────

func cmdPool(args []string) {
	subcmd := "list"
	if len(args) > 0 {
		subcmd = args[0]
		args = args[1:]
	}
	switch subcmd {
	case "create", "c", "resize", "r":
		cmdPoolResize(args)
	case "list", "ls", "status":
		cmdPoolList()
	case "rm", "remove":
		cmdPoolRm(args)
	case "destroy":
		cmdPoolDestroy()
	case "reset":
		cmdPoolReset(args)
	case "help":
		cmdHelp()
	default:
		if _, err := strconv.Atoi(subcmd); err == nil {
			cmdPoolResize(append([]string{subcmd}, args...))
		} else {
			fatal("Unknown pool command: %s", subcmd)
		}
	}
}

func cmdPoolResize(args []string) {
	if len(args) == 0 {
		fatal("Usage: wsg pool resize <N>")
	}

	var newSizeStr string
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--size", "-s":
			if i+1 < len(args) {
				newSizeStr = args[i+1]
				i++
			}
		default:
			if !strings.HasPrefix(args[i], "-") {
				newSizeStr = args[i]
			}
		}
	}
	if newSizeStr == "" {
		fatal("Usage: wsg pool resize <N>")
	}

	newSize, err := strconv.Atoi(newSizeStr)
	if err != nil {
		fatal("Invalid pool size: %s", newSizeStr)
	}

	r, err := newRepoContext()
	if err != nil {
		fatal("Not in a jj repo")
	}

	configFile := r.poolConfigFile()
	poolDir := r.poolDir()

	var cfg *PoolConfig
	cfg, err = loadPoolConfig(configFile)
	if err != nil {
		repo := ghRepo(r)
		os.MkdirAll(poolDir, 0755)
		cfg = &PoolConfig{
			Size:      0,
			GHRepo:    repo,
			Workers:   []string{},
			CreatedAt: nowUTC(),
		}
		savePoolConfig(configFile, cfg)
	}

	oldSize := cfg.Size
	if newSize == oldSize {
		info("Pool is already size %d", oldSize)
		return
	}

	if newSize > oldSize {
		var wg sync.WaitGroup
		for i := 0; i < newSize-oldSize; i++ {
			name := generateWorkerName()
			wspath := r.workerDir(name)
			wsName := name
			jjArgs := []string{"workspace", "add", wspath}
			if _, err := run(r.Root, "jj", jjArgs...); err != nil {
				fatal("Failed to create %s: %v", name, err)
			}
			cfg.Workers = append(cfg.Workers, name)
			cacheAddEntry(r.cacheFile(), wsName, wspath)
			wg.Add(1)
			go func(name, wspath string) {
				defer wg.Done()
				copyEnvFile(r.Root, wspath)
				copySynapseClone(r.Root, wspath)
				CreateIdleWorker(filepath.Join(poolDir, name+".json"))
			}(name, wspath)
			info("  Created %s", name)
		}
		wg.Wait()

		cfg.Size = newSize
		savePoolConfig(configFile, cfg)
		info("Pool expanded from %d to %d", oldSize, newSize)
		return
	}

	// Shrinking
	nonIdle := 0
	var idleNames []string
	for i := len(cfg.Workers) - 1; i >= newSize; i-- {
		name := cfg.Workers[i]
		sf := filepath.Join(poolDir, name+".json")
		if _, err := os.Stat(sf); os.IsNotExist(err) {
			idleNames = append(idleNames, name)
			continue
		}
		h, err := OpenWorker(sf)
		if err != nil {
			idleNames = append(idleNames, name)
			continue
		}
		h.CheckLiveness(r, name)
		if h.State().Status == "idle" || h.State().Status == "done" || h.State().Status == "failed" {
			idleNames = append(idleNames, name)
		} else {
			nonIdle++
		}
	}

	toRemove := oldSize - newSize
	if len(idleNames) < toRemove {
		minSize := oldSize - len(idleNames)
		fatal("Cannot shrink to %d: %d worker(s) are busy.\nMinimum safe size is %d. Use 'wsg pool list' to see status.", newSize, nonIdle, minSize)
	}

	removed := make(map[string]bool)
	for _, name := range idleNames {
		cmdRm([]string{"--force", name})
		os.Remove(filepath.Join(poolDir, name+".json"))
		os.Remove(filepath.Join(poolDir, name+".log"))
		removed[name] = true
		info("  Removed %s", name)
	}

	remaining := make([]string, 0, newSize)
	for _, w := range cfg.Workers {
		if !removed[w] {
			remaining = append(remaining, w)
		}
	}
	cfg.Size = newSize
	cfg.Workers = remaining
	savePoolConfig(configFile, cfg)
	info("Pool shrunk from %d to %d", oldSize, newSize)
}

func generateWorkerName() string {
	b := make([]byte, 3)
	rand.Read(b)
	return fmt.Sprintf("worker-%x", b)
}

func cmdPoolList() {
	r, err := newRepoContext()
	if err != nil {
		fatal("Not in a jj repo")
	}

	configFile := r.poolConfigFile()
	cfg, err := loadPoolConfig(configFile)
	if err != nil {
		fatal("No pool. Run: wsg pool create --size N")
	}

	idle, busy, doneCount, failed := 0, 0, 0, 0

	fmt.Printf("%-10s %-10s %-14s %s\n", "WORKER", "STATUS", "TICKET", "ELAPSED")
	fmt.Printf("%-10s %-10s %-14s %s\n", "------", "------", "------", "-------")

	for _, worker := range cfg.Workers {
		sf := r.workerStateFile(worker)
		h, err := OpenWorker(sf)
		if err != nil {
			continue
		}
		h.CheckLiveness(r, worker)
		ws := h.State()

		ticket := "-"
		if ws.Ticket != nil {
			ticket = *ws.Ticket
		}

		elapsed := "-"
		if ws.StartedAt != nil && *ws.StartedAt != "" {
			elapsed = elapsedDisplay(*ws.StartedAt, ws.CompletedAt)
		}

		paddedStatus := fmt.Sprintf("%-10s", ws.Status)
		switch ws.Status {
		case "idle":
			paddedStatus = colorize(paddedStatus, colorDim)
		case "busy":
			paddedStatus = colorize(paddedStatus, colorYellow)
		case "done":
			paddedStatus = colorize(paddedStatus, colorGreen)
		case "failed":
			paddedStatus = colorize(paddedStatus, colorRed)
		}

		fmt.Printf("%-10s %s %-14s %s\n", displayWorker(worker), paddedStatus, ticket, elapsed)

		switch ws.Status {
		case "idle":
			idle++
		case "busy":
			busy++
		case "done":
			doneCount++
		case "failed":
			failed++
		}
	}

	fmt.Println()
	fmt.Printf("Pool: %d idle, %d busy, %d done, %d failed (%d total)\n",
		idle, busy, doneCount, failed, cfg.Size)
}

func cmdPoolDestroy() {
	r, err := newRepoContext()
	if err != nil {
		fatal("Not in a jj repo")
	}

	configFile := r.poolConfigFile()
	cfg, err := loadPoolConfig(configFile)
	if err != nil {
		info("No pool to destroy")
		return
	}

	var wg sync.WaitGroup
	for _, worker := range cfg.Workers {
		wg.Add(1)
		go func(worker string) {
			defer wg.Done()
			sf := r.workerStateFile(worker)
			if ws, err := loadWorkerState(sf); err == nil {
				if ws.PID != nil && processAlive(*ws.PID) {
					killProcess(*ws.PID)
				}
			}
			cmdRm([]string{"--force", worker})
		}(worker)
	}
	wg.Wait()

	os.RemoveAll(r.poolDir())
	os.Remove(configFile)
	info("Pool destroyed")
}

func cmdPoolRm(args []string) {
	if len(args) == 0 {
		fatal("Usage: wsg pool rm <worker>")
	}
	worker := resolveWorker(args[0])

	r, err := newRepoContext()
	if err != nil {
		fatal("Not in a jj repo")
	}

	configFile := r.poolConfigFile()
	cfg, err := loadPoolConfig(configFile)
	if err != nil {
		fatal("No pool")
	}

	poolDir := r.poolDir()
	sf := filepath.Join(poolDir, worker+".json")
	if h, err := OpenWorker(sf); err == nil {
		h.CheckLiveness(r, worker)
		if h.State().Status == "busy" {
			fatal("Worker %s is busy. Reset it first: wsg pool reset %s", worker, worker)
		}
	}

	cmdRm([]string{"--force", worker})
	os.Remove(filepath.Join(poolDir, worker+".json"))
	os.Remove(filepath.Join(poolDir, worker+".log"))

	remaining := make([]string, 0, len(cfg.Workers)-1)
	for _, w := range cfg.Workers {
		if w != worker {
			remaining = append(remaining, w)
		}
	}
	cfg.Size = len(remaining)
	cfg.Workers = remaining
	savePoolConfig(configFile, cfg)
	info("Removed %s (pool size: %d)", worker, cfg.Size)
}

func cmdPoolReset(args []string) {
	if len(args) == 0 {
		fatal("Usage: wsg pool reset <worker>")
	}
	worker := resolveWorker(args[0])

	r, err := newRepoContext()
	if err != nil {
		fatal("Not in a jj repo")
	}

	sf := r.workerStateFile(worker)
	h, err := OpenWorker(sf)
	if err != nil {
		fatal("Worker %s not found", worker)
	}

	if h.State().PID != nil && processAlive(*h.State().PID) {
		killProcess(*h.State().PID)
	}

	h.Reset()
	info("Reset %s to idle", worker)

	wspath := r.workerDir(worker)
	if fi, err := os.Stat(wspath); err == nil && fi.IsDir() {
		startBackground(wspath, os.DevNull, "sh", "-c",
			"jj restore 2>/dev/null; jj new main 2>/dev/null")
	}
}
