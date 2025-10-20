//go:build windows

package main

import (
	"fmt"
	"os/exec"
	"syscall"
	"time"
)

// configureCmdWindows sets Windows-specific process attributes to hide console windows
func configureCmdWindows(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: 0x08000000, // CREATE_NO_WINDOW
	}
}

// gracefulStop attempts to gracefully stop a service process on Windows
// It sends CTRL_BREAK_EVENT first, waits for the timeout, then terminates if needed
func gracefulStop(s *Service, timeout time.Duration) error {
	if s.cmd == nil || s.cmd.Process == nil {
		return fmt.Errorf("no process to stop")
	}

	pid := s.cmd.Process.Pid

	// Try to send CTRL_BREAK_EVENT for graceful shutdown
	// Note: This only works if the process is in a process group
	// If it fails, we'll fall back to termination
	err := sendCtrlBreak(s.cmd.Process.Pid)
	if err != nil {
		// CTRL_BREAK failed, use direct termination with timeout
		fmt.Printf("Service %s (PID: %d): CTRL_BREAK not supported, using TerminateProcess\n",
			s.Config.Name, pid)
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
		fmt.Printf("Service %s (PID: %d) did not stop gracefully after %v, forcing termination\n",
			s.Config.Name, pid, timeout)
		if err := s.cmd.Process.Kill(); err != nil {
			return fmt.Errorf("failed to terminate process: %w", err)
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

// sendCtrlBreak sends a CTRL_BREAK_EVENT to the process
func sendCtrlBreak(pid int) error {
	// Note: GenerateConsoleCtrlEvent requires the process to be in a console process group
	// Since we're hiding windows with CREATE_NO_WINDOW, this might not work
	// We'll try anyway and fall back if it fails
	dll, err := syscall.LoadDLL("kernel32.dll")
	if err != nil {
		return err
	}
	defer dll.Release()

	proc, err := dll.FindProc("GenerateConsoleCtrlEvent")
	if err != nil {
		return err
	}

	// CTRL_BREAK_EVENT = 1
	r, _, err := proc.Call(uintptr(1), uintptr(pid))
	if r == 0 {
		return err
	}
	return nil
}
