package main

import (
	"fmt"
	"os"
	"strings"
	"time"

	"charm.land/bubbles/v2/textarea"
	"charm.land/bubbles/v2/viewport"
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

const defaultStatus = "[n]ew  [N]all  [f]ollow  [s]end  [r]eview  [g]rebase  [o]pen PR  [d]ismiss  [q]uit"

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
	status       string
	statusSetAt  time.Time
	prevStatus   string
	width        int
	height   int
	quitting bool

	// tail view state
	tailWorker    string
	tailLines     []string
	tailOffset    int64
	tailViewport  viewport.Model
	tailFollowing bool

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
		h, err := OpenWorker(m.repo.workerStateFile(name))
		if err != nil {
			h, _ = CreateIdleWorker(m.repo.workerStateFile(name))
		}
		h.CheckLiveness(m.repo, name)
		ws := h.State()
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
		if m.view == viewList && m.status != defaultStatus {
			if m.status != m.prevStatus {
				m.statusSetAt = time.Now()
				m.prevStatus = m.status
			} else if time.Since(m.statusSetAt) >= 3*time.Second {
				m.status = defaultStatus
				m.statusSetAt = time.Time{}
				m.prevStatus = ""
			}
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
		}
	case batchDispatchResultMsg:
		if msg.err != nil {
			m.status = fmt.Sprintf("Batch dispatch failed: %v", msg.err)
		} else {
			m.status = fmt.Sprintf("Dispatched %d ticket(s): %s", msg.dispatched, strings.Join(msg.tickets, ", "))
		}
		m.loadWorkers()
	case fetchAllResultMsg:
		if msg.err != nil {
			m.status = fmt.Sprintf("Fetch failed: %v", msg.err)
		} else if len(msg.tickets) == 0 {
			m.status = "No ready-for-agent tickets found"
		} else {
			m.status = fmt.Sprintf("Dispatching %d ticket(s)...", len(msg.tickets))
			m.loadWorkers()
			return m, m.doDispatchBatch(msg.tickets)
		}
	case dispatchResultMsg:
		if msg.err != nil {
			m.status = fmt.Sprintf("Dispatch failed: %v", msg.err)
		} else if msg.orchestrated {
			m.status = fmt.Sprintf("Orchestrating %s (%d sub-issues)", msg.ticket, msg.subIssueCount)
		} else if msg.backgrounded {
			m.status = fmt.Sprintf("Dispatching %s in background", msg.ticket)
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
		m.resizeTailViewport()
	}
	return m, nil
}

func (m *tuiModel) tailViewportSize() (int, int) {
	w := m.width
	if w <= 0 {
		w = 80
	}
	h := m.height - tailHeaderHeight
	if h < 3 {
		h = 3
	}
	return w, h
}

func (m *tuiModel) resizeTailViewport() {
	w, h := m.tailViewportSize()
	m.tailViewport.SetWidth(w)
	m.tailViewport.SetHeight(h)
	if m.tailFollowing {
		m.tailViewport.GotoBottom()
	}
}

