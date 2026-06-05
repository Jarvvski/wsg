package main

import "testing"

func TestParseLinearTickets(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  []string
	}{
		{
			name:  "direct JSON",
			input: `{"tickets": ["AMBA-1", "AMBA-2", "AMBA-3"]}`,
			want:  []string{"AMBA-1", "AMBA-2", "AMBA-3"},
		},
		{
			name:  "empty tickets",
			input: `{"tickets": []}`,
			want:  nil,
		},
		{
			name:  "invalid JSON",
			input: `not json at all`,
			want:  nil,
		},
		{
			name:  "result with no tickets key",
			input: `{"result": "{\"something\": \"else\"}"}`,
			want:  nil,
		},
		{
			name:  "single ticket",
			input: `{"tickets": ["AMBA-99"]}`,
			want:  []string{"AMBA-99"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseLinearTickets(tt.input)
			if len(got) != len(tt.want) {
				t.Fatalf("got %d tickets, want %d: %v", len(got), len(tt.want), got)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("ticket %d: got %q, want %q", i, got[i], tt.want[i])
				}
			}
		})
	}
}
