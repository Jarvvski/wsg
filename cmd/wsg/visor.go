package main

import (
	"fmt"
	"os"
	"strings"
)

// Visor controls a running kitty terminal via its remote-control socket.
// One Visor binds to one kitty instance, located via openVisor.
type Visor struct {
	socket string // "--to=unix:/tmp/kitty-visor-<id>"
}

// openVisor finds a kitty-visor socket under /tmp. The error text is
// user-facing - cmdMount fatal()s with it verbatim.
func openVisor() (*Visor, error) {
	entries, err := os.ReadDir("/tmp")
	if err == nil {
		for _, e := range entries {
			if strings.HasPrefix(e.Name(), "kitty-visor-") {
				return &Visor{socket: "--to=unix:/tmp/" + e.Name()}, nil
			}
		}
	}
	return nil, fmt.Errorf("No kitty visor socket found. Is kitty running?")
}

// NewTab opens a new tab running shellCmd via zsh, with title and cwd, and
// returns the new window's id.
func (v *Visor) NewTab(title, cwd, shellCmd string) (string, error) {
	return v.kitten("launch", "--type=tab",
		"--tab-title", title,
		"--cwd="+cwd,
		"--", "zsh", "-ic", shellCmd)
}

// SplitRight runs shellCmd in a new pane to the right of parentWinID and
// returns the new window's id.
func (v *Visor) SplitRight(parentWinID, cwd, shellCmd string) (string, error) {
	return v.kitten("launch", "--match", "id:"+parentWinID,
		"--location=vsplit",
		"--cwd="+cwd,
		"--", "zsh", "-ic", shellCmd)
}

// SplitDown runs shellCmd in a new pane below parentWinID and returns the
// new window's id.
func (v *Visor) SplitDown(parentWinID, cwd, shellCmd string) (string, error) {
	return v.kitten("launch", "--match", "id:"+parentWinID,
		"--location=hsplit",
		"--cwd="+cwd,
		"--", "zsh", "-ic", shellCmd)
}

// Focus brings the window with winID to the front.
func (v *Visor) Focus(winID string) error {
	_, err := v.kitten("focus-window", "--match", "id:"+winID)
	return err
}

func (v *Visor) kitten(args ...string) (string, error) {
	full := append([]string{"@", v.socket}, args...)
	return run("", "kitten", full...)
}
