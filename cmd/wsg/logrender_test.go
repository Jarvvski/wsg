package main

import (
	"bytes"
	"io"
	"os"
	"testing"
)

func captureOutput(fn func()) string {
	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w
	fn()
	w.Close()
	os.Stdout = old
	var buf bytes.Buffer
	io.Copy(&buf, r)
	return buf.String()
}

func TestFormatEventAssistantText(t *testing.T) {
	seen := &logState{seen: make(map[string]bool)}
	out := captureOutput(func() {
		formatEvent(`{"type":"assistant","message":{"content":[{"type":"text","text":"Hello world"}]}}`, seen)
	})
	if out != "Hello world\n" {
		t.Errorf("got %q, want %q", out, "Hello world\n")
	}
}

func TestFormatEventDeduplicatesText(t *testing.T) {
	seen := &logState{seen: make(map[string]bool)}
	captureOutput(func() {
		formatEvent(`{"type":"assistant","message":{"content":[{"type":"text","text":"Hello"}]}}`, seen)
	})
	out := captureOutput(func() {
		formatEvent(`{"type":"assistant","message":{"content":[{"type":"text","text":"Hello"}]}}`, seen)
	})
	if out != "" {
		t.Errorf("duplicate text should be suppressed, got %q", out)
	}
}

func TestFormatEventToolUse(t *testing.T) {
	origTTY := isTTY
	isTTY = false
	defer func() { isTTY = origTTY }()

	state := &logState{seen: make(map[string]bool)}
	out := captureOutput(func() {
		formatEvent(`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Bash","input":{"command":"ls -la"}}]}}`, state)
	})
	if out != "[  ?k] ├─ Bash ls -la\n" {
		t.Errorf("got %q", out)
	}
}

func TestFormatEventResult(t *testing.T) {
	seen := &logState{seen: make(map[string]bool)}
	origTTY := isTTY
	isTTY = false
	defer func() { isTTY = origTTY }()

	out := captureOutput(func() {
		formatEvent(`{"type":"result","duration_ms":5000,"num_turns":3,"total_cost_usd":0.42,"is_error":false}`, seen)
	})
	if out != "\n--- done in 5s, 3 turns, $0.42\n" {
		t.Errorf("got %q", out)
	}
}

func TestFormatEventInit(t *testing.T) {
	seen := &logState{seen: make(map[string]bool)}
	origTTY := isTTY
	isTTY = false
	defer func() { isTTY = origTTY }()

	out := captureOutput(func() {
		formatEvent(`{"type":"system","subtype":"init"}`, seen)
	})
	if out != "--- session started ---\n" {
		t.Errorf("got %q", out)
	}
}

func TestFormatEventInvalidJSON(t *testing.T) {
	seen := &logState{seen: make(map[string]bool)}
	out := captureOutput(func() {
		formatEvent("not json at all", seen)
	})
	if out != "not json at all\n" {
		t.Errorf("got %q, want raw passthrough", out)
	}
}

func TestTreeBranch(t *testing.T) {
	tests := []struct {
		depth int
		want  string
	}{
		{0, "├─ "},
		{1, "│  ├─ "},
		{2, "│  │  ├─ "},
		{3, "│  │  │  ├─ "},
	}
	for _, tt := range tests {
		got := treeBranch(tt.depth)
		if got != tt.want {
			t.Errorf("treeBranch(%d) = %q, want %q", tt.depth, got, tt.want)
		}
	}
}

func TestTreeAgentBranch(t *testing.T) {
	tests := []struct {
		depth int
		want  string
	}{
		{0, "├──╮ "},
		{1, "│  ├──╮ "},
		{2, "│  │  ├──╮ "},
	}
	for _, tt := range tests {
		got := treeAgentBranch(tt.depth)
		if got != tt.want {
			t.Errorf("treeAgentBranch(%d) = %q, want %q", tt.depth, got, tt.want)
		}
	}
}

func TestTreeClose(t *testing.T) {
	tests := []struct {
		depth int
		want  string
	}{
		{0, "╰─"},
		{1, "│  ╰─"},
		{2, "│  │  ╰─"},
	}
	for _, tt := range tests {
		got := treeClose(tt.depth)
		if got != tt.want {
			t.Errorf("treeClose(%d) = %q, want %q", tt.depth, got, tt.want)
		}
	}
}

