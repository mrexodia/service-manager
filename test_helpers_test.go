package main

import (
	"fmt"
	"os/exec"
)

func runGoBuild(pkg, out string) error {
	cmd := exec.Command("go", "build", "-o", out, pkg)
	b, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("go build failed: %w\n%s", err, string(b))
	}
	return nil
}
