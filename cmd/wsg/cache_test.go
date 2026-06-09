package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCacheRead(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want []CacheEntry
	}{
		{
			name: "normal entries",
			raw:  "default\t/repo\nworker-1\t/ws/worker-1\n",
			want: []CacheEntry{
				{Name: "default", Path: "/repo"},
				{Name: "worker-1", Path: "/ws/worker-1"},
			},
		},
		{
			name: "empty string",
			raw:  "",
			want: nil,
		},
		{
			name: "blank lines ignored",
			raw:  "default\t/repo\n\n\nworker-1\t/ws/worker-1\n",
			want: []CacheEntry{
				{Name: "default", Path: "/repo"},
				{Name: "worker-1", Path: "/ws/worker-1"},
			},
		},
		{
			name: "no trailing newline",
			raw:  "default\t/repo",
			want: []CacheEntry{{Name: "default", Path: "/repo"}},
		},
		{
			name: "malformed line without tab",
			raw:  "default\t/repo\nbadline\nworker-1\t/ws/worker-1",
			want: []CacheEntry{
				{Name: "default", Path: "/repo"},
				{Name: "worker-1", Path: "/ws/worker-1"},
			},
		},
		{
			name: "path with spaces",
			raw:  "my-ws\t/path/with spaces/ws\n",
			want: []CacheEntry{{Name: "my-ws", Path: "/path/with spaces/ws"}},
		},
		{
			name: "tab in path preserved",
			raw:  "ws\t/a\tb\n",
			want: []CacheEntry{{Name: "ws", Path: "/a\tb"}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			file := filepath.Join(t.TempDir(), "ws-cache")
			if err := os.WriteFile(file, []byte(tt.raw), 0644); err != nil {
				t.Fatalf("seed file: %v", err)
			}
			got, err := cacheRead(file)
			if err != nil {
				t.Fatalf("read: %v", err)
			}
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

func TestCacheReadMissing(t *testing.T) {
	got, err := cacheRead("/nonexistent/path")
	if err != nil {
		t.Fatalf("expected nil error for missing file, got %v", err)
	}
	if got != nil {
		t.Fatalf("expected nil entries, got %v", got)
	}
}

func TestCacheWriteRoundTrip(t *testing.T) {
	file := filepath.Join(t.TempDir(), "ws-cache")
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

	raw, _ := os.ReadFile(file)
	want := "default\t/repo\nworker-1\t/ws/worker-1\nworker-2\t/ws/worker-2\n"
	if string(raw) != want {
		t.Errorf("raw file:\ngot:  %q\nwant: %q", string(raw), want)
	}
}

func TestCacheStale(t *testing.T) {
	dir := t.TempDir()
	opDir := filepath.Join(dir, ".jj", "repo", "op_heads", "heads")
	if err := os.MkdirAll(opDir, 0755); err != nil {
		t.Fatalf("seed op dir: %v", err)
	}
	r := &RepoContext{Root: dir, BaseDir: dir + "-workspaces"}

	if err := cacheWrite(r.cacheFile(), []CacheEntry{{Name: "default", Path: dir}}); err != nil {
		t.Fatalf("seed cache: %v", err)
	}

	past := time.Now().Add(-time.Hour)
	if err := os.Chtimes(opDir, past, past); err != nil {
		t.Fatalf("backdate op dir: %v", err)
	}
	if cacheStale(r) {
		t.Errorf("cache should be fresh when op dir is older")
	}

	future := time.Now().Add(time.Hour)
	if err := os.Chtimes(opDir, future, future); err != nil {
		t.Fatalf("forward op dir: %v", err)
	}
	if !cacheStale(r) {
		t.Errorf("cache should be stale when op dir is newer")
	}
}

func TestCacheStaleMissingOpDir(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".jj"), 0755); err != nil {
		t.Fatalf("seed .jj: %v", err)
	}
	r := &RepoContext{Root: dir, BaseDir: dir + "-workspaces"}
	if err := cacheWrite(r.cacheFile(), []CacheEntry{{Name: "default", Path: dir}}); err != nil {
		t.Fatalf("seed cache: %v", err)
	}
	if cacheStale(r) {
		t.Errorf("missing op dir should be treated as fresh, not stale")
	}
}

func TestCacheStaleMissingCache(t *testing.T) {
	dir := t.TempDir()
	r := &RepoContext{Root: dir, BaseDir: dir + "-workspaces"}
	if cacheStale(r) {
		t.Errorf("missing cache file should not be reported stale by cacheStale")
	}
}
