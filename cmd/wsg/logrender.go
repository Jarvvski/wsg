package main

import (
	"encoding/json"
	"fmt"
	"strings"
)

type agentEntry struct {
	id     string
	tokens int
}

type logState struct {
	seen          map[string]bool
	contextTokens int
	agentStack    []agentEntry
}

func formatEvent(line string, state *logState) {
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
		if ev.Message.Usage != nil {
			newTokens := ev.Message.Usage.InputTokens +
				ev.Message.Usage.CacheReadInputTokens +
				ev.Message.Usage.CacheCreationInputTokens +
				ev.Message.Usage.OutputTokens
			for len(state.agentStack) > 0 && newTokens < state.contextTokens {
				top := state.agentStack[len(state.agentStack)-1]
				if top.tokens > 0 && newTokens < top.tokens*2 {
					state.agentStack = state.agentStack[:len(state.agentStack)-1]
					depth := len(state.agentStack)
					closeStr := treeClose(depth)
					fmt.Printf("       %s\n", colorize(closeStr, colorDim))
				} else {
					break
				}
			}
			state.contextTokens = newTokens
		}
		for _, c := range ev.Message.Content {
			switch c.Type {
			case "text":
				if c.Text == "" {
					return
				}
				if state.seen[c.Text] {
					return
				}
				state.seen[c.Text] = true
				depth := len(state.agentStack)
				if depth > 0 {
					prefix := colorize(treeBranch(depth), colorDim)
					for _, line := range strings.Split(c.Text, "\n") {
						fmt.Printf("       %s%s\n", prefix, line)
					}
				} else {
					fmt.Println(c.Text)
				}
			case "tool_use":
				input := summarizeInput(c.Input, 80)
				ctx := contextBadge(state.contextTokens)
				depth := len(state.agentStack)
				var prefix string
				if c.Name == "Agent" {
					prefix = treeAgentBranch(depth)
				} else {
					prefix = treeBranch(depth)
				}
				fmt.Printf("%s %s%s%s\n",
					ctx,
					colorize(prefix, colorDim),
					colorize(c.Name, colorYellow),
					input,
				)
				if c.Name == "Agent" {
					state.agentStack = append(state.agentStack, agentEntry{
						id:     c.ID,
						tokens: state.contextTokens,
					})
				}
			}
		}

	case "tool":
		if ev.Tool == nil || len(state.agentStack) == 0 {
			return
		}
		isAgent := ev.Tool.Name == "Agent"
		if !isAgent && ev.Tool.ToolUseID != "" {
			for _, entry := range state.agentStack {
				if entry.id == ev.Tool.ToolUseID {
					isAgent = true
					break
				}
			}
		}
		if !isAgent {
			return
		}
		state.agentStack = state.agentStack[:len(state.agentStack)-1]
		depth := len(state.agentStack)
		closeStr := treeClose(depth)
		fmt.Printf("       %s\n", colorize(closeStr, colorDim))

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
		var parts []string
		for _, c := range ev.Message.Content {
			switch c.Type {
			case "text":
				if c.Text != "" && !state.seen[c.Text] {
					state.seen[c.Text] = true
					parts = append(parts, c.Text)
				}
			case "tool_use":
				input := summarizeInput(c.Input, 0)
				parts = append(parts, colorize(c.Name, colorYellow)+input)
			}
		}
		return strings.Join(parts, " ")
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
