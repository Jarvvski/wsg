package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
)

type streamEvent struct {
	Type    string `json:"type"`
	Subtype string `json:"subtype"`

	// assistant message
	Message *streamMessage `json:"message"`

	// tool_use / tool_result
	Tool *streamTool `json:"tool"`

	// result
	DurationMs  int     `json:"duration_ms"`
	NumTurns    int     `json:"num_turns"`
	TotalCost   float64 `json:"total_cost_usd"`
	IsError     bool    `json:"is_error"`
	StopReason  string  `json:"stop_reason"`
}

type streamMessage struct {
	Content []streamContent `json:"content"`
	Usage   *streamUsage    `json:"usage"`
}

type streamUsage struct {
	InputTokens              int `json:"input_tokens"`
	OutputTokens             int `json:"output_tokens"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
}

type streamContent struct {
	Type  string `json:"type"`
	Text  string `json:"text"`
	Name  string `json:"name"`
	Input any    `json:"input"`
	ID    string `json:"id"`
}

type streamTool struct {
	Type      string `json:"type"`
	Name      string `json:"name"`
	Content   string `json:"content"`
	ToolUseID string `json:"tool_use_id"`
}

type agentEntry struct {
	id     string
	tokens int
}

type logState struct {
	seen          map[string]bool
	contextTokens int
	agentStack    []agentEntry
}

func streamLogs(path string) {
	f, err := os.Open(path)
	if err != nil {
		fatal("Cannot open %s: %v", path, err)
	}
	defer f.Close()

	reader := bufio.NewReader(f)
	state := &logState{seen: make(map[string]bool)}

	for {
		line, err := reader.ReadString('\n')
		if line != "" {
			line = strings.TrimSpace(line)
			if line != "" {
				formatEvent(line, state)
			}
		}
		if err != nil {
			if err == io.EOF {
				time.Sleep(200 * time.Millisecond)
				continue
			}
			return
		}
	}
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
				input := summarizeInput(c.Input)
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

func summarizeInput(input any) string {
	if input == nil {
		return ""
	}
	m, ok := input.(map[string]any)
	if !ok {
		return ""
	}
	// Show the most useful field for common tools
	if cmd, ok := m["command"].(string); ok {
		short := cmd
		if len(short) > 80 {
			short = short[:77] + "..."
		}
		return " " + colorize(short, colorDim)
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
		short := query
		if len(short) > 80 {
			short = short[:77] + "..."
		}
		return " " + colorize(short, colorDim)
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
