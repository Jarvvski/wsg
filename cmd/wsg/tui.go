package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"charm.land/bubbles/v2/textarea"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

type tickMsg struct{}

func tickCmd() tea.Cmd {
	return tea.Tick(2*time.Second, func(time.Time) tea.Msg {
		return tickMsg{}
	})
}

type viewState int

const (
	viewList viewState = iota
	viewTail
	viewInput
	viewDispatch
)

const defaultStatus = "[n]ew  [f]ollow  [s]end  [r]eview  [g]rebase  [o]pen PR  [d]ismiss  [q]uit"

type tuiWorker struct {
	name         string
	state        *WorkerState
	lastActivity string
}

type tuiModel struct {
	repo     *RepoContext
	workers  []tuiWorker
	cursor   int
	view     viewState
	status   string
	width    int
	height   int
	quitting bool

	// tail view state
	tailWorker string
	tailLines  []string
	tailOffset int64

	// input view state
	inputWorker string
	textArea    textarea.Model

	// dispatch view state
	dispatchArea textarea.Model
}

func runTUI(r *RepoContext) {
	m := newTUIModel(r)
	if m.quitting {
		fmt.Fprintln(os.Stderr, m.status)
		return
	}
	p := tea.NewProgram(m)
	if _, err := p.Run(); err != nil {
		fatal("TUI error: %v", err)
	}
}

func newTUIModel(r *RepoContext) tuiModel {
	m := tuiModel{
		repo:   r,
		status: defaultStatus,
	}
	if _, err := loadPoolConfig(r.poolConfigFile()); err != nil {
		m.quitting = true
		m.status = "No pool configured. Run 'wsg pool <N>' to create one."
		return m
	}
	m.loadWorkers()
	return m
}

func (m *tuiModel) loadWorkers() {
	cfg, err := loadPoolConfig(m.repo.poolConfigFile())
	if err != nil {
		return
	}
	workers := make([]tuiWorker, 0, len(cfg.Workers))
	for _, name := range cfg.Workers {
		checkWorkerLiveness(m.repo, name)
		ws, err := loadWorkerState(m.repo.workerStateFile(name))
		if err != nil {
			ws = newIdleWorkerState()
		}
		activity := ""
		if ws.LogFile != nil && *ws.LogFile != "" {
			activity = readLastActivity(*ws.LogFile)
		}
		workers = append(workers, tuiWorker{
			name:         name,
			state:        ws,
			lastActivity: activity,
		})
	}
	m.workers = workers
	if m.cursor >= len(m.workers) && len(m.workers) > 0 {
		m.cursor = len(m.workers) - 1
	}
}

func (m tuiModel) Init() tea.Cmd {
	return tickCmd()
}

func (m tuiModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.PasteMsg:
		switch m.view {
		case viewInput:
			var cmd tea.Cmd
			m.textArea, cmd = m.textArea.Update(msg)
			return m, cmd
		case viewDispatch:
			var cmd tea.Cmd
			m.dispatchArea, cmd = m.dispatchArea.Update(msg)
			return m, cmd
		}
	case tea.KeyPressMsg:
		switch m.view {
		case viewList:
			return m.updateList(msg)
		case viewTail:
			return m.updateTail(msg)
		case viewInput:
			return m.updateInput(msg)
		case viewDispatch:
			return m.updateDispatch(msg)
		}
	case tickMsg:
		m.loadWorkers()
		if m.view == viewTail {
			m.loadTailLines()
		}
		return m, tickCmd()
	case rebaseResultMsg:
		if msg.err != nil {
			m.status = fmt.Sprintf("Rebase failed: %v", msg.err)
		} else {
			m.status = fmt.Sprintf("Rebased %s onto trunk", displayWorker(msg.worker))
		}
	case reviewResultMsg:
		if msg.err != nil {
			m.status = fmt.Sprintf("Review failed: %v", msg.err)
		} else {
			m.status = fmt.Sprintf("Review dispatched for %s", displayWorker(msg.worker))
			m.view = viewTail
			m.tailWorker = msg.worker
			m.tailLines = nil
			m.tailOffset = 0
			m.loadTailLines()
		}
	case dispatchResultMsg:
		if msg.err != nil {
			m.status = fmt.Sprintf("Dispatch failed: %v", msg.err)
		} else if msg.orchestrated {
			m.status = fmt.Sprintf("Orchestrating %s (%d sub-issues)", msg.ticket, msg.subIssueCount)
		} else {
			m.status = fmt.Sprintf("Dispatched %s to %s", msg.ticket, displayWorker(msg.worker))
			m.view = viewTail
			m.tailWorker = msg.worker
			m.tailLines = nil
			m.tailOffset = 0
			m.loadTailLines()
		}
		m.loadWorkers()
	case sendResultMsg:
		if msg.err != nil {
			m.status = fmt.Sprintf("Send failed: %v", msg.err)
		} else {
			m.status = fmt.Sprintf("Message sent to %s", displayWorker(msg.worker))
			m.view = viewTail
			m.tailWorker = msg.worker
			m.tailLines = nil
			m.tailOffset = 0
			m.loadTailLines()
		}
	case openPRResultMsg:
		if msg.err != nil {
			m.status = fmt.Sprintf("PR: %v", msg.err)
		} else {
			m.status = fmt.Sprintf("Opened PR for %s", displayWorker(msg.worker))
		}
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
	}
	return m, nil
}

