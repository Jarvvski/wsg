package main

import (
	"bufio"
	"io"
	"os"
	"strings"
	"time"
)

func streamLogs(path string) {
	f, err := os.Open(path)
	if err != nil {
		fatal("Cannot open %s: %v", path, err)
	}
	defer f.Close()

	reader := bufio.NewReader(f)
	state := &logState{seen: make(map[string]bool)}

	for {
		line, err := reader.ReadString('\n')
		if line != "" {
			line = strings.TrimSpace(line)
			if line != "" {
				formatEvent(line, state)
			}
		}
		if err != nil {
			if err == io.EOF {
				time.Sleep(200 * time.Millisecond)
				continue
			}
			return
		}
	}
}

func readLogTail(path string, offset int64) ([]string, int64) {
	f, err := os.Open(path)
	if err != nil {
		return nil, offset
	}
	defer f.Close()

	if offset > 0 {
		f.Seek(offset, io.SeekStart)
	}

	var lines []string
	state := &logState{seen: make(map[string]bool)}
	reader := bufio.NewReader(f)
	for {
		line, err := reader.ReadString('\n')
		if line != "" {
			line = strings.TrimSpace(line)
			if line != "" {
				formatted := formatEventToString(line, state)
				if formatted != "" {
					lines = append(lines, formatted)
				}
			}
		}
		if err != nil {
			break
		}
	}

	newOffset, _ := f.Seek(0, io.SeekCurrent)
	return lines, newOffset
}
