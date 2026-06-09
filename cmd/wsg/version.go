package main

import "fmt"

// Version is the semver string for wsg. Bumped per landed change:
//   - PATCH for bug fixes (CHANGELOG: ### Fixed only)
//   - MINOR for everything else (### Added / ### Changed / ### Removed)
//   - MAJOR (1.0.0+) only with explicit owner approval - never auto-bump
const Version = "0.4.0"

func cmdVersion() {
	fmt.Printf("wsg %s\n", Version)
}
