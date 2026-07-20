package main

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

type agentEntry struct {
	id       string
	parentID string
	label    string
	tokens   int
}

type logState struct {
	seen            map[string]bool
	contextTokens   int
	activeAgents    map[string]agentEntry
	legacyAgentPath []string
}

func (s *logState) init() {
	if s.seen == nil {
		s.seen = make(map[string]bool)
	}
	if s.activeAgents == nil {
		s.activeAgents = make(map[string]agentEntry)
	}
}

func (s *logState) messageParent(ev streamEvent) (string, bool) {
	if len(ev.ParentToolUseID) > 0 {
		if string(ev.ParentToolUseID) == "null" {
			return "", true
		}
		var id string
		if json.Unmarshal(ev.ParentToolUseID, &id) == nil {
			return id, true
		}
	}
	if len(s.legacyAgentPath) > 0 {
		return s.legacyAgentPath[len(s.legacyAgentPath)-1], false
	}
	return "", false
}

func (s *logState) agentDepth(parentID string) int {
	depth := 0
	visited := make(map[string]bool)
	for parentID != "" && !visited[parentID] {
		visited[parentID] = true
		entry, ok := s.activeAgents[parentID]
		if !ok {
			break
		}
		depth++
		parentID = entry.parentID
	}
	return depth
}

func (s *logState) startAgent(id, parentID, label string, legacy bool) {
	if id == "" {
		return
	}
	s.activeAgents[id] = agentEntry{id: id, parentID: parentID, label: label, tokens: s.contextTokens}
	if legacy {
		s.legacyAgentPath = append(s.legacyAgentPath, id)
	}
}

func (s *logState) completeAgent(id string) (agentEntry, int, bool) {
	if id == "" && len(s.legacyAgentPath) > 0 {
		id = s.legacyAgentPath[len(s.legacyAgentPath)-1]
	}
	entry, ok := s.activeAgents[id]
	if !ok {
		return agentEntry{}, 0, false
	}
	depth := s.agentDepth(entry.parentID)
	delete(s.activeAgents, id)
	for i := len(s.legacyAgentPath) - 1; i >= 0; i-- {
		if s.legacyAgentPath[i] == id {
			s.legacyAgentPath = append(s.legacyAgentPath[:i], s.legacyAgentPath[i+1:]...)
			break
		}
	}
	return entry, depth, true
}

func (s *logState) applyClaudeUsage(ev streamEvent) []int {
	if ev.Message == nil || ev.Message.Usage == nil {
		return nil
	}
	newTokens := ev.Message.Usage.InputTokens +
		ev.Message.Usage.CacheReadInputTokens +
		ev.Message.Usage.CacheCreationInputTokens +
		ev.Message.Usage.OutputTokens
	var closed []int
	if len(ev.ParentToolUseID) == 0 {
		for len(s.legacyAgentPath) > 0 && newTokens < s.contextTokens {
			id := s.legacyAgentPath[len(s.legacyAgentPath)-1]
			entry, ok := s.activeAgents[id]
			if !ok {
				s.legacyAgentPath = s.legacyAgentPath[:len(s.legacyAgentPath)-1]
				continue
			}
			if entry.tokens == 0 || newTokens >= entry.tokens*2 {
				break
			}
			if _, depth, ok := s.completeAgent(id); ok {
				closed = append(closed, depth)
			}
		}
	}
	s.contextTokens = newTokens
	return closed
}

func isClaudeAgentTool(name string) bool {
	return name == "Agent" || name == "Task"
}

func claudeTextKey(parentID, text string) string {
	return "claude:" + parentID + ":" + text
}

func claudeAgentLabel(id string, input any) string {
	if fields, ok := input.(map[string]any); ok {
		for _, key := range []string{"description", "subagent_type", "prompt"} {
			if value, ok := fields[key].(string); ok && strings.TrimSpace(value) != "" {
				label := strings.Join(strings.Fields(value), " ")
				if len(label) > 40 {
					label = label[:37] + "..."
				}
				return label
			}
		}
	}
	if len(id) > 12 {
		return id[:12]
	}
	return id
}

func (s *logState) agentAssociation(id string) string {
	if id == "" {
		return ""
	}
	if entry, ok := s.activeAgents[id]; ok && entry.label != "" {
		return "[" + entry.label + "] "
	}
	return "[" + claudeAgentLabel(id, nil) + "] "
}

