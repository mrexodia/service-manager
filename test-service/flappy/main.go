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
	child := flag.Bool("child", false, "run as child")
	spawn := flag.Bool("spawn", false, "spawn a child process")
	exitCode := flag.Int("exit", 1, "exit code")
	sleepMs := flag.Int("sleep", 50, "sleep ms before exiting")
	flag.Parse()

	if *child {
		// Child: sleep long so we can detect process tree cleanup
		fmt.Println("child-start")
		time.Sleep(10 * time.Second)
		fmt.Println("child-exit")
		os.Exit(0)
	}

	if *spawn {
		// Spawn a long-lived child (separate binary) so tests can count it reliably.
		self, _ := os.Executable()
		childPath := filepath.Join(filepath.Dir(self), "flappychild.exe")
		cmd := exec.Command(childPath)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		_ = cmd.Start()
		fmt.Println("spawned-child", cmd.Process.Pid)
	}

	// Exit quickly with failure to trigger restarts.
	time.Sleep(time.Duration(*sleepMs) * time.Millisecond)
	fmt.Println("parent-exit", *exitCode)
	os.Exit(*exitCode)
}
