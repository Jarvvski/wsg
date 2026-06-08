package main

import (
	"fmt"
	"os"
	"strings"
)

type CacheEntry struct {
	Name string
	Path string
}

func cacheRead(file string) ([]CacheEntry, error) {
	data, err := os.ReadFile(file)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var entries []CacheEntry
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "\t", 2)
		if len(parts) != 2 {
			continue
		}
		entries = append(entries, CacheEntry{Name: parts[0], Path: parts[1]})
	}
	return entries, nil
}

func cacheWrite(file string, entries []CacheEntry) error {
	var b strings.Builder
	for _, e := range entries {
		fmt.Fprintf(&b, "%s\t%s\n", e.Name, e.Path)
	}
	tmp := file + ".tmp"
	if err := os.WriteFile(tmp, []byte(b.String()), 0644); err != nil {
		return err
	}
	return os.Rename(tmp, file)
}

func cacheRebuild(r *RepoContext) ([]CacheEntry, error) {
	names, err := jjListWorkspaceNames(r.Root)
	if err != nil {
		return nil, fmt.Errorf("jj workspace list: %w", err)
	}

	var entries []CacheEntry
	hasDefault := false
	for _, name := range names {
		var wspath string
		if name == "default" {
			wspath = r.Root
			hasDefault = true
		} else {
			wspath = r.BaseDir + "/" + name
		}
		entries = append(entries, CacheEntry{Name: name, Path: wspath})
	}
	if !hasDefault {
		entries = append([]CacheEntry{{Name: "default", Path: r.Root}}, entries...)
	}

	if err := cacheWrite(r.cacheFile(), entries); err != nil {
		return entries, err
	}
	return entries, nil
}

func cacheGet(r *RepoContext) ([]CacheEntry, error) {
	entries, err := cacheRead(r.cacheFile())
	if err != nil {
		return nil, err
	}
	if entries != nil {
		return entries, nil
	}
	return cacheRebuild(r)
}
