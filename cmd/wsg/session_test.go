package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestExtractSessionID(t *testing.T) {
	dir := t.TempDir()
	logFile := filepath.Join(dir, "worker.log")

	content := `{"type":"system","subtype":"status","status":null}
{"type":"rate_limit_event","rate_limit_info":{}}
{"type":"system","subtype":"init","session_id":"e046ef61-7c94-48cc-9852-c3e98adae73a","cwd":"/repo"}
{"type":"assistant","message":{"content":[{"type":"text","text":"Hello"}]}}
`
	os.WriteFile(logFile, []byte(content), 0644)

	sid, err := extractSessionID(logFile)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sid != "e046ef61-7c94-48cc-9852-c3e98adae73a" {
		t.Errorf("got %q", sid)
	}
}

func TestExtractSessionIDMissing(t *testing.T) {
	dir := t.TempDir()
	logFile := filepath.Join(dir, "worker.log")

	os.WriteFile(logFile, []byte(`{"type":"assistant","message":{}}`+"\n"), 0644)

	_, err := extractSessionID(logFile)
	if err == nil {
		t.Fatal("expected error for missing session ID")
	}
}

func TestExtractSessionIDPlainText(t *testing.T) {
	dir := t.TempDir()
	logFile := filepath.Join(dir, "worker.log")

	os.WriteFile(logFile, []byte("This is plain text output\nnot json\n"), 0644)

	_, err := extractSessionID(logFile)
	if err == nil {
		t.Fatal("expected error for plain text log")
	}
}

func TestResolveSession(t *testing.T) {
	dir := t.TempDir()

	resumable := filepath.Join(dir, "resumable.log")
	os.WriteFile(resumable, []byte(`{"type":"system","subtype":"init","session_id":"abc-123","cwd":"/repo"}`+"\n"), 0644)

	noSession := filepath.Join(dir, "no_session.log")
	os.WriteFile(noSession, []byte(`{"type":"assistant","message":{}}`+"\n"), 0644)

	plainText := filepath.Join(dir, "plain.log")
	os.WriteFile(plainText, []byte("this is not json\n"), 0644)

	empty := ""

	cases := []struct {
		name        string
		ws          *WorkerState
		wantID      string
		wantReason  string
		wantResumed bool
	}{
		{
			name:        "log carries session id",
			ws:          &WorkerState{LogFile: &resumable},
			wantID:      "abc-123",
			wantResumed: true,
		},
		{
			name:       "no log file pointer",
			ws:         &WorkerState{LogFile: nil},
			wantReason: "no prior session log",
		},
		{
			name:       "empty log file path",
			ws:         &WorkerState{LogFile: &empty},
			wantReason: "no prior session log",
		},
		{
			name:       "log has no session id",
			ws:         &WorkerState{LogFile: &noSession},
			wantReason: "log has no session id yet",
		},
		{
			name:       "log unreadable as json",
			ws:         &WorkerState{LogFile: &plainText},
			wantReason: "log has no session id yet",
		},
		{
			name:       "log file missing on disk",
			ws:         &WorkerState{LogFile: strPtr(filepath.Join(dir, "missing.log"))},
			wantReason: "log file unreadable",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			sid, reason := resolveSession(c.ws)
			out := ResumeOutcome{SessionID: sid, Reason: reason}
			if out.Resumed() != c.wantResumed {
				t.Fatalf("Resumed() = %v, want %v (sid=%q reason=%q)", out.Resumed(), c.wantResumed, sid, reason)
			}
			if sid != c.wantID {
				t.Errorf("session id = %q, want %q", sid, c.wantID)
			}
			if c.wantReason != "" && !strings.HasPrefix(reason, c.wantReason) {
				t.Errorf("reason = %q, want prefix %q", reason, c.wantReason)
			}
			if c.wantReason == "" && reason != "" {
				t.Errorf("expected empty reason, got %q", reason)
			}
		})
	}
}

func TestResumeBadge(t *testing.T) {
	cases := []struct {
		name string
		out  ResumeOutcome
		want string
	}{
		{"resumed", ResumeOutcome{SessionID: "abc"}, "(resumed)"},
		{"fresh with reason", ResumeOutcome{Reason: "no prior session log"}, "(fresh: no prior session log)"},
		{"zero value", ResumeOutcome{}, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := resumeBadge(c.out); got != c.want {
				t.Errorf("resumeBadge = %q, want %q", got, c.want)
			}
		})
	}
}
