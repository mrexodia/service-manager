package main

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"time"
)

func main() {
	spawnGrandchild := flag.Bool("spawn", false, "spawn a grandchild process")
	testDir := flag.String("testdir", "", "directory containing test services (for go run)")
	flag.Parse()

	fmt.Println("flappychild-start")

	if *spawnGrandchild {
		// Spawn a long-lived grandchild process
		var cmd *exec.Cmd
		if *testDir != "" {
			cmd = exec.Command("go", "run", fmt.Sprintf("%s/flappygrandchild", *testDir))
		} else {
			// Fallback for backward compatibility
			self, _ := os.Executable()
			cmd = exec.Command(self+"grandchild")
		}
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Start(); err == nil {
			fmt.Println("spawned-grandchild", cmd.Process.Pid)
		}
	}

	// Sleep for a long time so we can detect if it's not killed
	time.Sleep(30 * time.Second)
	fmt.Println("flappychild-exit")
}
