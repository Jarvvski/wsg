package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
)

type logResult struct {
	Status   WorkerStatus
	ExitCode *int
	Error    *string
}

func readLogResult(logFile string) *logResult {
	data, err := os.ReadFile(logFile)
	if err != nil {
		return nil
	}
	lines := bytes.Split(bytes.TrimSpace(data), []byte("\n"))
	if len(lines) == 0 {
		return nil
	}
	for i := len(lines) - 1; i >= 0; i-- {
		var raw struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(lines[i], &raw); err != nil {
			continue
		}
		switch raw.Type {
		case "result":
			var ev streamEvent
			if err := json.Unmarshal(lines[i], &ev); err != nil {
				return nil
			}
			if ev.Subtype == "success" && !ev.IsError {
				ec := 0
				return &logResult{Status: WorkerStatusDone, ExitCode: &ec}
			}
			ec := 1
			errMsg := ev.Result
			if errMsg == "" {
				errMsg = ev.Subtype
			}
			return &logResult{Status: WorkerStatusFailed, ExitCode: &ec, Error: &errMsg}
		case "turn.completed":
			ec := 0
			return &logResult{Status: WorkerStatusDone, ExitCode: &ec}
		case "turn.failed", "error":
			var ev codexEvent
			if err := json.Unmarshal(lines[i], &ev); err != nil {
				return nil
			}
			ec := 1
			errMsg := ev.Message
			if ev.Error != nil && ev.Error.Message != "" {
				errMsg = ev.Error.Message
			}
			if errMsg == "" {
				errMsg = raw.Type
			}
			return &logResult{Status: WorkerStatusFailed, ExitCode: &ec, Error: &errMsg}
		default:
			if raw.Type == "" {
				continue
			}
			if !strings.Contains(raw.Type, ".") {
				return nil
			}
		}
	}
	return nil
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

	readSize := int64(65536)
	if fi.Size() < readSize {
		readSize = fi.Size()
	}
	f.Seek(-readSize, io.SeekEnd)

	data := make([]byte, readSize)
	n, _ := f.Read(data)
	data = data[:n]

	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		var codex codexEvent
		if err := json.Unmarshal([]byte(lines[i]), &codex); err == nil {
			if activity := codexActivity(codex); activity != "" {
				if len(activity) > 50 {
					activity = activity[:47] + "..."
				}
				return activity
			}
		}
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

func codexActivity(ev codexEvent) string {
	switch ev.Type {
	case "turn.completed":
		if ev.Usage == nil {
			return "done"
		}
		return fmt.Sprintf("done %dk tokens", (ev.Usage.InputTokens+ev.Usage.OutputTokens)/1000)
	case "turn.failed", "error":
		msg := ev.Message
		if ev.Error != nil && ev.Error.Message != "" {
			msg = ev.Error.Message
		}
		if msg == "" {
			return "error"
		}
		return "error " + msg
	case "item.started", "item.completed", "item.updated":
		if ev.Item == nil {
			return ""
		}
		switch ev.Item.Type {
		case "command_execution":
			return ev.Item.Command
		case "mcp_tool_call":
			name := strings.Trim(strings.Join([]string{ev.Item.Server, ev.Item.Tool}, "."), ".")
			if ev.Item.Status == "failed" && ev.Item.Error != nil {
				return name + " failed: " + ev.Item.Error.Message
			}
			return name
		case "web_search":
			return "search " + ev.Item.Query
		case "file_change":
			return "file changes"
		case "agent_message":
			return ev.Item.Text
		case "error":
			return "warning " + ev.Item.Message
		case "reasoning":
			return ev.Item.Text
		case "todo_list":
			return "plan updated"
		case "collab_tool_call":
			return "collab " + ev.Item.Tool
		}
	}
	return ""
}

func extractSessionID(logFile string) (string, error) {
	f, err := os.Open(logFile)
	if err != nil {
		return "", err
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024)
	for scanner.Scan() {
		var codex codexEvent
		if err := json.Unmarshal(scanner.Bytes(), &codex); err == nil && codex.Type == "thread.started" && codex.ThreadID != "" {
			return codex.ThreadID, nil
		}
		var ev streamEvent
		if err := json.Unmarshal(scanner.Bytes(), &ev); err != nil {
			continue
		}
		if ev.Type == "system" && ev.Subtype == "init" && ev.SessionID != "" {
			return ev.SessionID, nil
		}
	}
	return "", fmt.Errorf("no session ID found in log")
}
