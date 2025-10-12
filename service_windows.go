//go:build windows

package main

import (
	"os/exec"
	"syscall"
)

// configureCmdWindows sets Windows-specific process attributes to hide console windows
func configureCmdWindows(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: 0x08000000, // CREATE_NO_WINDOW
	}
}
