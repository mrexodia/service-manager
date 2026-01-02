//go:build !windows

package main

import (
	"fmt"
	"os/exec"
	"syscall"
	"time"
)

// configureCmdWindows is a no-op on non-Windows platforms
func configureCmdWindows(cmd *exec.Cmd) {
	// No-op on Unix-like systems
}

// platformStartProcess is defined in platform_unix.go

// gracefulStop attempts to gracefully stop a service process
// It sends SIGTERM first, waits for the timeout, then sends SIGKILL if needed
func gracefulStop(s *Service, timeout time.Duration) error {
	if s.cmd == nil || s.cmd.Process == nil {
		return fmt.Errorf("no process to stop")
	}

	pid := s.cmd.Process.Pid

	// Send SIGTERM for graceful shutdown
	if err := s.cmd.Process.Signal(syscall.SIGTERM); err != nil {
		// Process might have already exited, try SIGKILL as fallback
		return s.cmd.Process.Kill()
	}

	// Wait for process to exit gracefully
	done := make(chan error, 1)
	go func() {
		_, err := s.cmd.Process.Wait()
		done <- err
	}()

	select {
	case <-time.After(timeout):
		// Timeout - force kill
		fmt.Printf("Service %s (PID: %d) did not stop gracefully after %v, forcing kill\n",
			s.Config.Name, pid, timeout)
		if err := s.cmd.Process.Kill(); err != nil {
			return fmt.Errorf("failed to force kill process: %w", err)
		}
		// Wait for the kill to complete
		<-done
		return nil
	case err := <-done:
		// Process exited gracefully
		if err == nil {
			fmt.Printf("Service %s (PID: %d) stopped gracefully\n", s.Config.Name, pid)
		}
		return err
	}
}