const tailHeaderHeight = 4

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
		ta.Placeholder = "AMBA-42 AMBA-43 ..."
		ta.Focus()
		styleTextArea(&ta)
		ta.SetHeight(1)
		if m.width > 0 {
			ta.SetWidth(m.width - 4)
		} else {
			ta.SetWidth(76)
		}
		m.dispatchArea = ta
		m.status = "Ticket ID(s) separated by spaces, Enter to dispatch, Esc to cancel"
		return m, ta.Focus()
	case "N":
		m.status = "Fetching ready-for-agent tickets..."
		return m, m.doFetchAll()
	case "d":
		w := m.selectedWorker()
		if w == nil {
			return m, nil
		}
		if w.state.Status == "busy" {
			m.status = "Cannot dismiss: worker is busy"
			return m, nil
		}
		if w.state.Status == "idle" {
			name := w.name
			size, err := removePoolWorker(m.repo, name)
			if err != nil {
				m.status = fmt.Sprintf("Dismiss failed: %v", err)
				return m, nil
			}
			m.loadWorkers()
			m.status = fmt.Sprintf("Dismissed %s (pool size: %d)", displayWorker(name), size)
			return m, nil
		}
		sf := m.repo.workerStateFile(w.name)
		if h, err := OpenWorker(sf); err == nil {
			h.Reset()
			w.state = h.State()
		}
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
		m.tailFollowing = true
		vpW, vpH := m.tailViewportSize()
		vp := viewport.New(viewport.WithWidth(vpW), viewport.WithHeight(vpH))
		vp.SoftWrap = true
		m.tailViewport = vp
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
		return m, nil
	case "G", "end":
		m.tailViewport.GotoBottom()
		m.tailFollowing = true
		return m, nil
	case "g", "home":
		m.tailViewport.GotoTop()
		m.tailFollowing = false
		return m, nil
	}

	var cmd tea.Cmd
	m.tailViewport, cmd = m.tailViewport.Update(msg)
	m.tailFollowing = m.tailViewport.AtBottom()
	return m, cmd
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
		raw := strings.TrimSpace(m.dispatchArea.Value())
		if raw == "" {
			return m, nil
		}
		tickets := splitTickets(raw)
		if len(tickets) == 0 {
			return m, nil
		}
		m.view = viewList
		if len(tickets) == 1 {
			m.status = fmt.Sprintf("Dispatching %s...", tickets[0])
			return m, m.doDispatch(tickets[0])
		}
		m.status = fmt.Sprintf("Dispatching %d tickets...", len(tickets))
		return m, m.doDispatchBatch(tickets)
	}

	var cmd tea.Cmd
	m.dispatchArea, cmd = m.dispatchArea.Update(msg)
	return m, cmd
}

func splitTickets(raw string) []string {
	raw = strings.ToUpper(raw)
	raw = strings.ReplaceAll(raw, ",", " ")
	fields := strings.Fields(raw)
	seen := make(map[string]bool)
	var result []string
	for _, f := range fields {
		if !seen[f] {
			seen[f] = true
			result = append(result, f)
		}
	}
	return result
}

// ── Commands ───────────────────────────────────────────────────────

type dispatchResultMsg struct {
	ticket        string
	worker        string
	orchestrated  bool
	backgrounded  bool
	subIssueCount int
	err           error
}

type batchDispatchResultMsg struct {
	tickets    []string
	dispatched int
	err        error
}

type fetchAllResultMsg struct {
	tickets []string
	err     error
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
		prompt, err := buildWorkerReviewPrompt(repo, name)
		if err != nil {
			return reviewResultMsg{worker: name, err: err}
		}
		_, err = resumeWorker(repo, name, resumeOpts{
			Prompt: prompt,
		})
		if err != nil {
			return reviewResultMsg{worker: name, err: err}
		}
		return reviewResultMsg{worker: name}
	}
}

func (m tuiModel) doSend(workerName, prompt string) tea.Cmd {
	repo := m.repo
	return func() tea.Msg {
		_, err := resumeWorker(repo, workerName, resumeOpts{
			Prompt:       prompt,
			SystemPrompt: sendSystemPrompt(ghRepo(repo)),
		})
		if err != nil {
			return sendResultMsg{worker: workerName, err: err}
		}
		return sendResultMsg{worker: workerName}
	}
}

func (m tuiModel) doDispatchBatch(tickets []string) tea.Cmd {
	repo := m.repo
	return func() tea.Msg {
		ensurePoolCapacityForBatch(repo, tickets)

		dispatched := 0
		for _, ticket := range tickets {
			opts := &DispatchOpts{
				TicketID: ticket,
				Model:    "opus",
			}
			dgFile := dispatchGroupFile(repo, ticket)
			if dg := syncExistingGroup(repo, dgFile); dg != nil {
				if !isGroupTerminal(dg) {
					if err := spawnOrchestrator(repo, ticket, opts); err != nil {
						return batchDispatchResultMsg{err: fmt.Errorf("orchestrate %s: %v", ticket, err)}
					}
				}
				dispatched++
				continue
			}
			if err := spawnOrchestrator(repo, ticket, opts); err != nil {
				return batchDispatchResultMsg{err: fmt.Errorf("orchestrate %s: %v", ticket, err)}
			}
			dispatched++
		}
		return batchDispatchResultMsg{
			tickets:    tickets,
			dispatched: dispatched,
		}
	}
}

