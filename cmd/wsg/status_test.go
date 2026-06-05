package main

import (
	"encoding/json"
	"testing"
)

func TestWorkerStatusJSONWire(t *testing.T) {
	// Wire compat with jj-wsx: WorkerStatus must marshal as the bare
	// lowercase string the .jj/pool/worker-N.json files have always used.
	cases := []struct {
		s    WorkerStatus
		want string
	}{
		{WorkerStatusIdle, `"idle"`},
		{WorkerStatusBusy, `"busy"`},
		{WorkerStatusDone, `"done"`},
		{WorkerStatusFailed, `"failed"`},
	}
	for _, tt := range cases {
		got, err := json.Marshal(tt.s)
		if err != nil {
			t.Fatalf("Marshal(%q): %v", tt.s, err)
		}
		if string(got) != tt.want {
			t.Errorf("Marshal(%q) = %s, want %s", tt.s, got, tt.want)
		}
		var back WorkerStatus
		if err := json.Unmarshal([]byte(tt.want), &back); err != nil {
			t.Fatalf("Unmarshal(%s): %v", tt.want, err)
		}
		if back != tt.s {
			t.Errorf("Unmarshal(%s) = %q, want %q", tt.want, back, tt.s)
		}
	}
}

func TestSubIssueStatusJSONWire(t *testing.T) {
	cases := []struct {
		s    SubIssueStatus
		want string
	}{
		{SubIssueStatusPending, `"pending"`},
		{SubIssueStatusDispatched, `"dispatched"`},
		{SubIssueStatusDone, `"done"`},
		{SubIssueStatusFailed, `"failed"`},
		{SubIssueStatusSkipped, `"skipped"`},
	}
	for _, tt := range cases {
		got, err := json.Marshal(tt.s)
		if err != nil {
			t.Fatalf("Marshal(%q): %v", tt.s, err)
		}
		if string(got) != tt.want {
			t.Errorf("Marshal(%q) = %s, want %s", tt.s, got, tt.want)
		}
		var back SubIssueStatus
		if err := json.Unmarshal([]byte(tt.want), &back); err != nil {
			t.Fatalf("Unmarshal(%s): %v", tt.want, err)
		}
		if back != tt.s {
			t.Errorf("Unmarshal(%s) = %q, want %q", tt.want, back, tt.s)
		}
	}
}

func TestWorkerStatusPredicates(t *testing.T) {
	cases := []struct {
		s         WorkerStatus
		terminal  bool
		active    bool
		removable bool
	}{
		{WorkerStatusIdle, false, false, true},
		{WorkerStatusBusy, false, true, false},
		{WorkerStatusDone, true, false, true},
		{WorkerStatusFailed, true, false, true},
	}
	for _, tt := range cases {
		if got := tt.s.IsTerminal(); got != tt.terminal {
			t.Errorf("%q.IsTerminal() = %v, want %v", tt.s, got, tt.terminal)
		}
		if got := tt.s.IsActive(); got != tt.active {
			t.Errorf("%q.IsActive() = %v, want %v", tt.s, got, tt.active)
		}
		if got := tt.s.IsRemovable(); got != tt.removable {
			t.Errorf("%q.IsRemovable() = %v, want %v", tt.s, got, tt.removable)
		}
	}
}

func TestSubIssueStatusPredicates(t *testing.T) {
	cases := []struct {
		s        SubIssueStatus
		terminal bool
		active   bool
		unblocks bool
	}{
		{SubIssueStatusPending, false, true, false},
		{SubIssueStatusDispatched, false, true, false},
		{SubIssueStatusDone, true, false, true},
		{SubIssueStatusFailed, true, false, false},
		{SubIssueStatusSkipped, true, false, true},
	}
	for _, tt := range cases {
		if got := tt.s.IsTerminal(); got != tt.terminal {
			t.Errorf("%q.IsTerminal() = %v, want %v", tt.s, got, tt.terminal)
		}
		if got := tt.s.IsActive(); got != tt.active {
			t.Errorf("%q.IsActive() = %v, want %v", tt.s, got, tt.active)
		}
		if got := tt.s.Unblocks(); got != tt.unblocks {
			t.Errorf("%q.Unblocks() = %v, want %v", tt.s, got, tt.unblocks)
		}
	}
}