func formatEvent(line string, state *logState) {
	state.init()
	var kind struct {
		Type string `json:"type"`
	}
	if json.Unmarshal([]byte(line), &kind) == nil && isCodexEventType(kind.Type) {
		formatCodexEvent(line, state)
		return
	}
	var ev streamEvent
	if err := json.Unmarshal([]byte(line), &ev); err != nil {
		fmt.Println(line)
		return
	}

	switch ev.Type {
	case "system":
		if ev.Subtype == "init" {
			fmt.Println(colorize("--- session started ---", colorDim))
		}

	case "assistant":
		if ev.Message == nil {
			return
		}
		for _, depth := range state.applyClaudeUsage(ev) {
			fmt.Printf("       %s\n", colorize(treeClose(depth), colorDim))
		}
		parentID, explicitParent := state.messageParent(ev)
		depth := state.agentDepth(parentID)
		association := ""
		if explicitParent {
			association = state.agentAssociation(parentID)
		}
		for _, c := range ev.Message.Content {
			switch c.Type {
			case "text":
				if c.Text == "" {
					return
				}
				textKey := claudeTextKey(parentID, c.Text)
				if state.seen[textKey] {
					return
				}
				state.seen[textKey] = true
				if depth > 0 {
					prefix := colorize(treeBranch(depth), colorDim)
					for _, line := range strings.Split(c.Text, "\n") {
						fmt.Printf("       %s%s%s\n", prefix, colorize(association, colorDim), line)
					}
				} else {
					fmt.Println(c.Text)
				}
			case "tool_use":
				input := summarizeInput(c.Input, 80)
				ctx := contextBadge(state.contextTokens)
				var prefix string
				if isClaudeAgentTool(c.Name) {
					prefix = treeAgentBranch(depth)
				} else {
					prefix = treeBranch(depth)
				}
				fmt.Printf("%s %s%s%s\n",
					ctx,
					colorize(prefix, colorDim),
					colorize(association+c.Name, colorYellow),
					input,
				)
				if isClaudeAgentTool(c.Name) {
					state.startAgent(c.ID, parentID, claudeAgentLabel(c.ID, c.Input), !explicitParent)
				}
			}
		}

	case "user":
		if ev.Message == nil {
			return
		}
		for _, c := range ev.Message.Content {
			if c.Type == "tool_result" {
				if entry, depth, ok := state.completeAgent(c.ToolUseID); ok {
					fmt.Printf("       %s %s\n", colorize(treeClose(depth), colorDim), colorize("["+entry.label+"]", colorDim))
				}
			}
		}

	case "tool":
		if ev.Tool == nil {
			return
		}
		if !isClaudeAgentTool(ev.Tool.Name) && ev.Tool.ToolUseID == "" {
			return
		}
		if _, depth, ok := state.completeAgent(ev.Tool.ToolUseID); ok {
			fmt.Printf("       %s\n", colorize(treeClose(depth), colorDim))
		}

	case "result":
		dur := fmt.Sprintf("%.0fs", float64(ev.DurationMs)/1000)
		cost := fmt.Sprintf("$%.2f", ev.TotalCost)
		status := "done"
		statusColor := colorGreen
		if ev.IsError {
			status = "error"
			statusColor = colorRed
		}
		fmt.Printf("\n%s %s in %s, %d turns, %s\n",
			colorize("---", colorDim),
			colorize(status, statusColor),
			dur,
			ev.NumTurns,
			cost,
		)
	}
}

func formatEventToString(line string, state *logState) string {
	state.init()
	var kind struct {
		Type string `json:"type"`
	}
	if json.Unmarshal([]byte(line), &kind) == nil && isCodexEventType(kind.Type) {
		return formatCodexEventToString(line, state)
	}
	var ev streamEvent
	if err := json.Unmarshal([]byte(line), &ev); err != nil {
		return line
	}

	switch ev.Type {
	case "system":
		if ev.Subtype == "init" {
			return colorize("--- session started ---", colorDim)
		}
	case "assistant":
		if ev.Message == nil {
			return ""
		}
		parentID, explicitParent := state.messageParent(ev)
		association := ""
		if explicitParent {
			association = state.agentAssociation(parentID)
		}
		var parts []string
		for _, c := range ev.Message.Content {
			switch c.Type {
			case "text":
				textKey := claudeTextKey(parentID, c.Text)
				if c.Text != "" && !state.seen[textKey] {
					state.seen[textKey] = true
					parts = append(parts, association+c.Text)
				}
			case "tool_use":
				input := summarizeInput(c.Input, 0)
				parts = append(parts, colorize(association+c.Name, colorYellow)+input)
				if isClaudeAgentTool(c.Name) {
					state.startAgent(c.ID, parentID, claudeAgentLabel(c.ID, c.Input), !explicitParent)
				}
			}
		}
		return strings.Join(parts, " ")
	case "user":
		if ev.Message == nil {
			return ""
		}
		var parts []string
		for _, c := range ev.Message.Content {
			if c.Type == "tool_result" {
				if entry, _, ok := state.completeAgent(c.ToolUseID); ok {
					parts = append(parts, "Agent "+entry.label+" completed")
				}
			}
		}
		return strings.Join(parts, " ")
	case "tool":
		if ev.Tool == nil {
			return ""
		}
		if entry, _, ok := state.completeAgent(ev.Tool.ToolUseID); ok {
			return "Agent " + entry.label + " completed"
		}
		return ""
	case "result":
		dur := fmt.Sprintf("%.0fs", float64(ev.DurationMs)/1000)
		cost := fmt.Sprintf("$%.2f", ev.TotalCost)
		status := "done"
		statusColor := colorGreen
		if ev.IsError {
			status = "error"
			statusColor = colorRed
		}
		return fmt.Sprintf("%s %s in %s, %d turns, %s",
			colorize("---", colorDim),
			colorize(status, statusColor),
			dur,
			ev.NumTurns,
			cost,
		)
	}
	return ""
}

