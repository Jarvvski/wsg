package main

import (
	"errors"
	"reflect"
	"testing"
	"time"
)

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

func TestValidateSubIssueEntries(t *testing.T) {
	tests := []struct {
		name    string
		parent  string
		entries map[string]linearSubIssueEntry
		want    map[string]linearSubIssueEntry
	}{
		{
			name:    "nil map",
			parent:  "AMBA-1",
			entries: nil,
			want:    nil,
		},
		{
			name:   "drops parent appearing as its own child",
			parent: "AMBA-1",
			entries: map[string]linearSubIssueEntry{
				"AMBA-1": {Title: "Parent", Status: "Backlog", BlockedBy: []string{}},
				"AMBA-2": {Title: "Child", Status: "Backlog", BlockedBy: []string{}},
			},
			want: map[string]linearSubIssueEntry{
				"AMBA-2": {Title: "Child", Status: "Backlog", BlockedBy: []string{}},
			},
		},
		{
			name:   "drops empty title",
			parent: "AMBA-1",
			entries: map[string]linearSubIssueEntry{
				"AMBA-2": {Title: "   ", Status: "Backlog", BlockedBy: []string{}},
				"AMBA-3": {Title: "Real", Status: "Backlog", BlockedBy: []string{}},
			},
			want: map[string]linearSubIssueEntry{
				"AMBA-3": {Title: "Real", Status: "Backlog", BlockedBy: []string{}},
			},
		},
		{
			name:   "drops empty status",
			parent: "AMBA-1",
			entries: map[string]linearSubIssueEntry{
				"AMBA-2": {Title: "Child", Status: "", BlockedBy: []string{}},
				"AMBA-3": {Title: "Real", Status: "Backlog", BlockedBy: []string{}},
			},
			want: map[string]linearSubIssueEntry{
				"AMBA-3": {Title: "Real", Status: "Backlog", BlockedBy: []string{}},
			},
		},
		{
			name:   "filters self-blockers",
			parent: "AMBA-1",
			entries: map[string]linearSubIssueEntry{
				"AMBA-2": {Title: "Self-blocked", Status: "Backlog", BlockedBy: []string{"AMBA-2", "AMBA-3"}},
				"AMBA-3": {Title: "Real", Status: "Backlog", BlockedBy: []string{}},
			},
			want: map[string]linearSubIssueEntry{
				"AMBA-2": {Title: "Self-blocked", Status: "Backlog", BlockedBy: []string{"AMBA-3"}},
				"AMBA-3": {Title: "Real", Status: "Backlog", BlockedBy: []string{}},
			},
		},
		{
			name:   "clean entries pass through",
			parent: "AMBA-1",
			entries: map[string]linearSubIssueEntry{
				"AMBA-2": {Title: "A", Status: "Backlog", BlockedBy: []string{}},
				"AMBA-3": {Title: "B", Status: "Todo", BlockedBy: []string{"AMBA-2"}},
			},
			want: map[string]linearSubIssueEntry{
				"AMBA-2": {Title: "A", Status: "Backlog", BlockedBy: []string{}},
				"AMBA-3": {Title: "B", Status: "Todo", BlockedBy: []string{"AMBA-2"}},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := validateSubIssueEntries(tt.entries, tt.parent)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("got %#v, want %#v", got, tt.want)
			}
		})
	}
}

func TestFetchSubIssueGraphRetry(t *testing.T) {
	prev := linearSubIssueGraphRetryDelay
	linearSubIssueGraphRetryDelay = time.Millisecond
	t.Cleanup(func() { linearSubIssueGraphRetryDelay = prev })

	const happy = `{"sub_issues": {"AMBA-2": {"title": "T", "status": "Backlog", "blocked_by": []}}}`

	t.Run("first call succeeds: no retry", func(t *testing.T) {
		calls := 0
		query := func(string) (string, error) {
			calls++
			return happy, nil
		}
		got, err := fetchSubIssueGraph("AMBA-1", "p", query)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if calls != 1 {
			t.Errorf("expected 1 call, got %d", calls)
		}
		if _, ok := got["AMBA-2"]; !ok {
			t.Errorf("expected AMBA-2 in result, got %v", got)
		}
	})

	t.Run("transient query error then success", func(t *testing.T) {
		calls := 0
		query := func(string) (string, error) {
			calls++
			if calls == 1 {
				return "", errors.New("network blip")
			}
			return happy, nil
		}
		got, err := fetchSubIssueGraph("AMBA-1", "p", query)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if calls != 2 {
			t.Errorf("expected 2 calls, got %d", calls)
		}
		if _, ok := got["AMBA-2"]; !ok {
			t.Errorf("expected AMBA-2 in result, got %v", got)
		}
	})

	t.Run("malformed JSON then success", func(t *testing.T) {
		calls := 0
		query := func(string) (string, error) {
			calls++
			if calls == 1 {
				return "not json", nil
			}
			return happy, nil
		}
		got, err := fetchSubIssueGraph("AMBA-1", "p", query)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if calls != 2 {
			t.Errorf("expected 2 calls, got %d", calls)
		}
		if len(got) != 1 {
			t.Errorf("expected 1 entry, got %d: %v", len(got), got)
		}
	})

	t.Run("both calls fail returns error", func(t *testing.T) {
		calls := 0
		query := func(string) (string, error) {
			calls++
			return "", errors.New("MCP down")
		}
		_, err := fetchSubIssueGraph("AMBA-1", "p", query)
		if err == nil {
			t.Fatal("expected error after two failures")
		}
		if calls != 2 {
			t.Errorf("expected 2 calls, got %d", calls)
		}
	})

	t.Run("validates after fetch (drops parent)", func(t *testing.T) {
		query := func(string) (string, error) {
			return `{"sub_issues": {"AMBA-1": {"title": "Parent", "status": "Backlog", "blocked_by": []}, "AMBA-2": {"title": "Child", "status": "Backlog", "blocked_by": []}}}`, nil
		}
		got, err := fetchSubIssueGraph("AMBA-1", "p", query)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if _, ok := got["AMBA-1"]; ok {
			t.Errorf("expected parent AMBA-1 to be dropped, got %v", got)
		}
		if _, ok := got["AMBA-2"]; !ok {
			t.Errorf("expected AMBA-2 in result, got %v", got)
		}
	})
}
