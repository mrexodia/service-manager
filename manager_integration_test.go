package main

import (
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

// Integration test to reproduce historical restart-collision scenario by rapidly disabling
// and re-enabling a service while it is failing/restarting.
//
// This test spawns real processes (Windows only in CI typically). It is timing-sensitive,
// but we keep it relatively forgiving.
func TestRapidDisableEnableDoesNotSpawnDuplicateInstances(t *testing.T) {
	// Only meaningful on Windows for the original issue context.
	if runtime.GOOS != "windows" {
		t.Skip("integration test is Windows-focused")
	}

	tmp := t.TempDir()

	// Build the flappy test binaries
	bin := filepath.Join(tmp, "flappy.exe") // parent
	childBin := filepath.Join(tmp, "flappychild.exe")
	if err := buildTestBinary(t, "./test-service/flappy", bin); err != nil {
		t.Fatalf("build flappy: %v", err)
	}
	if err := buildTestBinary(t, "./test-service/flappychild", childBin); err != nil {
		t.Fatalf("build flappychild: %v", err)
	}

	m := NewServiceManager(GlobalConfig{FailureRetries: 3})

	// Service fails quickly and spawns a long-lived child, to catch tree cleanup issues too.
	svcEnabled := func(enabled bool) ServiceConfig {
		return ServiceConfig{
			Name:    "flappy",
			Command: "\"" + bin + "\" -spawn -exit 1 -sleep 50",
			Enabled: &enabled,
		}
	}

	// Reproduce by rapidly toggling enable/disable while the service fails/restarts.
	// Do it multiple times to increase the chance of hitting a race.
	for i := 0; i < 25; i++ {
		m.OnServicesUpdated([]ServiceConfig{svcEnabled(true)}, nil)
		time.Sleep(80 * time.Millisecond)

		m.OnServicesUpdated([]ServiceConfig{svcEnabled(false)}, []string{"flappy"})

		// Assert the child is cleaned up quickly after stop.
		for j := 0; j < 20; j++ {
			cc, err := countProcessesByExeBasename("flappychild.exe")
			if err != nil {
				t.Fatalf("count child processes: %v", err)
			}
			if cc == 0 {
				break
			}
			time.Sleep(25 * time.Millisecond)
			if j == 19 {
				t.Fatalf("child processes not cleaned up after stop: flappychild.exe count=%d", cc)
			}
		}

		time.Sleep(20 * time.Millisecond)
	}

	// Final enable
	m.OnServicesUpdated([]ServiceConfig{svcEnabled(true)}, nil)

	// Let it run a bit; if duplication happens, there would likely be multiple processes.
	time.Sleep(700 * time.Millisecond)

	// Assert the manager tracks exactly one service object.
	services := m.GetAllServices()
	if len(services) != 1 {
		t.Fatalf("expected 1 service in manager, got %d", len(services))
	}

	// Assert we don't have multiple flappy.exe instances.
	// (We allow 0 briefly if it is in a crash/restart window, but we must not have >1.)
	for i := 0; i < 40; i++ {
		count, err := countProcessesByExeBasename("flappy.exe")
		if err != nil {
			t.Fatalf("count processes: %v", err)
		}
		if count > 1 {
			t.Fatalf("detected duplicate instances: flappy.exe count=%d", count)
		}
		time.Sleep(50 * time.Millisecond)
	}

	// Shutdown
	m.StopAll()
}

func buildTestBinary(t *testing.T, pkg, out string) error {
	t.Helper()
	// Using go test's ability to run commands is not available; just use exec.
	// Implemented in a small helper to avoid toolchain dependency injection.
	return runGoBuild(pkg, out)
}