func (m tuiModel) selectedWorker() *tuiWorker {
	if m.cursor >= 0 && m.cursor < len(m.workers) {
		return &m.workers[m.cursor]
	}
	return nil
}

func (m tuiModel) updateList(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "q", "ctrl+c":
		m.quitting = true
		return m, tea.Quit
	case "j", "down":
		if m.cursor < len(m.workers)-1 {
			m.cursor++
		}
	case "k", "up":
		if m.cursor > 0 {
			m.cursor--
		}
	case "g":
		w := m.selectedWorker()
		if w == nil {
			return m, nil
		}
		if w.state.Status == "busy" {
			m.status = "Cannot rebase: worker is busy"
			return m, nil
		}
		if w.state.BranchName == nil || *w.state.BranchName == "" {
			m.status = "Cannot rebase: no branch"
			return m, nil
		}
		m.status = fmt.Sprintf("Rebasing %s...", displayWorker(w.name))
		return m, m.doRebase(w)
	case "r":
		w := m.selectedWorker()
		if w == nil {
			return m, nil
		}
		if w.state.BranchName == nil || *w.state.BranchName == "" {
			m.status = "No branch - run a dispatch first"
			return m, nil
		}
		if w.state.Status == "busy" {
			m.status = "Cannot review: worker is busy"
			return m, nil
		}
		m.status = fmt.Sprintf("Dispatching review for %s...", displayWorker(w.name))
		return m, m.doReview(w)
	case "o":
		w := m.selectedWorker()
		if w == nil {
			return m, nil
		}
		if w.state.BranchName == nil || *w.state.BranchName == "" {
			m.status = "No branch - run a dispatch first"
			return m, nil
		}
		return m, m.doOpenPR(w)
	case "s":
		w := m.selectedWorker()
		if w == nil {
			return m, nil
		}
		if w.state.Status == "busy" {
			m.status = "Worker is busy"
			return m, nil
		}
		if w.state.LogFile == nil || *w.state.LogFile == "" {
			m.status = "No session to resume"
			return m, nil
		}
		m.view = viewInput
		m.inputWorker = w.name
		ta := textarea.New()
		ta.Placeholder = "Message to " + displayWorker(w.name) + "..."
		ta.Focus()
		styleTextArea(&ta)
		ta.SetHeight(3)
		if m.width > 0 {
			ta.SetWidth(m.width - 4)
		} else {
			ta.SetWidth(76)
		}
		m.textArea = ta
		m.status = "Enter to send, Shift+Enter for newline, Esc to cancel"
		return m, ta.Focus()
	case "n":
		m.view = viewDispatch
		ta := textarea.New()
		ta.Placeholder = "AMBA-42"
		ta.Focus()
		styleTextArea(&ta)
		ta.SetHeight(1)
		if m.width > 0 {
			ta.SetWidth(m.width - 4)
		} else {
			ta.SetWidth(76)
		}
		m.dispatchArea = ta
		m.status = "Enter ticket ID, Enter to dispatch, Esc to cancel"
		return m, ta.Focus()
	case "d":
		w := m.selectedWorker()
		if w == nil {
			return m, nil
		}
		if w.state.Status == "busy" {
			m.status = "Cannot reset: worker is busy"
			return m, nil
		}
		if w.state.Status == "idle" {
			m.status = "Worker is already idle"
			return m, nil
		}
		sf := m.repo.workerStateFile(w.name)
		w.state.Reset()
		saveWorkerState(sf, w.state)
		w.lastActivity = ""
		m.status = fmt.Sprintf("Reset %s to idle", displayWorker(w.name))
		return m, nil
	case "f":
		w := m.selectedWorker()
		if w == nil {
			return m, nil
		}
		if w.state.LogFile == nil || *w.state.LogFile == "" {
			m.status = "No log file"
			return m, nil
		}
		m.view = viewTail
		m.tailWorker = w.name
		m.tailLines = nil
		m.tailOffset = 0
		m.loadTailLines()
		return m, nil
	}
	return m, nil
}

