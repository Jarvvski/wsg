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
	var ev streamEvent
	if err := json.Unmarshal(lines[len(lines)-1], &ev); err != nil {
		return nil
	}
	if ev.Type != "result" {
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

func extractSessionID(logFile string) (string, error) {
	f, err := os.Open(logFile)
	if err != nil {
		return "", err
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024)
	for scanner.Scan() {
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