// ensurePoolCapacityForBatch grows the pool up front so concurrent orchestrators
// don't race on cmdPoolResize. Counts only tickets that will actually dispatch
// (skips groups already in a terminal state).
func ensurePoolCapacityForBatch(r *RepoContext, tickets []string) {
	cfg, err := loadPoolConfig(r.poolConfigFile())
	if err != nil {
		return
	}
	need := 0
	for _, ticket := range tickets {
		dgFile := dispatchGroupFile(r, ticket)
		if dg := syncExistingGroup(r, dgFile); dg != nil && isGroupTerminal(dg) {
			continue
		}
		need++
	}
	if need == 0 {
		return
	}
	idle := countIdleWorkers(r)
	if idle >= need {
		return
	}
	newSize := cfg.Size + (need - idle)
	cmdPoolResize([]string{fmt.Sprintf("%d", newSize)})
}

func (m tuiModel) doFetchAll() tea.Cmd {
	repo := m.repo
	return func() tea.Msg {
		label := "ready-for-agent"
		prompt := fmt.Sprintf(
			"Use the Linear MCP list_issues tool to find issues with label '%s' that are in 'Todo' state for the Ameba team. Return ONLY the issue identifiers (e.g. AMBA-42) as a JSON array in this exact format: {\"tickets\": [\"AMBA-1\", \"AMBA-2\"]}",
			label,
		)
		output, err := claudeQuery(repo.Root, prompt,
			"mcp__claude_ai_Linear__list_issues,mcp__claude_ai_Linear__get_issue")
		if err != nil {
			return fetchAllResultMsg{err: err}
		}
		tickets := parseTicketResponse(output)
		return fetchAllResultMsg{tickets: tickets}
	}
}

func (m tuiModel) doDispatch(ticket string) tea.Cmd {
	repo := m.repo
	return func() tea.Msg {
		opts := &DispatchOpts{
			TicketID: ticket,
			Model:    "opus",
		}

		dgFile := dispatchGroupFile(repo, ticket)
		if dg := syncExistingGroup(repo, dgFile); dg != nil {
			if !isGroupTerminal(dg) {
				if err := spawnOrchestrator(repo, ticket, opts); err != nil {
					return dispatchResultMsg{ticket: ticket, err: err}
				}
			}
			return dispatchResultMsg{
				ticket:        ticket,
				orchestrated:  true,
				subIssueCount: len(dg.SubIssues),
			}
		}

		if err := spawnOrchestrator(repo, ticket, opts); err != nil {
			return dispatchResultMsg{ticket: ticket, err: err}
		}
		return dispatchResultMsg{ticket: ticket, backgrounded: true}
	}
}

// ── Tail helpers ───────────────────────────────────────────────────

func (m *tuiModel) loadTailLines() {
	for _, w := range m.workers {
		if w.name == m.tailWorker && w.state.LogFile != nil {
			lines, newOffset := readLogTail(*w.state.LogFile, m.tailOffset)
			if len(lines) == 0 && m.tailOffset == newOffset {
				return
			}
			m.tailLines = append(m.tailLines, lines...)
			m.tailOffset = newOffset
			maxLines := 5000
			if len(m.tailLines) > maxLines {
				m.tailLines = m.tailLines[len(m.tailLines)-maxLines:]
			}
			m.tailViewport.SetContent(strings.Join(m.tailLines, "\n"))
			if m.tailFollowing {
				m.tailViewport.GotoBottom()
			}
			return
		}
	}
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

	follow := "off"
	if m.tailFollowing {
		follow = "on"
	}
	pct := int(m.tailViewport.ScrollPercent() * 100)
	header := fmt.Sprintf("Tailing %s  follow:%s  %d%%  [j/k scroll, g/G top/bottom, q return]",
		displayWorker(m.tailWorker), follow, pct)
	b.WriteString(header)
	b.WriteString("\n")
	b.WriteString(strings.Repeat("-", len(header)))
	b.WriteString("\n\n")
	b.WriteString(m.tailViewport.View())

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

	b.WriteString("Dispatch ticket(s):\n\n")
	b.WriteString(m.dispatchArea.View())
	b.WriteString("\n\n")
	b.WriteString(m.status)

	return b.String()
}
