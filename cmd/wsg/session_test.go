package main

import (
	"os"
	"path/filepath"
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

func TestVisorSocket(t *testing.T) {
	// Just verify it doesn't panic - actual socket presence is environment-dependent
	result := visorSocket()
	_ = result
}
