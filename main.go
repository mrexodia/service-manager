package main

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/signal"
	"syscall"
	"time"
)

func main() {
	fmt.Println("Starting Service Manager...")

	// Load global configuration from config.yaml
	globalConfig, err := LoadGlobalConfig("services.yaml")
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to load global config: %v\n", err)
		os.Exit(1)
	}

	// Check if port is already in use
	addr := fmt.Sprintf("%s:%d", globalConfig.Host, globalConfig.Port)
	if isPortInUse(addr) {
		fmt.Fprintf(os.Stderr, "Error: Port %d is already in use. Another instance may be running.\n", globalConfig.Port)
		os.Exit(1)
	}

	// Create service manager (implements ConfigListener)
	serviceManager := NewServiceManager(globalConfig)

	// Create config manager for services
	configManager := NewConfigManager("services.yaml")

	// Start watching for config file changes (loads initial config, emits initial state, and watches for changes)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := configManager.StartWatching(ctx, serviceManager); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to start config watcher: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("Watching services.yaml for changes...")

	// Start web server with configured host/port
	server := NewServer(serviceManager, configManager)

	// Channel to signal web server errors
	serverErrChan := make(chan error, 1)
	go func() {
		if err := server.Start(); err != nil {
			serverErrChan <- err
		}
	}()

	// Wait for interrupt signal or server error
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)

	select {
	case <-sigChan:
		fmt.Println("\nShutting down...")
	case err := <-serverErrChan:
		fmt.Fprintf(os.Stderr, "Web server error: %v\n", err)
		fmt.Println("Shutting down due to web server error...")
	}

	// Perform graceful shutdown
	cancel() // Stop config watcher
	configManager.Stop()
	serviceManager.StopAll()
	fmt.Println("Service Manager stopped")
}

// isPortInUse checks if a port is already in use by attempting to listen on it
func isPortInUse(addr string) bool {
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return true // Port is in use or unreachable
	}
	listener.Close()

	// Small delay to ensure port is fully released
	time.Sleep(10 * time.Millisecond)
	return false
}
