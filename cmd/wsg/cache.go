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
	return parseCacheLines(string(data)), nil
}

func parseCacheLines(s string) []CacheEntry {
	var entries []CacheEntry
	for _, line := range strings.Split(strings.TrimSpace(s), "\n") {
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "\t", 2)
		if len(parts) != 2 {
			continue
		}
		entries = append(entries, CacheEntry{Name: parts[0], Path: parts[1]})
	}
	return entries
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

func cacheHas(entries []CacheEntry, name string) bool {
	for _, e := range entries {
		if e.Name == name {
			return true
		}
	}
	return false
}

func cacheFindPath(entries []CacheEntry, name string) string {
	for _, e := range entries {
		if e.Name == name {
			return e.Path
		}
	}
	return ""
}

func cacheAddEntry(file string, name string, path string) error {
	entries, err := cacheRead(file)
	if err != nil {
		return err
	}
	if cacheHas(entries, name) {
		return nil
	}
	entries = append(entries, CacheEntry{Name: name, Path: path})
	return cacheWrite(file, entries)
}

func cacheRemoveEntry(file string, name string) error {
	entries, err := cacheRead(file)
	if err != nil {
		return err
	}
	var filtered []CacheEntry
	for _, e := range entries {
		if e.Name != name {
			filtered = append(filtered, e)
		}
	}
	return cacheWrite(file, filtered)
}

func cacheRebuild(r *RepoContext) ([]CacheEntry, error) {
	output, err := run(r.Root, "jj", "workspace", "list")
	if err != nil {
		return nil, fmt.Errorf("jj workspace list: %w", err)
	}

	var entries []CacheEntry
	hasDefault := false
	for _, line := range strings.Split(output, "\n") {
		if line == "" {
			continue
		}
		name := strings.TrimSpace(strings.SplitN(line, ":", 2)[0])
		if name == "" {
			continue
		}
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
	cf := r.cacheFile()
	entries, err := cacheRead(cf)
	if err != nil {
		return nil, err
	}
	if entries != nil {
		return entries, nil
	}
	return cacheRebuild(r)
}