func (m tuiModel) updateTail(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "q", "esc", "ctrl+c":
		m.view = viewList
		m.status = defaultStatus
	}
	return m, nil
}

func (m tuiModel) updateInput(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.view = viewList
		m.status = defaultStatus
		return m, nil
	case "enter":
		text := strings.TrimSpace(m.textArea.Value())
		if text == "" {
			return m, nil
		}
		m.view = viewList
		m.status = fmt.Sprintf("Sending to %s...", displayWorker(m.inputWorker))
		return m, m.doSend(m.inputWorker, text)
	}

	// Forward to textarea for all other keys
	var cmd tea.Cmd
	m.textArea, cmd = m.textArea.Update(msg)
	return m, cmd
}

func (m tuiModel) updateDispatch(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.view = viewList
		m.status = defaultStatus
		return m, nil
	case "enter":
		ticket := strings.TrimSpace(m.dispatchArea.Value())
		if ticket == "" {
			return m, nil
		}
		ticket = strings.ToUpper(ticket)
		m.view = viewList
		m.status = fmt.Sprintf("Dispatching %s...", ticket)
		return m, m.doDispatch(ticket)
	}

	var cmd tea.Cmd
	m.dispatchArea, cmd = m.dispatchArea.Update(msg)
	return m, cmd
}

// ── Commands ───────────────────────────────────────────────────────

type dispatchResultMsg struct {
	ticket        string
	worker        string
	orchestrated  bool
	subIssueCount int
	err           error
}

type rebaseResultMsg struct {
	worker string
	err    error
}

type reviewResultMsg struct {
	worker string
	err    error
}

type sendResultMsg struct {
	worker string
	err    error
}

type openPRResultMsg struct {
	worker string
	err    error
}

func (m tuiModel) doRebase(w *tuiWorker) tea.Cmd {
	name := w.name
	branch := *w.state.BranchName
	wspath := m.repo.workerDir(name)
	return func() tea.Msg {
		if _, err := run(wspath, "jj", "rebase", "-b", branch, "-d", "main"); err != nil {
			return rebaseResultMsg{worker: name, err: err}
		}
		if _, err := run(wspath, "jj", "git", "push", "-b", branch); err != nil {
			run(wspath, "jj", "op", "undo")
			return rebaseResultMsg{worker: name, err: fmt.Errorf("rebase caused conflicts, reverted - use [r]eview instead")}
		}
		return rebaseResultMsg{worker: name}
	}
}

func (m tuiModel) doOpenPR(w *tuiWorker) tea.Cmd {
	name := w.name
	branch := *w.state.BranchName
	repo := ghRepo(m.repo)
	return func() tea.Msg {
		if repo == "" {
			return openPRResultMsg{worker: name, err: fmt.Errorf("cannot detect GitHub repo")}
		}
		_, err := run("", "gh", "-R", repo, "pr", "view", branch, "--web")
		if err != nil {
			return openPRResultMsg{worker: name, err: fmt.Errorf("no PR for branch %s", branch)}
		}
		return openPRResultMsg{worker: name}
	}
}

