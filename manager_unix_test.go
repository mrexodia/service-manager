//go:build !windows

package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

// Integration test to verify that child and grandchild processes are properly
// terminated when the service is stopped on Unix-like systems.
func TestChildProcessTreeCleanup(t *testing.T) {
	// Skip on Windows (separate test exists for Windows)
	if runtime.GOOS == "windows" {
		t.Skip("Unix-specific test")
	}

	// Get the working directory (project root)
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("get working directory: %v", err)
	}
	testServiceDir := filepath.Join(wd, "test-service")

	m := NewServiceManager(GlobalConfig{})

	enabled := true
	svcConfig := ServiceConfig{
		Name: "flappy",
		// Use go run with full path to test service main.go
		Command: fmt.Sprintf("go run %s/flappy/main.go -spawn -sleep 10000 -testdir=%s",
			testServiceDir, testServiceDir),
		Enabled: &enabled,
	}

	// Update services to start the service
	m.OnServicesUpdated([]ServiceConfig{svcConfig}, nil)

	// Wait for child and grandchild to be spawned
	// Use longer wait in CI environments where go run might be slower
	time.Sleep(4 * time.Second)

	// Count go run processes for each test service
	parentCount, err := countGoRunProcesses("flappy", testServiceDir)
	if err != nil {
		t.Fatalf("count parent processes: %v", err)
	}
	childCount, err := countGoRunProcesses("flappychild", testServiceDir)
	if err != nil {
		t.Fatalf("count child processes: %v", err)
	}
	grandchildCount, err := countGoRunProcesses("flappygrandchild", testServiceDir)
	if err != nil {
		t.Fatalf("count grandchild processes: %v", err)
	}

	t.Logf("Before stop: parent=%d, child=%d, grandchild=%d", parentCount, childCount, grandchildCount)

	// Verify we have at least one of each
	if parentCount == 0 {
		t.Fatalf("expected at least 1 parent process")
	}
	if childCount == 0 {
		t.Fatalf("expected at least 1 child process")
	}
	if grandchildCount == 0 {
		t.Fatalf("expected at least 1 grandchild process")
	}

	// Stop the service
	m.OnServicesUpdated([]ServiceConfig{}, []string{"flappy"})

	// Give processes time to be killed
	time.Sleep(300 * time.Millisecond)

	// Count processes again
	parentCountAfter, err := countGoRunProcesses("flappy", testServiceDir)
	if err != nil {
		t.Fatalf("count parent processes after stop: %v", err)
	}
	childCountAfter, err := countGoRunProcesses("flappychild", testServiceDir)
	if err != nil {
		t.Fatalf("count child processes after stop: %v", err)
	}
	grandchildCountAfter, err := countGoRunProcesses("flappygrandchild", testServiceDir)
	if err != nil {
		t.Fatalf("count grandchild processes after stop: %v", err)
	}

	t.Logf("After stop: parent=%d, child=%d, grandchild=%d", parentCountAfter, childCountAfter, grandchildCountAfter)

	// Verify all processes were killed
	if parentCountAfter > 0 {
		t.Errorf("expected parent process to be killed, found %d", parentCountAfter)
	}
	if childCountAfter > 0 {
		t.Errorf("expected child process to be killed, found %d", childCountAfter)
	}
	if grandchildCountAfter > 0 {
		t.Errorf("expected grandchild process to be killed, found %d", grandchildCountAfter)
	}

	// Cleanup
	m.StopAll()
}

// countGoRunProcesses returns the number of running "go run" processes for a specific test service
func countGoRunProcesses(serviceName, testServiceDir string) (int, error) {
	// Use pgrep to find "go run" processes that match the service name
	// Pattern matches: go run .../test-service/flappy/main.go
	cmd := exec.Command("pgrep", "-f", fmt.Sprintf("%s/%s/main.go", testServiceDir, serviceName))
	output, err := cmd.Output()
	if err != nil {
		// pgrep returns exit code 1 when no processes found - that's OK
		if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 1 {
			return 0, nil
		}
		return 0, fmt.Errorf("pgrep failed: %w", err)
	}

	// Debug: print what pgrep found
	if len(output) > 0 {
		fmt.Printf("[pgrep %s/%s/main.go] found PIDs: %s", testServiceDir, serviceName, string(output))
	} else {
		fmt.Printf("[pgrep %s/%s/main.go] found no processes\n", testServiceDir, serviceName)
	}

	// Count lines in output
	lines := 0
	for _, b := range output {
		if b == '\n' {
			lines++
		}
	}
	if len(output) > 0 {
		lines++ // Last line might not have newline
	}
	return lines, nil
}
