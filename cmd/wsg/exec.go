package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"
)

func run(dir string, name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("%s %s: %w\n%s", name, strings.Join(args, " "), err, stderr.String())
	}
	return strings.TrimSpace(stdout.String()), nil
}

func runCapture(dir string, name string, args ...string) (stdout string, stderr string, err error) {
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	var outBuf, errBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf
	err = cmd.Run()
	return strings.TrimSpace(outBuf.String()), strings.TrimSpace(errBuf.String()), err
}

func runCtx(ctx context.Context, dir string, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("%s %s: %w\n%s", name, strings.Join(args, " "), err, stderr.String())
	}
	return strings.TrimSpace(stdout.String()), nil
}

func runPassthrough(dir string, name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func startBackground(dir string, logFile string, name string, args ...string) (int, error) {
	// Build a shell command so the redirect is owned by the child shell,
	// not a Go file handle that closes when wsg exits.
	var quoted []string
	for _, a := range append([]string{name}, args...) {
		quoted = append(quoted, shellQuote(a))
	}
	shellCmd := "exec " + strings.Join(quoted, " ") + " > " + shellQuote(logFile) + " 2>&1"
	cmd := exec.Command("sh", "-c", shellCmd)
	cmd.Dir = dir
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		return 0, fmt.Errorf("start %s: %w", name, err)
	}
	go cmd.Wait()
	return cmd.Process.Pid, nil
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}

func startForeground(dir string, logFile string, name string, args ...string) (int, error) {
	f, err := os.Create(logFile)
	if err != nil {
		return 0, fmt.Errorf("create log %s: %w", logFile, err)
	}
	defer f.Close()
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	cmd.Stdin = os.Stdin
	cmd.Stdout = io.MultiWriter(os.Stdout, f)
	cmd.Stderr = io.MultiWriter(os.Stderr, f)
	err = cmd.Run()
	exitCode := 0
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else {
			return 1, err
		}
	}
	return exitCode, nil
}

func waitForProcess(pid int) {
	proc, err := os.FindProcess(pid)
	if err != nil {
		return
	}
	proc.Wait()
}

func claudeQuery(dir, prompt, allowedTools string) (string, error) {
	output, stderr, err := runCapture(dir, "claude", "-p",
		"--model", "haiku",
		"--output-format", "json",
		"--no-session-persistence",
		"--allowedTools="+allowedTools,
		prompt,
	)
	if err != nil {
		diag := stderr
		if diag == "" {
			diag = output
		}
		return "", fmt.Errorf("claude failed: %s", diag)
	}
	return unwrapClaudeJSON(output), nil
}

func unwrapClaudeJSON(output string) string {
	var wrapper struct {
		Result string `json:"result"`
	}
	if err := json.Unmarshal([]byte(output), &wrapper); err == nil && wrapper.Result != "" {
		output = wrapper.Result
	}
	return extractJSON(output)
}

func processAlive(pid int) bool {
	return syscall.Kill(pid, 0) == nil
}

func killProcess(pid int) {
	// Kill the process group (negative PID) so child processes also die
	syscall.Kill(-pid, syscall.SIGTERM)
	time.Sleep(time.Second)
	if processAlive(pid) {
		syscall.Kill(-pid, syscall.SIGKILL)
	}
}
