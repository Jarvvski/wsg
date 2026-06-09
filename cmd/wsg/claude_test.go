package main

import "testing"

func TestUnwrapClaudeJSON(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "direct JSON",
			input: `{"tickets": ["AMBA-1"]}`,
			want:  `{"tickets": ["AMBA-1"]}`,
		},
		{
			name:  "wrapped in result",
			input: `{"result": "{\"tickets\": [\"AMBA-42\"]}"}`,
			want:  `{"tickets": ["AMBA-42"]}`,
		},
		{
			name:  "result with markdown",
			input: `{"result": "Here is the result:\n{\"key\": \"value\"}"}`,
			want:  `{"key": "value"}`,
		},
		{
			name:  "plain text",
			input: `not json`,
			want:  `not json`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := unwrapClaudeJSON(tt.input)
			if got != tt.want {
				t.Errorf("unwrapClaudeJSON(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestExtractJSON(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "pure JSON",
			input: `{"tickets": ["AMBA-1"]}`,
			want:  `{"tickets": ["AMBA-1"]}`,
		},
		{
			name:  "JSON with leading text",
			input: `Here is the result: {"key": "value"}`,
			want:  `{"key": "value"}`,
		},
		{
			name:  "JSON with trailing text",
			input: `{"key": "value"} some trailing text`,
			want:  `{"key": "value"}`,
		},
		{
			name:  "nested braces",
			input: `prefix {"outer": {"inner": 1}} suffix`,
			want:  `{"outer": {"inner": 1}}`,
		},
		{
			name:  "no JSON",
			input: `just plain text`,
			want:  `just plain text`,
		},
		{
			name:  "empty string",
			input: ``,
			want:  ``,
		},
		{
			name:  "markdown code block",
			input: "```json\n{\"sub_issues\": {}}\n```",
			want:  `{"sub_issues": {}}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractJSON(tt.input)
			if got != tt.want {
				t.Errorf("extractJSON(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}