func isCodexEventType(eventType string) bool {
	switch eventType {
	case "thread.started", "turn.started", "turn.completed", "turn.failed", "item.started", "item.updated", "item.completed", "error":
		return true
	default:
		return false
	}
}

func formatCodexEvent(line string, state *logState) {
	formatted := formatCodexEventToString(line, state)
	if formatted != "" {
		fmt.Println(formatted)
	}
}

func formatCodexEventToString(line string, state *logState) string {
	var ev codexEvent
	if err := json.Unmarshal([]byte(line), &ev); err != nil {
		return line
	}
	switch ev.Type {
	case "thread.started":
		return colorize("--- session started ---", colorDim)
	case "item.started", "item.updated", "item.completed":
		if ev.Item == nil {
			return ""
		}
		key := "codex:" + ev.Item.ID
		switch ev.Item.Type {
		case "agent_message":
			if ev.Item.Text == "" || state.seen[key+":"+ev.Item.Text] {
				return ""
			}
			state.seen[key+":"+ev.Item.Text] = true
			return ev.Item.Text
		case "command_execution":
			if ev.Item.Command == "" {
				return ""
			}
			if ev.Type == "item.completed" && (ev.Item.Status == "failed" || ev.Item.Status == "declined") {
				state.seen[key+":completed"] = true
				return colorize("Command "+ev.Item.Status, colorRed) + " " + colorize(ev.Item.Command, colorDim)
			}
			if state.seen[key+":started"] {
				return ""
			}
			state.seen[key+":started"] = true
			return colorize("Command", colorYellow) + " " + colorize(ev.Item.Command, colorDim)
		case "mcp_tool_call":
			name := strings.Trim(strings.Join([]string{ev.Item.Server, ev.Item.Tool}, "."), ".")
			if ev.Type == "item.completed" && ev.Item.Status == "failed" {
				msg := ""
				if ev.Item.Error != nil {
					msg = " " + ev.Item.Error.Message
				}
				state.seen[key+":completed"] = true
				return colorize(name+" failed", colorRed) + msg
			}
			if state.seen[key+":started"] {
				return ""
			}
			state.seen[key+":started"] = true
			return colorize(name, colorYellow)
		case "web_search":
			if state.seen[key] {
				return ""
			}
			state.seen[key] = true
			return colorize("WebSearch", colorYellow) + " " + colorize(ev.Item.Query, colorDim)
		case "file_change":
			if ev.Type != "item.completed" || state.seen[key] {
				return ""
			}
			state.seen[key] = true
			paths := make([]string, 0, len(ev.Item.Changes))
			for _, change := range ev.Item.Changes {
				paths = append(paths, change.Path)
			}
			return strings.TrimSpace(colorize("File changes", colorYellow) + " " + strings.Join(paths, ", "))
		case "error":
			if ev.Type != "item.completed" || state.seen[key] {
				return ""
			}
			state.seen[key] = true
			return colorize("warning", colorYellow) + " " + ev.Item.Message
		case "reasoning":
			if ev.Item.Text == "" || state.seen[key+":"+ev.Item.Text] {
				return ""
			}
			state.seen[key+":"+ev.Item.Text] = true
			return colorize(ev.Item.Text, colorDim)
		case "todo_list":
			if state.seen[key+":"+ev.Type] {
				return ""
			}
			state.seen[key+":"+ev.Type] = true
			completed := 0
			for _, item := range ev.Item.Items {
				if item.Completed {
					completed++
				}
			}
			return fmt.Sprintf("%s %d/%d", colorize("Plan", colorYellow), completed, len(ev.Item.Items))
		case "collab_tool_call":
			if state.seen[key+":"+ev.Type] {
				return ""
			}
			state.seen[key+":"+ev.Type] = true
			statusColor := colorYellow
			if codexCollabFailed(ev.Item) {
				statusColor = colorRed
			}
			return colorize("Collab", statusColor) + " " + codexCollabDetails(ev.Item, true)
		}
	case "turn.completed":
		usage := ""
		if ev.Usage != nil {
			total := ev.Usage.InputTokens + ev.Usage.OutputTokens
			usage = fmt.Sprintf(", %dk tokens", total/1000)
		}
		return fmt.Sprintf("%s %s%s", colorize("---", colorDim), colorize("done", colorGreen), usage)
	case "turn.failed", "error":
		msg := ev.Message
		if ev.Error != nil && ev.Error.Message != "" {
			msg = ev.Error.Message
		}
		if msg == "" {
			msg = ev.Type
		}
		return fmt.Sprintf("%s %s: %s", colorize("---", colorDim), colorize("error", colorRed), msg)
	}
	return ""
}

