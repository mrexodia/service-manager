package main

import (
	"fmt"
	"time"
)

func main() {
	fmt.Println("flappygrandchild-start")
	// Sleep for a long time so we can detect if it's not killed
	time.Sleep(30 * time.Second)
	fmt.Println("flappygrandchild-exit")
}
