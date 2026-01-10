package main

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"time"
)

func main() {
	child := flag.Bool("child", false, "run as child")
	spawn := flag.Bool("spawn", false, "spawn a child process")
	exitCode := flag.Int("exit", 1, "exit code")
	sleepMs := flag.Int("sleep", 50, "sleep ms before exiting")
	testDir := flag.String("testdir", "", "directory containing test services (for go run)")
	flag.Parse()

	if *child {
		// Child: sleep long so we can detect process tree cleanup
		fmt.Println("child-start")
		time.Sleep(10 * time.Second)
		fmt.Println("child-exit")
		os.Exit(0)
	}

	if *spawn {
		// Spawn a long-lived child process.
		// Use go run if testdir is provided, otherwise try to find the binary.
		var cmd *exec.Cmd
		if *testDir != "" {
			childPath := fmt.Sprintf("%s/flappychild/main.go", *testDir)
			cmd = exec.Command("go", "run", childPath, "-spawn", "-testdir="+*testDir)
		} else {
			// Fallback for backward compatibility
			self, _ := os.Executable()
			cmd = exec.Command(self+"child", "-spawn")
		}
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Start(); err != nil {
			fmt.Println("failed to spawn child:", err)
		} else {
			fmt.Println("spawned-child", cmd.Process.Pid)
		}
	}

	// Exit quickly with failure to trigger restarts.
	time.Sleep(time.Duration(*sleepMs) * time.Millisecond)
	fmt.Println("parent-exit", *exitCode)
	os.Exit(*exitCode)
}
