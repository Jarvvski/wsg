package main

import (
	"testing"
)

func TestColorize(t *testing.T) {
	origTTY := isTTY

	// When not a TTY, no color codes
	isTTY = false
	got := colorize("done", colorGreen)
	if got != "done" {
		t.Errorf("non-TTY: got %q, want %q", got, "done")
	}

	// When TTY, wrap in color codes
	isTTY = true
	got = colorize("done", colorGreen)
	want := colorGreen + "done" + colorReset
	if got != want {
		t.Errorf("TTY: got %q, want %q", got, want)
	}

	isTTY = origTTY
}
