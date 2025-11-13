package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestServiceRestartBugOnDisableEnable is a regression test for a bug where
// disabling and then re-enabling a constantly-failing service results in
// two service instances being started that collide.
//
// Bug scenario:
// 1. Service constantly fails on startup (restart loop)
// 2. User disables the service to perform recovery
// 3. User re-enables the service
// 4. Result: TWO service instances start and collide
//
// Root cause: Race condition between monitor() goroutine's restart loop
// and OnServicesUpdated() calling Start() when re-enabling the service.
func TestServiceRestartBugOnDisableEnable(t *testing.T) {
	// Clean up any existing logs from previous test runs
	os.RemoveAll("logs")

	// Create a temporary directory for the test
	tempDir, err := os.MkdirTemp("", "service-manager-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	configPath := filepath.Join(tempDir, "config.yaml")
	lockFile := filepath.Join(tempDir, "service.lock")

	// Note: Logs will be created in ./logs (current working directory)
	// not in tempDir, because that's how the service manager works

	// Create a test script that detects duplicate instances using a lock file
	scriptPath := filepath.Join(tempDir, "test-service.sh")
	scriptContent := fmt.Sprintf(`#!/bin/bash
set -e

LOCK_FILE="%s"
PID=$$

# Function to cleanup on exit
cleanup() {
    # Only remove lock if it contains our PID
    if [ -f "$LOCK_FILE" ]; then
        LOCK_PID=$(cat "$LOCK_FILE" 2>/dev/null || echo "")
        if [ "$LOCK_PID" = "$PID" ]; then
            rm -f "$LOCK_FILE"
        fi
    fi
}
trap cleanup EXIT

# Check if another instance is already running
if [ -f "$LOCK_FILE" ]; then
    OTHER_PID=$(cat "$LOCK_FILE")
    # Check if that process is actually still running
    if kill -0 "$OTHER_PID" 2>/dev/null; then
        echo "COLLISION DETECTED: Another instance (PID $OTHER_PID) is already running!" >&2
        echo "Current PID: $PID" >&2
        exit 2
    else
        # Stale lock file, remove it
        rm -f "$LOCK_FILE"
    fi
fi

# Acquire lock
echo "$PID" > "$LOCK_FILE"
echo "Service started with PID $PID" >&2

# Fail immediately to create rapid restart loops
# This increases the chance of hitting the race condition
echo "Service failing immediately (PID $PID)" >&2
exit 1
`, lockFile)

	if err := os.WriteFile(scriptPath, []byte(scriptContent), 0755); err != nil {
		t.Fatalf("Failed to write test script: %v", err)
	}

	// Create initial config with a service that fails repeatedly
	initialConfig := fmt.Sprintf(`services:
  - name: failing-service
    command: %s
    enabled: true
`, scriptPath)

	if err := os.WriteFile(configPath, []byte(initialConfig), 0644); err != nil {
		t.Fatalf("Failed to write initial config: %v", err)
	}

	// Start the service manager
	configManager := NewConfigManager(configPath)

	// Use default global config for testing
	globalConfig := GlobalConfig{
		Host: "localhost",
		Port: 8080,
	}
	serviceManager := NewServiceManager(globalConfig)

	// Start watching config - this will load and start the failing service
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := configManager.StartWatching(ctx, serviceManager); err != nil {
		t.Fatalf("Failed to start watching config: %v", err)
	}

	// Wait for the service to fail - it will wait 5 seconds before first restart
	t.Log("Waiting for service to fail and enter restart delay...")
	time.Sleep(500 * time.Millisecond)

	// Verify the service is in restart loop
	serviceManager.mu.RLock()
	service, exists := serviceManager.services["failing-service"]
	serviceManager.mu.RUnlock()

	if !exists {
		t.Fatal("Service not found in service manager")
	}

	service.mu.RLock()
	restarts := service.restarts
	service.mu.RUnlock()

	if restarts < 2 {
		t.Logf("Warning: Expected at least 2 restarts, got %d. Continuing anyway...", restarts)
	} else {
		t.Logf("Service has restarted %d times (expected behavior)", restarts)
	}

	// Now disable the service to simulate recovery
	// The service is now in the 5-second restart delay
	t.Log("Disabling service (while in restart delay)...")
	disabledConfig := fmt.Sprintf(`services:
  - name: failing-service
    command: %s
    enabled: false
`, scriptPath)

	if err := os.WriteFile(configPath, []byte(disabledConfig), 0644); err != nil {
		t.Fatalf("Failed to write disabled config: %v", err)
	}

	// Wait for the config change to be detected and processed
	time.Sleep(300 * time.Millisecond)

	// Now wait almost until the restart would have happened (4.5 seconds into the 5-second delay)
	// Then re-enable right at the critical moment when monitor() is about to call Start()
	t.Log("Waiting to approach the restart moment...")
	time.Sleep(4200 * time.Millisecond) // Total: 4.5 seconds since crash

	// Now re-enable the service - this should trigger the bug
	// The old monitor() goroutine may be just about to call Start()
	// while we create a new service and call Start() too
	t.Log("Re-enabling service NOW (critical timing to trigger bug)...")
	reenabledConfig := fmt.Sprintf(`services:
  - name: failing-service
    command: %s
    enabled: true
`, scriptPath)

	if err := os.WriteFile(configPath, []byte(reenabledConfig), 0644); err != nil {
		t.Fatalf("Failed to write re-enabled config: %v", err)
	}

	// Wait for the service to start and potentially collide
	time.Sleep(1500 * time.Millisecond)

	// Check the stderr logs for collision detection
	stderrLogPath := "logs/failing-service-stderr.log"
	stderrContent, err := os.ReadFile(stderrLogPath)
	if err != nil {
		t.Fatalf("Failed to read stderr log: %v", err)
	}

	stderrStr := string(stderrContent)

	// Look for collision message
	if strings.Contains(stderrStr, "COLLISION DETECTED") {
		t.Error("BUG REPRODUCED: Two instances of the service were started simultaneously!")
		t.Logf("Stderr log:\n%s", stderrStr)

		// Extract the PIDs involved in the collision
		lines := strings.Split(stderrStr, "\n")
		for i, line := range lines {
			if strings.Contains(line, "COLLISION DETECTED") {
				t.Logf("Collision detected at line %d: %s", i+1, line)
				if i+1 < len(lines) {
					t.Logf("  Next line: %s", lines[i+1])
				}
			}
		}
	} else {
		t.Log("No collision detected - bug may be fixed or test conditions were not met")
		t.Logf("Stderr log:\n%s", stderrStr)
	}

	// Clean up
	serviceManager.mu.RLock()
	for _, svc := range serviceManager.services {
		svc.Stop()
	}
	serviceManager.mu.RUnlock()

	configManager.Stop()
}

// TestServiceRestartBugOnDisableEnableWithRaceDetector is a more aggressive
// version that tries to hit the race condition more reliably by rapidly
// disabling and re-enabling during the restart loop.
func TestServiceRestartBugOnDisableEnableRapidToggle(t *testing.T) {
	// Clean up any existing logs from previous test runs
	os.RemoveAll("logs")

	// Create a temporary directory for the test
	tempDir, err := os.MkdirTemp("", "service-manager-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	configPath := filepath.Join(tempDir, "config.yaml")
	lockFile := filepath.Join(tempDir, "service.lock")

	// Note: Logs will be created in ./logs (current working directory)
	// not in tempDir, because that's how the service manager works

	// Create a test script that detects duplicate instances
	scriptPath := filepath.Join(tempDir, "test-service.sh")
	scriptContent := fmt.Sprintf(`#!/bin/bash
LOCK_FILE="%s"
PID=$$

cleanup() {
    if [ -f "$LOCK_FILE" ]; then
        LOCK_PID=$(cat "$LOCK_FILE" 2>/dev/null || echo "")
        if [ "$LOCK_PID" = "$PID" ]; then
            rm -f "$LOCK_FILE"
        fi
    fi
}
trap cleanup EXIT

if [ -f "$LOCK_FILE" ]; then
    OTHER_PID=$(cat "$LOCK_FILE")
    if kill -0 "$OTHER_PID" 2>/dev/null; then
        echo "COLLISION DETECTED: Another instance (PID $OTHER_PID) is already running!" >&2
        echo "Current PID: $PID" >&2
        exit 2
    else
        rm -f "$LOCK_FILE"
    fi
fi

echo "$PID" > "$LOCK_FILE"
echo "Service started with PID $PID" >&2
echo "Service failing immediately (PID $PID)" >&2
exit 1
`, lockFile)

	if err := os.WriteFile(scriptPath, []byte(scriptContent), 0755); err != nil {
		t.Fatalf("Failed to write test script: %v", err)
	}

	// Create initial config
	initialConfig := fmt.Sprintf(`services:
  - name: failing-service
    command: %s
    enabled: true
`, scriptPath)

	if err := os.WriteFile(configPath, []byte(initialConfig), 0644); err != nil {
		t.Fatalf("Failed to write initial config: %v", err)
	}

	// Start the service manager
	configManager := NewConfigManager(configPath)

	// Use default global config for testing
	globalConfig := GlobalConfig{
		Host: "localhost",
		Port: 8080,
	}
	serviceManager := NewServiceManager(globalConfig)

	// Start watching config - this will load and start the failing service
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := configManager.StartWatching(ctx, serviceManager); err != nil {
		t.Fatalf("Failed to start watching config: %v", err)
	}

	// Wait for service to start failing and enter restart delay
	time.Sleep(300 * time.Millisecond)

	// Rapidly toggle the service multiple times to increase chance of hitting the race
	// We do many attempts because the race window is very narrow
	for attempt := 0; attempt < 10; attempt++ {
		t.Logf("Attempt %d: Rapidly toggling service...", attempt+1)

		// Disable - this should close stopChan
		disabledConfig := fmt.Sprintf(`services:
  - name: failing-service
    command: %s
    enabled: false
`, scriptPath)
		if err := os.WriteFile(configPath, []byte(disabledConfig), 0644); err != nil {
			t.Fatalf("Failed to write disabled config: %v", err)
		}

		// Tiny delay to let Stop() be called
		time.Sleep(50 * time.Millisecond)

		// Re-enable immediately - create new service while old monitor() might still be running
		enabledConfig := fmt.Sprintf(`services:
  - name: failing-service
    command: %s
    enabled: true
`, scriptPath)
		if err := os.WriteFile(configPath, []byte(enabledConfig), 0644); err != nil {
			t.Fatalf("Failed to write enabled config: %v", err)
		}

		// Wait briefly for the race to manifest
		time.Sleep(200 * time.Millisecond)

		stderrLogPath := "logs/failing-service-stderr.log"
		if content, err := os.ReadFile(stderrLogPath); err == nil {
			if strings.Contains(string(content), "COLLISION DETECTED") {
				t.Logf("Collision detected on attempt %d", attempt+1)
				break
			}
		}
	}

	// Final check
	stderrLogPath := "logs/failing-service-stderr.log"
	stderrContent, err := os.ReadFile(stderrLogPath)
	if err != nil {
		t.Fatalf("Failed to read stderr log: %v", err)
	}

	stderrStr := string(stderrContent)

	if strings.Contains(stderrStr, "COLLISION DETECTED") {
		t.Error("BUG REPRODUCED: Two instances of the service were started simultaneously!")
		t.Logf("Stderr log excerpt:\n%s", stderrStr)
	} else {
		t.Log("No collision detected in rapid toggle test")
		t.Logf("Stderr log:\n%s", stderrStr)
	}

	// Clean up
	serviceManager.mu.RLock()
	for _, svc := range serviceManager.services {
		svc.Stop()
	}
	serviceManager.mu.RUnlock()

	configManager.Stop()
}

// Helper function to check if a command exists
func commandExists(cmd string) bool {
	_, err := exec.LookPath(cmd)
	return err == nil
}

func init() {
	// Ensure bash is available for the test script
	if !commandExists("bash") {
		fmt.Fprintf(os.Stderr, "Warning: bash not found, service tests may fail\n")
	}
}