func TestAgentNesting(t *testing.T) {
	origTTY := isTTY
	isTTY = false
	defer func() { isTTY = origTTY }()

	state := &logState{seen: make(map[string]bool)}

	// Tool at depth 0 uses branch
	out := captureOutput(func() {
		formatEvent(`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Bash","input":{"command":"ls"}}]}}`, state)
	})
	if out != "[  ?k] ├─ Bash ls\n" {
		t.Errorf("depth 0 tool: got %q", out)
	}

	// Agent at depth 0 opens sub-tree
	out = captureOutput(func() {
		formatEvent(`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Agent","id":"agent_1","input":{"description":"Explore code"}}]}}`, state)
	})
	if out != "[  ?k] ├──╮ Agent Explore code\n" {
		t.Errorf("agent at depth 0: got %q", out)
	}
	if len(state.agentStack) != 1 {
		t.Fatalf("agentStack len = %d, want 1", len(state.agentStack))
	}

	// Tool at depth 1 has parent continuation
	out = captureOutput(func() {
		formatEvent(`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Grep","input":{"pattern":"foo"}}]}}`, state)
	})
	if out != "[  ?k] │  ├─ Grep foo\n" {
		t.Errorf("depth 1 tool: got %q", out)
	}

	// Text at depth 1 uses branch
	out = captureOutput(func() {
		formatEvent(`{"type":"assistant","message":{"content":[{"type":"text","text":"Found it"}]}}`, state)
	})
	if out != "       │  ├─ Found it\n" {
		t.Errorf("depth 1 text: got %q", out)
	}

	// Agent tool_result closes sub-tree
	out = captureOutput(func() {
		formatEvent(`{"type":"tool","tool":{"type":"tool_result","name":"Agent","tool_use_id":"agent_1"}}`, state)
	})
	if out != "       ╰─\n" {
		t.Errorf("agent close: got %q", out)
	}
	if len(state.agentStack) != 0 {
		t.Fatalf("agentStack len = %d, want 0", len(state.agentStack))
	}

	// Back to depth 0
	out = captureOutput(func() {
		formatEvent(`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Read","input":{"file_path":"/bar.go"}}]}}`, state)
	})
	if out != "[  ?k] ├─ Read /bar.go\n" {
		t.Errorf("back to depth 0: got %q", out)
	}
}

func TestNestedAgents(t *testing.T) {
	origTTY := isTTY
	isTTY = false
	defer func() { isTTY = origTTY }()

	state := &logState{seen: make(map[string]bool)}

	// Outer agent
	captureOutput(func() {
		formatEvent(`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Agent","id":"a1","input":{"description":"Outer"}}]}}`, state)
	})
	// Inner agent at depth 1 opens sub-tree
	out := captureOutput(func() {
		formatEvent(`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Agent","id":"a2","input":{"description":"Inner"}}]}}`, state)
	})
	if out != "[  ?k] │  ├──╮ Agent Inner\n" {
		t.Errorf("inner agent: got %q", out)
	}
	if len(state.agentStack) != 2 {
		t.Fatalf("agentStack len = %d, want 2", len(state.agentStack))
	}

	// Tool at depth 2
	out = captureOutput(func() {
		formatEvent(`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Read","input":{"file_path":"/x.go"}}]}}`, state)
	})
	if out != "[  ?k] │  │  ├─ Read /x.go\n" {
		t.Errorf("depth 2 tool: got %q", out)
	}

	// Inner agent closes
	out = captureOutput(func() {
		formatEvent(`{"type":"tool","tool":{"type":"tool_result","name":"Agent","tool_use_id":"a2"}}`, state)
	})
	if out != "       │  ╰─\n" {
		t.Errorf("inner close: got %q", out)
	}

	// Back to depth 1
	out = captureOutput(func() {
		formatEvent(`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Read","input":{"file_path":"/y.go"}}]}}`, state)
	})
	if out != "[  ?k] │  ├─ Read /y.go\n" {
		t.Errorf("back to depth 1: got %q", out)
	}

	// Outer agent closes
	out = captureOutput(func() {
		formatEvent(`{"type":"tool","tool":{"type":"tool_result","name":"Agent","tool_use_id":"a1"}}`, state)
	})
	if out != "       ╰─\n" {
		t.Errorf("outer close: got %q", out)
	}
}

