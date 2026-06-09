package main

import (
	"encoding/json"
	"fmt"
	"strings"
)

// claude.go is the single seam onto the `claude` CLI. It owns the prompt
// round-trip (stream-json result unwrap, fenced/embedded JSON extraction)
// so callers like linear.go can stay on a clean query() boundary.

func claudeQuery(dir, prompt, allowedTools string) (string, error) {
	output, stderr, err := runCapture(dir, "claude", "-p",
		"--model", "haiku",
		"--output-format", "json",
		"--no-session-persistence",
		"--allowedTools="+allowedTools,
		prompt,
	)
	if err != nil {
		diag := stderr
		if diag == "" {
			diag = output
		}
		return "", fmt.Errorf("claude failed: %s", diag)
	}
	return unwrapClaudeJSON(output), nil
}

func unwrapClaudeJSON(output string) string {
	var wrapper struct {
		Result string `json:"result"`
	}
	if err := json.Unmarshal([]byte(output), &wrapper); err == nil && wrapper.Result != "" {
		output = wrapper.Result
	}
	return extractJSON(output)
}

// extractJSON returns the first balanced {...} object inside s. Used to
// recover JSON payloads from claude responses that wrap them in prose or
// markdown fences. Falls back to s when no opening brace is found.
func extractJSON(s string) string {
	start := strings.Index(s, "{")
	if start == -1 {
		return s
	}
	depth := 0
	for i := start; i < len(s); i++ {
		switch s[i] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return s[start : i+1]
			}
		}
	}
	return s[start:]
}