func (m tuiModel) doReview(w *tuiWorker) tea.Cmd {
	name := w.name
	repo := m.repo
	return func() tea.Msg {
		sf := repo.workerStateFile(name)
		ws, err := loadWorkerState(sf)
		if err != nil {
			return reviewResultMsg{worker: name, err: err}
		}

		if ws.LogFile == nil || *ws.LogFile == "" {
			return reviewResultMsg{worker: name, err: fmt.Errorf("no log file")}
		}

		sessionID, err := extractSessionID(*ws.LogFile)
		if err != nil {
			return reviewResultMsg{worker: name, err: err}
		}

		ghRepoName := ghRepo(repo)
		if ghRepoName == "" {
			return reviewResultMsg{worker: name, err: fmt.Errorf("cannot detect GitHub repo")}
		}

		prJSON, err := run("", "gh", "-R", ghRepoName, "pr", "list", "--head", *ws.BranchName, "--json", "number,url,headRefName,mergeable", "--limit", "1")
		if err != nil {
			return reviewResultMsg{worker: name, err: err}
		}
		if prJSON == "" || prJSON == "[]" {
			return reviewResultMsg{worker: name, err: fmt.Errorf("no PR for branch %s", *ws.BranchName)}
		}

		var prs []struct {
			Number      int    `json:"number"`
			HeadRefName string `json:"headRefName"`
			Mergeable   string `json:"mergeable"`
		}
		if err := json.Unmarshal([]byte(prJSON), &prs); err != nil || len(prs) == 0 {
			return reviewResultMsg{worker: name, err: fmt.Errorf("no PR for branch %s", *ws.BranchName)}
		}
		pr := prs[0]

		hasConflicts := strings.EqualFold(pr.Mergeable, "CONFLICTING")
		failingChecks := fetchFailingChecks(ghRepoName, pr.Number)
		prompt := buildReviewPrompt(ghRepoName, pr.Number, "", pr.HeadRefName, failingChecks, hasConflicts)

		wspath := repo.workerDir(name)
		poolDir := repo.poolDir()
		logFile := fmt.Sprintf("%s/%s.log", poolDir, name)

		ws.MarkResumed(logFile)
		saveWorkerState(sf, ws)

		inv := claudeInvocation{
			Budget:    "5",
			SessionID: sessionID,
			Prompt:    prompt,
		}
		fullArgs := append([]string{"claude"}, inv.Args()...)
		_, err = runClaudeBG(wspath, logFile, sf, ws, fullArgs)
		if err != nil {
			return reviewResultMsg{worker: name, err: err}
		}

		return reviewResultMsg{worker: name}
	}
}

func (m tuiModel) doSend(workerName, prompt string) tea.Cmd {
	repo := m.repo
	return func() tea.Msg {
		sf := repo.workerStateFile(workerName)
		ws, err := loadWorkerState(sf)
		if err != nil {
			return sendResultMsg{worker: workerName, err: err}
		}

		wspath := repo.workerDir(workerName)
		poolDir := repo.poolDir()
		logFile := fmt.Sprintf("%s/%s.log", poolDir, workerName)

		sessionID := ""
		if ws.LogFile != nil && *ws.LogFile != "" {
			if sid, err := extractSessionID(*ws.LogFile); err == nil {
				sessionID = sid
			}
		}

		ws.MarkResumed(logFile)
		saveWorkerState(sf, ws)

		inv := claudeInvocation{
			Budget:    "5",
			SessionID: sessionID,
			Prompt:    prompt,
		}
		if sessionID == "" {
			inv.SystemPrompt = sendSystemPrompt(ghRepo(repo))
		}
		fullArgs := append([]string{"claude"}, inv.Args()...)
		_, err = runClaudeBG(wspath, logFile, sf, ws, fullArgs)
		if err != nil {
			return sendResultMsg{worker: workerName, err: err}
		}

		return sendResultMsg{worker: workerName}
	}
}

func (m tuiModel) doDispatch(ticket string) tea.Cmd {
	repo := m.repo
	return func() tea.Msg {
		opts := &DispatchOpts{
			TicketID: ticket,
			Model:    "opus",
			Budget:   "20",
		}

		// Check for existing dispatch group (resume)
		dgFile := dispatchGroupFile(repo, ticket)
		if dg := syncExistingGroup(repo, dgFile); dg != nil {
			if !isGroupTerminal(dg) {
				spawnOrchestrator(repo, ticket, opts)
			}
			return dispatchResultMsg{
				ticket:        ticket,
				orchestrated:  true,
				subIssueCount: len(dg.SubIssues),
			}
		}

		// Try to build dependency graph (detects parent issues)
		dg, err := buildDependencyGraph(repo, ticket, opts)
		if err == nil && dg != nil {
			spawnOrchestrator(repo, ticket, opts)
			return dispatchResultMsg{
				ticket:        ticket,
				orchestrated:  true,
				subIssueCount: len(dg.SubIssues),
			}
		}

		// Single ticket dispatch - find idle worker
		worker, err := findIdleWorker(repo)
		if err != nil {
			// Auto-resize pool
			cfg, cfgErr := loadPoolConfig(repo.poolConfigFile())
			if cfgErr != nil {
				return dispatchResultMsg{ticket: ticket, err: fmt.Errorf("no pool")}
			}
			newSize := cfg.Size + 1
			cmdPoolResize([]string{fmt.Sprintf("%d", newSize)})
			worker, err = findIdleWorker(repo)
			if err != nil {
				return dispatchResultMsg{ticket: ticket, err: fmt.Errorf("no idle workers after resize")}
			}
		}

		launchWorker(repo, worker, opts, nil)
		return dispatchResultMsg{ticket: ticket, worker: worker}
	}
}