func TestAgentCompletionByContextDrop(t *testing.T) {
	origTTY := isTTY
	isTTY = false
	defer func() { isTTY = origTTY }()

	state := &logState{seen: make(map[string]bool)}

	// Set initial context to 34k
	captureOutput(func() {
		formatEvent(`{"type":"assistant","message":{"content":[{"type":"text","text":"start"}],"usage":{"input_tokens":30000,"output_tokens":4000,"cache_read_input_tokens":0,"cache_creation_input_tokens":0}}}`, state)
	})

	// Agent pushed at 34k context
	captureOutput(func() {
		formatEvent(`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Agent","id":"a1","input":{"description":"Explore"}}]}}`, state)
	})
	if len(state.agentStack) != 1 || state.agentStack[0].tokens != 34000 {
		t.Fatalf("agentStack = %+v, want [{id:a1 tokens:34000}]", state.agentStack)
	}

	// Sub-agent runs, context grows to 139k
	captureOutput(func() {
		formatEvent(`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Read","input":{"file_path":"/x.go"}}],"usage":{"input_tokens":100000,"output_tokens":20000,"cache_read_input_tokens":10000,"cache_creation_input_tokens":9000}}}`, state)
	})

	// Context drops to 36k - agent finished, parent resumed
	out := captureOutput(func() {
		formatEvent(`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Bash","input":{"command":"ls"}}],"usage":{"input_tokens":30000,"output_tokens":6000,"cache_read_input_tokens":0,"cache_creation_input_tokens":0}}}`, state)
	})
	if len(state.agentStack) != 0 {
		t.Fatalf("agentStack should be empty after context drop, len = %d", len(state.agentStack))
	}
	want := "       ╰─\n[ 36k] ├─ Bash ls\n"
	if out != want {
		t.Errorf("context drop:\ngot  %q\nwant %q", out, want)
	}
}

func TestNestedAgentCompletionByContextDrop(t *testing.T) {
	origTTY := isTTY
	isTTY = false
	defer func() { isTTY = origTTY }()

	state := &logState{seen: make(map[string]bool)}

	// Set context to 30k, push outer agent
	captureOutput(func() {
		formatEvent(`{"type":"assistant","message":{"content":[{"type":"text","text":"s"}],"usage":{"input_tokens":25000,"output_tokens":5000,"cache_read_input_tokens":0,"cache_creation_input_tokens":0}}}`, state)
	})
	captureOutput(func() {
		formatEvent(`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Agent","id":"a1","input":{"description":"Outer"}}]}}`, state)
	})

	// Outer agent runs, context grows to 100k, push inner agent
	captureOutput(func() {
		formatEvent(`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Agent","id":"a2","input":{"description":"Inner"}}],"usage":{"input_tokens":80000,"output_tokens":10000,"cache_read_input_tokens":5000,"cache_creation_input_tokens":5000}}}`, state)
	})
	if len(state.agentStack) != 2 {
		t.Fatalf("agentStack len = %d, want 2", len(state.agentStack))
	}

	// Inner agent grows to 200k
	captureOutput(func() {
		formatEvent(`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Read","input":{"file_path":"/x.go"}}],"usage":{"input_tokens":150000,"output_tokens":30000,"cache_read_input_tokens":10000,"cache_creation_input_tokens":10000}}}`, state)
	})

	// Context drops to 105k - inner agent finished
	out := captureOutput(func() {
		formatEvent(`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Read","input":{"file_path":"/y.go"}}],"usage":{"input_tokens":80000,"output_tokens":15000,"cache_read_input_tokens":5000,"cache_creation_input_tokens":5000}}}`, state)
	})
	if len(state.agentStack) != 1 {
		t.Fatalf("after inner drop: agentStack len = %d, want 1", len(state.agentStack))
	}
	want := "       │  ╰─\n[105k] │  ├─ Read /y.go\n"
	if out != want {
		t.Errorf("inner agent drop:\ngot  %q\nwant %q", out, want)
	}

	// Context drops to 35k - outer agent finished
	out = captureOutput(func() {
		formatEvent(`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Bash","input":{"command":"done"}}],"usage":{"input_tokens":30000,"output_tokens":5000,"cache_read_input_tokens":0,"cache_creation_input_tokens":0}}}`, state)
	})
	if len(state.agentStack) != 0 {
		t.Fatalf("after outer drop: agentStack len = %d, want 0", len(state.agentStack))
	}
	want = "       ╰─\n[ 35k] ├─ Bash done\n"
	if out != want {
		t.Errorf("outer agent drop:\ngot  %q\nwant %q", out, want)
	}
}