func codexCollabFailed(item *codexItem) bool {
	if item.Status == "failed" {
		return true
	}
	for _, state := range item.AgentsStates {
		switch state.Status {
		case "errored", "interrupted", "not_found":
			return true
		}
	}
	return false
}

func codexCollabDetails(item *codexItem, includeContext bool) string {
	parts := []string{item.Tool}
	if item.Status != "" {
		parts = append(parts, item.Status)
	}
	if includeContext && item.SenderThreadID != "" {
		parts = append(parts, "sender="+item.SenderThreadID)
	}
	if includeContext && len(item.ReceiverThreadIDs) > 0 {
		parts = append(parts, "receivers="+strings.Join(item.ReceiverThreadIDs, ","))
	}
	if includeContext && strings.TrimSpace(item.Prompt) != "" {
		parts = append(parts, "prompt="+strings.Join(strings.Fields(item.Prompt), " "))
	}

	ids := make([]string, 0, len(item.AgentsStates))
	for id := range item.AgentsStates {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		state := item.AgentsStates[id]
		detail := id + "=" + state.Status
		if state.Message != "" {
			detail += " (" + strings.Join(strings.Fields(state.Message), " ") + ")"
		}
		parts = append(parts, detail)
	}
	if !includeContext && len(ids) == 0 {
		if len(item.ReceiverThreadIDs) > 0 {
			parts = append(parts, strings.Join(item.ReceiverThreadIDs, ","))
		} else if strings.TrimSpace(item.Prompt) != "" {
			parts = append(parts, strings.Join(strings.Fields(item.Prompt), " "))
		}
	}
	return strings.Join(parts, " ")
}

func contextBadge(tokens int) string {
	if tokens == 0 {
		return colorize("[  ?k]", colorDim)
	}
	k := tokens / 1000
	label := fmt.Sprintf("[%3dk]", k)
	if k < 100 {
		return colorize(label, colorGreen)
	} else if k < 250 {
		return colorize(label, colorYellow)
	}
	return colorize(label, colorRed)
}

func treeBranch(depth int) string {
	var b strings.Builder
	for i := 0; i < depth; i++ {
		b.WriteString("│  ")
	}
	b.WriteString("├─ ")
	return b.String()
}

func treeAgentBranch(depth int) string {
	var b strings.Builder
	for i := 0; i < depth; i++ {
		b.WriteString("│  ")
	}
	b.WriteString("├──╮ ")
	return b.String()
}

func treeClose(depth int) string {
	var b strings.Builder
	for i := 0; i < depth; i++ {
		b.WriteString("│  ")
	}
	b.WriteString("╰─")
	return b.String()
}

func summarizeInput(input any, maxLen int) string {
	if input == nil {
		return ""
	}
	m, ok := input.(map[string]any)
	if !ok {
		return ""
	}
	truncate := func(s string) string {
		if maxLen > 0 && len(s) > maxLen {
			return s[:maxLen-3] + "..."
		}
		return s
	}
	if cmd, ok := m["command"].(string); ok {
		return " " + colorize(truncate(cmd), colorDim)
	}
	if fp, ok := m["file_path"].(string); ok {
		return " " + colorize(fp, colorDim)
	}
	if desc, ok := m["description"].(string); ok {
		return " " + colorize(desc, colorDim)
	}
	if pattern, ok := m["pattern"].(string); ok {
		return " " + colorize(pattern, colorDim)
	}
	if query, ok := m["query"].(string); ok {
		return " " + colorize(truncate(query), colorDim)
	}
	return ""
}

func summarizeInputPlain(input any) string {
	if input == nil {
		return ""
	}
	m, ok := input.(map[string]any)
	if !ok {
		return ""
	}
	for _, key := range []string{"command", "file_path", "description", "pattern", "query"} {
		if val, ok := m[key].(string); ok {
			return " " + val
		}
	}
	return ""
}