// ── Tail helpers ───────────────────────────────────────────────────

func (m *tuiModel) loadTailLines() {
	for _, w := range m.workers {
		if w.name == m.tailWorker && w.state.LogFile != nil {
			lines, newOffset := readLogTail(*w.state.LogFile, m.tailOffset)
			m.tailLines = append(m.tailLines, lines...)
			m.tailOffset = newOffset
			maxLines := 200
			if len(m.tailLines) > maxLines {
				m.tailLines = m.tailLines[len(m.tailLines)-maxLines:]
			}
			return
		}
	}
}

func readLogTail(path string, offset int64) ([]string, int64) {
	f, err := os.Open(path)
	if err != nil {
		return nil, offset
	}
	defer f.Close()

	if offset > 0 {
		f.Seek(offset, io.SeekStart)
	}

	var lines []string
	state := &logState{seen: make(map[string]bool)}
	reader := bufio.NewReader(f)
	for {
		line, err := reader.ReadString('\n')
		if line != "" {
			line = strings.TrimSpace(line)
			if line != "" {
				formatted := formatEventToString(line, state)
				if formatted != "" {
					lines = append(lines, formatted)
				}
			}
		}
		if err != nil {
			break
		}
	}

	newOffset, _ := f.Seek(0, io.SeekCurrent)
	return lines, newOffset
}

func formatEventToString(line string, state *logState) string {
	var ev streamEvent
	if err := json.Unmarshal([]byte(line), &ev); err != nil {
		return line
	}

	switch ev.Type {
	case "system":
		if ev.Subtype == "init" {
			return "--- session started ---"
		}
	case "assistant":
		if ev.Message == nil {
			return ""
		}
		var parts []string
		for _, c := range ev.Message.Content {
			switch c.Type {
			case "text":
				if c.Text != "" && !state.seen[c.Text] {
					state.seen[c.Text] = true
					parts = append(parts, c.Text)
				}
			case "tool_use":
				input := summarizeInput(c.Input)
				parts = append(parts, c.Name+input)
			}
		}
		return strings.Join(parts, " ")
	case "result":
		dur := fmt.Sprintf("%.0fs", float64(ev.DurationMs)/1000)
		cost := fmt.Sprintf("$%.2f", ev.TotalCost)
		status := "done"
		if ev.IsError {
			status = "error"
		}
		return fmt.Sprintf("--- %s in %s, %d turns, %s", status, dur, ev.NumTurns, cost)
	}
	return ""
}

func readLastActivity(logFile string) string {
	f, err := os.Open(logFile)
	if err != nil {
		return ""
	}
	defer f.Close()

	fi, err := f.Stat()
	if err != nil || fi.Size() == 0 {
		return ""
	}

	// Read last 64KB - result events can be large (contain full output text)
	readSize := int64(65536)
	if fi.Size() < readSize {
		readSize = fi.Size()
	}
	f.Seek(-readSize, io.SeekEnd)

	data := make([]byte, readSize)
	n, _ := f.Read(data)
	data = data[:n]

	// Find last complete line with a tool_use
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		var ev streamEvent
		if err := json.Unmarshal([]byte(lines[i]), &ev); err != nil {
			continue
		}
		if ev.Type == "result" {
			dur := fmt.Sprintf("%.0fs", float64(ev.DurationMs)/1000)
			cost := fmt.Sprintf("$%.2f", ev.TotalCost)
			if ev.IsError {
				return fmt.Sprintf("error %s %s", dur, cost)
			}
			return fmt.Sprintf("done %s %s", dur, cost)
		}
		if ev.Type == "assistant" && ev.Message != nil {
			for _, c := range ev.Message.Content {
				if c.Type == "tool_use" {
					input := summarizeInputPlain(c.Input)
					result := c.Name + input
					if len(result) > 50 {
						result = result[:47] + "..."
					}
					return result
				}
			}
		}
	}
	return ""
}

