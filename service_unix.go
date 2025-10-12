//go:build !windows

package main

import "os/exec"

// configureCmdWindows is a no-op on non-Windows platforms
func configureCmdWindows(cmd *exec.Cmd) {
	// No-op on Unix-like systems
}
