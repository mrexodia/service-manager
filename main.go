package main

import (
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/mrexodia/service-manager/config"
	"github.com/mrexodia/service-manager/manager"
	"github.com/mrexodia/service-manager/web"
)

func main() {
	fmt.Println("Starting Service Manager...")

	// Load configuration
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to load configuration: %v\n", err)
		fmt.Println("Creating empty services.yaml file...")
		if err := createEmptyConfig(); err != nil {
			fmt.Fprintf(os.Stderr, "Failed to create services.yaml: %v\n", err)
			os.Exit(1)
		}
		cfg = &config.Config{Services: []config.ServiceConfig{}}
	}

	// Create service manager
	mgr := manager.New()

	// Load and start services
	if err := mgr.LoadConfig(cfg); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to load services: %v\n", err)
		os.Exit(1)
	}

	// Start watching for config file changes
	mgr.StartConfigWatch()
	fmt.Println("Watching services.yaml for changes...")

	// Start web server with configured host/port
	globalCfg := mgr.GetGlobalConfig()
	server := web.New(mgr, globalCfg.Host, globalCfg.Port)

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
	mgr.StopAll()
	fmt.Println("Service Manager stopped")
}

func createEmptyConfig() error {
	content := `# Service Manager Configuration
# Define your services below

config:
  host: "127.0.0.1"
  port: 4321
  failure_webhook_url: ""
  failure_retries: 3

services: []
`
	return os.WriteFile("services.yaml", []byte(content), 0644)
}
