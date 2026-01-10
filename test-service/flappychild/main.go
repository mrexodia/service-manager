package main

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

func main() {
	spawnGrandchild := flag.Bool("spawn", false, "spawn a grandchild process")
	testDir := flag.String("testdir", "", "directory containing test services (for go run)")
	flag.Parse()

	fmt.Println("flappychild-start, spawnGrandchild:", *spawnGrandchild, "testDir:", *testDir)

	if *spawnGrandchild {
		// Spawn a long-lived grandchild process
		var cmd *exec.Cmd
		if *testDir != "" {
			// testDir is the full path to test-service directory
			// Use package path within testServiceDir
			grandchildPath := filepath.Join(*testDir, "flappygrandchild", "main.go")
			fmt.Println("About to run:", grandchildPath)
			cmd = exec.Command("go", "run", grandchildPath)
		} else {
			// Fallback for backward compatibility
			self, _ := os.Executable()
			fmt.Println("Using fallback, self:", self)
			cmd = exec.Command(self+"grandchild")
		}
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		fmt.Println("Starting command:", cmd.String())
		if err := cmd.Start(); err != nil {
			fmt.Println("failed to spawn grandchild:", err)
		} else {
			fmt.Println("spawned-grandchild", cmd.Process.Pid)
		}
	}

	// Sleep for a long time so we can detect if it's not killed
	time.Sleep(30 * time.Second)
	fmt.Println("flappychild-exit")
}
