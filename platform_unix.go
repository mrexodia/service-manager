//go:build !windows

package main

import (
	"syscall"
)

// platformStartProcess starts the process in a new process group.
// This allows us to kill the entire process tree (parent + all children)
// by sending signals to the process group.
func platformStartProcess(s *Service) error {
	// Set SysProcAttr to create a new process group.
	// This is the Unix equivalent of Windows Job Objects.
	s.cmd.SysProcAttr = &syscall.SysProcAttr{
		Setpgid: true,
	}
	return s.cmd.Start()
}

func platformCleanup(s *Service) {}
