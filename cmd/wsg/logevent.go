package main

// streamEvent is one decoded line from a claude stream-json log.
// Fields are nullable across event types; consumers branch on Type/Subtype.
type streamEvent struct {
	Type      string `json:"type"`
	Subtype   string `json:"subtype"`
	SessionID string `json:"session_id"`

	// assistant message
	Message *streamMessage `json:"message"`

	// tool_use / tool_result
	Tool *streamTool `json:"tool"`

	// result
	DurationMs int     `json:"duration_ms"`
	NumTurns   int     `json:"num_turns"`
	TotalCost  float64 `json:"total_cost_usd"`
	IsError    bool    `json:"is_error"`
	Result     string  `json:"result"`
	StopReason string  `json:"stop_reason"`
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

// codexEvent is one decoded line from `codex exec --json`. Its protocol is
// distinct from Claude's stream-json format, so log consumers inspect the
// event type before reading provider-specific fields.
type codexEvent struct {
	Type     string      `json:"type"`
	ThreadID string      `json:"thread_id"`
	Item     *codexItem  `json:"item"`
	Usage    *codexUsage `json:"usage"`
	Error    *codexError `json:"error"`
	Message  string      `json:"message"`
}

type codexItem struct {
	ID               string            `json:"id"`
	Type             string            `json:"type"`
	Text             string            `json:"text"`
	Command          string            `json:"command"`
	AggregatedOutput string            `json:"aggregated_output"`
	Status           string            `json:"status"`
	Server           string            `json:"server"`
	Tool             string            `json:"tool"`
	Query            string            `json:"query"`
	Message          string            `json:"message"`
	Changes          []codexFileChange `json:"changes"`
	Items            []codexTodoItem   `json:"items"`
	Error            *codexError       `json:"error"`
}

type codexFileChange struct {
	Path string `json:"path"`
	Kind string `json:"kind"`
}

type codexTodoItem struct {
	Text      string `json:"text"`
	Completed bool   `json:"completed"`
}

type codexUsage struct {
	InputTokens           int `json:"input_tokens"`
	CachedInputTokens     int `json:"cached_input_tokens"`
	CacheWriteInputTokens int `json:"cache_write_input_tokens"`
	OutputTokens          int `json:"output_tokens"`
	ReasoningOutputTokens int `json:"reasoning_output_tokens"`
}

type codexError struct {
	Message string `json:"message"`
}