func TestSummarizeInput(t *testing.T) {
	origTTY := isTTY
	isTTY = false
	defer func() { isTTY = origTTY }()

	tests := []struct {
		name   string
		input  any
		maxLen int
		want   string
	}{
		{"nil", nil, 80, ""},
		{"command", map[string]any{"command": "git status"}, 80, " git status"},
		{"file_path", map[string]any{"file_path": "/foo/bar.go"}, 80, " /foo/bar.go"},
		{"pattern", map[string]any{"pattern": "*.go"}, 80, " *.go"},
		{"description", map[string]any{"description": "Explore code"}, 80, " Explore code"},
		{"query", map[string]any{"query": "search term"}, 80, " search term"},
		{"unknown", map[string]any{"other": "val"}, 80, ""},
		{"long command", map[string]any{"command": "a very long command that exceeds eighty characters in total length and should be truncated for display purposes"}, 80, " a very long command that exceeds eighty characters in total length and should..."},
		{"long query", map[string]any{"query": "a very long query that exceeds eighty characters in total length and should be truncated for display purposes in logs"}, 80, " a very long query that exceeds eighty characters in total length and should b..."},
		{"long command untruncated", map[string]any{"command": "a very long command that exceeds eighty characters in total length and should be truncated for display purposes"}, 0, " a very long command that exceeds eighty characters in total length and should be truncated for display purposes"},
		{"long query untruncated", map[string]any{"query": "a very long query that exceeds eighty characters in total length and should be truncated for display purposes in logs"}, 0, " a very long query that exceeds eighty characters in total length and should be truncated for display purposes in logs"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := summarizeInput(tt.input, tt.maxLen)
			if got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestContextBadge(t *testing.T) {
	origTTY := isTTY
	isTTY = false
	defer func() { isTTY = origTTY }()

	tests := []struct {
		tokens int
		want   string
	}{
		{0, "[  ?k]"},
		{50000, "[ 50k]"},
		{99999, "[ 99k]"},
		{100000, "[100k]"},
		{150000, "[150k]"},
		{249999, "[249k]"},
		{250000, "[250k]"},
		{500000, "[500k]"},
	}
	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			got := contextBadge(tt.tokens)
			if got != tt.want {
				t.Errorf("contextBadge(%d) = %q, want %q", tt.tokens, got, tt.want)
			}
		})
	}
}

func TestFormatEventToolUseWithContext(t *testing.T) {
	origTTY := isTTY
	isTTY = false
	defer func() { isTTY = origTTY }()

	state := &logState{seen: make(map[string]bool)}
	// First event sets usage
	captureOutput(func() {
		formatEvent(`{"type":"assistant","message":{"content":[{"type":"text","text":"thinking..."}],"usage":{"input_tokens":80000,"output_tokens":5000,"cache_read_input_tokens":10000,"cache_creation_input_tokens":5000}}}`, state)
	})
	if state.contextTokens != 100000 {
		t.Fatalf("contextTokens = %d, want 100000", state.contextTokens)
	}
	// Next tool use shows the context
	out := captureOutput(func() {
		formatEvent(`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Read","input":{"file_path":"/foo.go"}}]}}`, state)
	})
	if out != "[100k] ├─ Read /foo.go\n" {
		t.Errorf("got %q", out)
	}
}
