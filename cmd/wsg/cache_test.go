package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseCacheLines(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  []CacheEntry
	}{
		{
			name:  "normal entries",
			input: "default\t/repo\nworker-1\t/ws/worker-1\n",
			want: []CacheEntry{
				{Name: "default", Path: "/repo"},
				{Name: "worker-1", Path: "/ws/worker-1"},
			},
		},
		{
			name:  "empty string",
			input: "",
			want:  nil,
		},
		{
			name:  "blank lines ignored",
			input: "default\t/repo\n\n\nworker-1\t/ws/worker-1\n",
			want: []CacheEntry{
				{Name: "default", Path: "/repo"},
				{Name: "worker-1", Path: "/ws/worker-1"},
			},
		},
		{
			name:  "no trailing newline",
			input: "default\t/repo",
			want:  []CacheEntry{{Name: "default", Path: "/repo"}},
		},
		{
			name:  "malformed line without tab",
			input: "default\t/repo\nbadline\nworker-1\t/ws/worker-1",
			want: []CacheEntry{
				{Name: "default", Path: "/repo"},
				{Name: "worker-1", Path: "/ws/worker-1"},
			},
		},
		{
			name:  "path with spaces",
			input: "my-ws\t/path/with spaces/ws\n",
			want:  []CacheEntry{{Name: "my-ws", Path: "/path/with spaces/ws"}},
		},
		{
			name:  "tab in path preserved",
			input: "ws\t/a\tb\n",
			want:  []CacheEntry{{Name: "ws", Path: "/a\tb"}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseCacheLines(tt.input)
			if len(got) != len(tt.want) {
				t.Fatalf("got %d entries, want %d", len(got), len(tt.want))
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("entry %d: got %+v, want %+v", i, got[i], tt.want[i])
				}
			}
		})
	}
}

func TestCacheHas(t *testing.T) {
	entries := []CacheEntry{
		{Name: "default", Path: "/repo"},
		{Name: "worker-1", Path: "/ws/worker-1"},
	}
	if !cacheHas(entries, "default") {
		t.Error("expected default to be found")
	}
	if !cacheHas(entries, "worker-1") {
		t.Error("expected worker-1 to be found")
	}
	if cacheHas(entries, "worker-2") {
		t.Error("expected worker-2 to not be found")
	}
	if cacheHas(nil, "anything") {
		t.Error("expected nil slice to return false")
	}
}

func TestCacheFindPath(t *testing.T) {
	entries := []CacheEntry{
		{Name: "default", Path: "/repo"},
		{Name: "worker-1", Path: "/ws/worker-1"},
	}
	if got := cacheFindPath(entries, "worker-1"); got != "/ws/worker-1" {
		t.Errorf("got %q, want /ws/worker-1", got)
	}
	if got := cacheFindPath(entries, "missing"); got != "" {
		t.Errorf("got %q, want empty", got)
	}
}

func TestCacheReadWrite(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "ws-cache")

	entries := []CacheEntry{
		{Name: "default", Path: "/repo"},
		{Name: "worker-1", Path: "/ws/worker-1"},
		{Name: "worker-2", Path: "/ws/worker-2"},
	}
	if err := cacheWrite(file, entries); err != nil {
		t.Fatalf("write: %v", err)
	}

	got, err := cacheRead(file)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(got) != len(entries) {
		t.Fatalf("got %d entries, want %d", len(got), len(entries))
	}
	for i := range got {
		if got[i] != entries[i] {
			t.Errorf("entry %d: got %+v, want %+v", i, got[i], entries[i])
		}
	}

	// Verify raw format is tab-separated
	raw, _ := os.ReadFile(file)
	want := "default\t/repo\nworker-1\t/ws/worker-1\nworker-2\t/ws/worker-2\n"
	if string(raw) != want {
		t.Errorf("raw file:\ngot:  %q\nwant: %q", string(raw), want)
	}
}

func TestCacheReadMissing(t *testing.T) {
	got, err := cacheRead("/nonexistent/path")
	if err != nil {
		t.Fatalf("expected nil error for missing file, got %v", err)
	}
	if got != nil {
		t.Fatalf("expected nil entries, got %v", got)
	}
}

func TestCacheAddEntry(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "ws-cache")

	cacheWrite(file, []CacheEntry{{Name: "default", Path: "/repo"}})

	if err := cacheAddEntry(file, "worker-1", "/ws/worker-1"); err != nil {
		t.Fatalf("add: %v", err)
	}
	got, _ := cacheRead(file)
	if len(got) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(got))
	}

	// Adding duplicate is a no-op
	if err := cacheAddEntry(file, "worker-1", "/ws/worker-1"); err != nil {
		t.Fatalf("add duplicate: %v", err)
	}
	got, _ = cacheRead(file)
	if len(got) != 2 {
		t.Fatalf("expected 2 entries after duplicate add, got %d", len(got))
	}
}

func TestCacheRemoveEntry(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "ws-cache")

	cacheWrite(file, []CacheEntry{
		{Name: "default", Path: "/repo"},
		{Name: "worker-1", Path: "/ws/worker-1"},
		{Name: "worker-2", Path: "/ws/worker-2"},
	})

	if err := cacheRemoveEntry(file, "worker-1"); err != nil {
		t.Fatalf("remove: %v", err)
	}
	got, _ := cacheRead(file)
	if len(got) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(got))
	}
	if cacheHas(got, "worker-1") {
		t.Error("worker-1 should have been removed")
	}

	// Removing nonexistent is a no-op
	if err := cacheRemoveEntry(file, "worker-99"); err != nil {
		t.Fatalf("remove nonexistent: %v", err)
	}
}
