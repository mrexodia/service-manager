//go:build windows

package main

import (
	"fmt"
	"os/exec"
	"time"

	winjob "github.com/kolesnikovae/go-winjob"
	"golang.org/x/sys/windows"
)

// configureCmdWindows sets Windows-specific process attributes
func configureCmdWindows(cmd *exec.Cmd) {
	cmd.SysProcAttr = &windows.SysProcAttr{
		HideWindow: true,
		// Start process in a new process group (fine, but not strictly required for Job Objects)
		CreationFlags: windows.CREATE_NEW_PROCESS_GROUP,
	}
}

// platformStartProcess is defined in platform_windows.go

// gracefulStop attempts to gracefully stop a service process and its entire process tree on Windows
func gracefulStop(s *Service, timeout time.Duration) error {
	if s.cmd == nil || s.cmd.Process == nil {
		return fmt.Errorf("no process to stop")
	}

	pid := s.cmd.Process.Pid

	// NOTE: service-manager is built as a GUI app and typically has *no console*.
	// Console control events (CTRL_BREAK_EVENT) are therefore unreliable and can affect
	// other processes when a console is present during testing.
	//
	// We intentionally do NOT send any console control events here.
	// If you need graceful shutdown, implement an app-level shutdown mechanism in the service
	// (HTTP endpoint, named pipe, etc.). Otherwise we fall back to timeout + taskkill.

	// Wait for process to exit gracefully
	done := make(chan error, 1)
	go func() {
		_, err := s.cmd.Process.Wait()
		done <- err
	}()

	// If timeout == 0, skip waiting and force terminate immediately.
	if timeout <= 0 {
		fmt.Printf("Service %s (PID: %d) stopping: force killing immediately (no graceful stop configured)\n",
			s.Config.Name, pid)
		if s.winJob != nil {
			_ = s.winJob.Close()
			s.winJob = nil
		} else {
			cmd := exec.Command("taskkill", "/PID", fmt.Sprintf("%d", pid), "/T", "/F")
			cmd.Run()
		}
		_, err := s.cmd.Process.Wait()
		return err
	}

	select {
	case <-time.After(timeout):
		// Timeout - forcefully terminate.
		fmt.Printf("Service %s (PID: %d) did not stop gracefully after %v, force killing\n",
			s.Config.Name, pid, timeout)

		// Preferred: close job handle (KillOnJobClose) => kills entire tree.
		if s.winJob != nil {
			_ = s.winJob.Close()
			s.winJob = nil
		} else {
			// Fallback: taskkill the process tree
			cmd := exec.Command("taskkill", "/PID", fmt.Sprintf("%d", pid), "/T", "/F")
			cmd.Run()
		}

		// Wait for the termination to complete
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
