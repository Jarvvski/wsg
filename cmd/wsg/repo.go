package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type RepoContext struct {
	Root    string
	BaseDir string
}

func findJJDir() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for dir != "/" {
		if fi, err := os.Stat(filepath.Join(dir, ".jj")); err == nil && fi.IsDir() {
			return dir, nil
		}
		dir = filepath.Dir(dir)
	}
	return "", fmt.Errorf("not in a jj repo")
}

func repoRoot() (string, error) {
	wsRoot, err := findJJDir()
	if err != nil {
		return "", err
	}
	repoFile := filepath.Join(wsRoot, ".jj", "repo")
	fi, err := os.Stat(repoFile)
	if err != nil || fi.IsDir() {
		return wsRoot, nil
	}
	data, err := os.ReadFile(repoFile)
	if err != nil {
		return wsRoot, nil
	}
	target := strings.TrimSpace(string(data))
	if !filepath.IsAbs(target) {
		target = filepath.Join(wsRoot, ".jj", target)
	}
	resolved, err := filepath.EvalSymlinks(target)
	if err != nil {
		resolved = target
	}
	return filepath.Dir(filepath.Dir(resolved)), nil
}

func baseDir(root string) string {
	if env := os.Getenv("JJ_WS_DIR"); env != "" {
		if filepath.IsAbs(env) {
			return env
		}
		return filepath.Join(root, env)
	}
	return filepath.Join(filepath.Dir(root), filepath.Base(root)+"-workspaces")
}

func newRepoContext() (*RepoContext, error) {
	root, err := repoRoot()
	if err != nil {
		return nil, err
	}
	return &RepoContext{
		Root:    root,
		BaseDir: baseDir(root),
	}, nil
}

func (r *RepoContext) cacheFile() string {
	return filepath.Join(r.Root, ".jj", "ws-cache")
}

func (r *RepoContext) poolDir() string {
	return filepath.Join(r.Root, ".jj", "pool")
}

func (r *RepoContext) poolConfigFile() string {
	return filepath.Join(r.Root, ".jj", "pool.json")
}

func (r *RepoContext) workerStateFile(worker string) string {
	return filepath.Join(r.poolDir(), worker+".json")
}

func (r *RepoContext) workerDir(worker string) string {
	return filepath.Join(r.BaseDir, worker)
}

func displayWorker(name string) string {
	return strings.TrimPrefix(name, "worker-")
}

func resolveWorker(input string) string {
	if strings.HasPrefix(input, "worker-") {
		return input
	}
	return "worker-" + input
}
