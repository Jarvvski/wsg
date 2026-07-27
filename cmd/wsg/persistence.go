package main

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"syscall"
)

var errStateConflict = errors.New("persisted state changed")

type stateRevision struct {
	exists bool
	digest [sha256.Size]byte
}

type stateTemporary interface {
	Name() string
	Chmod(os.FileMode) error
	Write([]byte) (int, error)
	Sync() error
	Close() error
}

var createStateTemporary = func(parent, pattern string) (stateTemporary, error) {
	return os.CreateTemp(parent, pattern)
}

var renameStateFile = os.Rename

func marshalWithExtras(known any, extras map[string]json.RawMessage) ([]byte, error) {
	data, err := json.Marshal(known)
	if err != nil {
		return nil, err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return nil, err
	}
	for name, value := range extras {
		if _, exists := fields[name]; !exists {
			fields[name] = value
		}
	}
	return json.Marshal(fields)
}

func unmarshalWithExtras(data []byte, known any, knownNames ...string) (map[string]json.RawMessage, error) {
	if err := json.Unmarshal(data, known); err != nil {
		return nil, err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return nil, err
	}
	for _, name := range knownNames {
		delete(fields, name)
	}
	return fields, nil
}

func revisionOf(data []byte) stateRevision {
	return stateRevision{exists: true, digest: sha256.Sum256(data)}
}

func loadJSONState(path string, value any) (stateRevision, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return stateRevision{}, err
	}
	if err := json.Unmarshal(data, value); err != nil {
		return stateRevision{}, fmt.Errorf("decode %s: %w", path, err)
	}
	return revisionOf(data), nil
}

func currentRevision(path string) (stateRevision, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return stateRevision{}, nil
	}
	if err != nil {
		return stateRevision{}, err
	}
	return revisionOf(data), nil
}

func fileExistsForPersistence(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func withStateLock(lockPath string, fn func() error) error {
	if err := os.MkdirAll(filepath.Dir(lockPath), 0755); err != nil {
		return fmt.Errorf("create lock directory for %s: %w", lockPath, err)
	}
	lock, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0644)
	if err != nil {
		return fmt.Errorf("open state lock %s: %w", lockPath, err)
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return fmt.Errorf("lock state %s: %w", lockPath, err)
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	return fn()
}

func withStateLocks(lockPaths []string, fn func() error) error {
	paths := append([]string(nil), lockPaths...)
	sort.Strings(paths)
	unique := paths[:0]
	for _, path := range paths {
		if len(unique) == 0 || unique[len(unique)-1] != path {
			unique = append(unique, path)
		}
	}
	paths = unique
	var lockNext func(int) error
	lockNext = func(index int) error {
		if index == len(paths) {
			return fn()
		}
		return withStateLock(paths[index], func() error { return lockNext(index + 1) })
	}
	return lockNext(0)
}

func commitJSONState(path, lockPath string, expected stateRevision, value any) (stateRevision, error) {
	var committed stateRevision
	err := withStateLock(lockPath, func() error {
		current, err := currentRevision(path)
		if err != nil {
			return fmt.Errorf("reload %s: %w", path, err)
		}
		if current != expected {
			return fmt.Errorf("commit %s: %w", path, errStateConflict)
		}
		if err := writeJSONAtomic(path, value); err != nil {
			return err
		}
		committed, err = currentRevision(path)
		return err
	})
	return committed, err
}

func writeJSONAtomic(path string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return fmt.Errorf("serialize %s: %w", path, err)
	}
	data = append(data, '\n')
	parent := filepath.Dir(path)
	if err := os.MkdirAll(parent, 0755); err != nil {
		return fmt.Errorf("create state directory %s: %w", parent, err)
	}
	temporary, err := createStateTemporary(parent, "."+filepath.Base(path)+".*.tmp")
	if err != nil {
		return fmt.Errorf("create temporary state for %s: %w", path, err)
	}
	temporaryPath := temporary.Name()
	closed := false
	defer func() {
		if !closed {
			_ = temporary.Close()
		}
		_ = os.Remove(temporaryPath)
	}()
	if err := temporary.Chmod(0644); err != nil {
		return fmt.Errorf("set temporary state mode for %s: %w", path, err)
	}
	written, err := temporary.Write(data)
	if err != nil {
		return fmt.Errorf("write temporary state for %s: %w", path, err)
	}
	if written != len(data) {
		return fmt.Errorf("write temporary state for %s: %w", path, io.ErrShortWrite)
	}
	if err := temporary.Sync(); err != nil {
		return fmt.Errorf("sync temporary state for %s: %w", path, err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close temporary state for %s: %w", path, err)
	}
	closed = true
	if err := renameStateFile(temporaryPath, path); err != nil {
		return fmt.Errorf("replace state %s: %w", path, err)
	}
	return nil
}
