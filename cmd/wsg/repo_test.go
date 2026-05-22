package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestBaseDir(t *testing.T) {
	got := baseDir("/home/user/projects/myrepo")
	want := "/home/user/projects/myrepo-workspaces"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestBaseDirEnvAbsolute(t *testing.T) {
	t.Setenv("JJ_WS_DIR", "/custom/workspaces")
	got := baseDir("/home/user/projects/myrepo")
	want := "/custom/workspaces"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestBaseDirEnvRelative(t *testing.T) {
	t.Setenv("JJ_WS_DIR", "my-workspaces")
	got := baseDir("/home/user/projects/myrepo")
	want := "/home/user/projects/myrepo/my-workspaces"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestRepoContextPaths(t *testing.T) {
	r := &RepoContext{Root: "/repo", BaseDir: "/repo-workspaces"}

	if got := r.cacheFile(); got != "/repo/.jj/ws-cache" {
		t.Errorf("cacheFile = %q", got)
	}
	if got := r.poolDir(); got != "/repo/.jj/pool" {
		t.Errorf("poolDir = %q", got)
	}
	if got := r.poolConfigFile(); got != "/repo/.jj/pool.json" {
		t.Errorf("poolConfigFile = %q", got)
	}
	if got := r.workerStateFile("worker-1"); got != "/repo/.jj/pool/worker-1.json" {
		t.Errorf("workerStateFile = %q", got)
	}
}

// resolveDir resolves symlinks so tests work on macOS where /var -> /private/var
func resolveDir(t *testing.T, dir string) string {
	t.Helper()
	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatalf("resolve %s: %v", dir, err)
	}
	return resolved
}

func TestFindJJDir(t *testing.T) {
	dir := resolveDir(t, t.TempDir())
	jjDir := filepath.Join(dir, "a", "b", ".jj")
	os.MkdirAll(jjDir, 0755)
	nested := filepath.Join(dir, "a", "b", "c", "d")
	os.MkdirAll(nested, 0755)

	orig, _ := os.Getwd()
	defer os.Chdir(orig)
	os.Chdir(nested)

	got, err := findJJDir()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := filepath.Join(dir, "a", "b")
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestRepoRootDirect(t *testing.T) {
	dir := resolveDir(t, t.TempDir())
	os.MkdirAll(filepath.Join(dir, ".jj", "repo"), 0755)

	orig, _ := os.Getwd()
	defer os.Chdir(orig)
	os.Chdir(dir)

	got, err := repoRoot()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != dir {
		t.Errorf("got %q, want %q", got, dir)
	}
}

func TestRepoRootSecondaryWorkspace(t *testing.T) {
	dir := resolveDir(t, t.TempDir())
	mainDir := filepath.Join(dir, "main")
	wsDir := filepath.Join(dir, "ws")
	os.MkdirAll(filepath.Join(mainDir, ".jj", "repo"), 0755)
	os.MkdirAll(filepath.Join(wsDir, ".jj"), 0755)

	target := filepath.Join(mainDir, ".jj", "repo")
	os.WriteFile(filepath.Join(wsDir, ".jj", "repo"), []byte(target), 0644)

	orig, _ := os.Getwd()
	defer os.Chdir(orig)
	os.Chdir(wsDir)

	got, err := repoRoot()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != mainDir {
		t.Errorf("got %q, want %q", got, mainDir)
	}
}