// ── View ───────────────────────────────────────────────────────────

func (m tuiModel) View() tea.View {
	if m.quitting {
		return tea.NewView("")
	}
	var v tea.View
	v.AltScreen = true
	switch m.view {
	case viewTail:
		v.SetContent(m.renderTail())
	case viewInput:
		v.SetContent(m.renderInput())
	case viewDispatch:
		v.SetContent(m.renderDispatch())
	default:
		v.SetContent(m.renderList())
	}
	return v
}

func (m tuiModel) renderList() string {
	var b strings.Builder

	b.WriteString(fmt.Sprintf("  %-10s %-10s %-14s %-12s %s\n",
		"WORKER", "STATUS", "TICKET", "ELAPSED", "ACTIVITY"))
	b.WriteString(fmt.Sprintf("  %-10s %-10s %-14s %-12s %s\n",
		"------", "------", "------", "-------", "--------"))

	for i, w := range m.workers {
		prefix := "  "
		if i == m.cursor {
			prefix = "> "
		}

		display := displayWorker(w.name)
		status := w.state.Status

		ticket := "-"
		if w.state.Ticket != nil {
			ticket = *w.state.Ticket
		}

		elapsed := "-"
		if w.state.StartedAt != nil && *w.state.StartedAt != "" {
			elapsed = elapsedDisplay(*w.state.StartedAt, w.state.CompletedAt)
		}

		activity := "-"
		if w.lastActivity != "" {
			activity = w.lastActivity
		}

		paddedStatus := fmt.Sprintf("%-10s", status)
		if isTTY {
			switch status {
			case "idle":
				paddedStatus = colorize(paddedStatus, colorDim)
			case "busy":
				paddedStatus = colorize(paddedStatus, colorYellow)
			case "done":
				paddedStatus = colorize(paddedStatus, colorGreen)
			case "failed":
				paddedStatus = colorize(paddedStatus, colorRed)
			}
		}

		b.WriteString(fmt.Sprintf("%s%-10s %s %-14s %-12s %s\n",
			prefix, display, paddedStatus, ticket, elapsed, activity))
	}

	b.WriteString("\n")
	b.WriteString(m.status)

	return b.String()
}

func (m tuiModel) renderTail() string {
	var b strings.Builder

	header := fmt.Sprintf("Tailing %s  [q/Esc to return]", displayWorker(m.tailWorker))
	b.WriteString(header)
	b.WriteString("\n")
	b.WriteString(strings.Repeat("-", len(header)))
	b.WriteString("\n\n")

	visibleLines := 40
	if m.height > 0 {
		visibleLines = m.height - 5
	}

	start := 0
	if len(m.tailLines) > visibleLines {
		start = len(m.tailLines) - visibleLines
	}

	for _, line := range m.tailLines[start:] {
		b.WriteString(line)
		b.WriteString("\n")
	}

	return b.String()
}

func (m tuiModel) renderInput() string {
	var b strings.Builder

	b.WriteString(fmt.Sprintf("Send message to %s:\n\n", displayWorker(m.inputWorker)))
	b.WriteString(m.textArea.View())
	b.WriteString("\n\n")
	b.WriteString(m.status)

	return b.String()
}

func styleTextArea(ta *textarea.Model) {
	s := ta.Styles()
	white := lipgloss.Color("15")
	dim := lipgloss.Color("245")
	s.Focused.Text = lipgloss.NewStyle().Foreground(white)
	s.Focused.CursorLine = lipgloss.NewStyle()
	s.Focused.CursorLineNumber = lipgloss.NewStyle().Foreground(dim)
	s.Focused.LineNumber = lipgloss.NewStyle().Foreground(dim)
	s.Focused.Placeholder = lipgloss.NewStyle().Foreground(dim)
	s.Focused.Prompt = lipgloss.NewStyle().Foreground(dim)
	ta.SetStyles(s)
}

func (m tuiModel) renderDispatch() string {
	var b strings.Builder

	b.WriteString("Dispatch ticket:\n\n")
	b.WriteString(m.dispatchArea.View())
	b.WriteString("\n\n")
	b.WriteString(m.status)

	return b.String()
}
