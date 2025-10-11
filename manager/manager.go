package manager

import (
	"fmt"
	"os"
	"sync"
	"time"

	"service-manager/config"
	"service-manager/service"
)

// Manager manages all services
type Manager struct {
	services       map[string]*service.Service
	order          []string // Maintains service order from YAML
	lastModTime    time.Time
	mu             sync.RWMutex
	stopWatchChan  chan struct{}
}

// New creates a new manager
func New() *Manager {
	return &Manager{
		services:      make(map[string]*service.Service),
		order:         make([]string, 0),
		stopWatchChan: make(chan struct{}),
	}
}

// LoadConfig loads services from configuration and starts them
func (m *Manager) LoadConfig(cfg *config.Config) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Record the modification time
	if info, err := os.Stat("services.yaml"); err == nil {
		m.lastModTime = info.ModTime()
	}

	for _, svcCfg := range cfg.Services {
		svc := service.New(svcCfg)
		m.services[svcCfg.Name] = svc
		m.order = append(m.order, svcCfg.Name)

		// Only start if enabled
		if svcCfg.IsEnabled() {
			if err := svc.Start(); err != nil {
				fmt.Printf("Warning: Failed to start service %s: %v\n", svcCfg.Name, err)
			}
		}
	}

	return nil
}

// ReloadConfig reloads the configuration and updates services
func (m *Manager) ReloadConfig() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	// Build a map of new services
	newServices := make(map[string]config.ServiceConfig)
	newOrder := make([]string, 0, len(cfg.Services))
	for _, svcCfg := range cfg.Services {
		newServices[svcCfg.Name] = svcCfg
		newOrder = append(newOrder, svcCfg.Name)
	}

	// Stop and remove services that are no longer in the config
	for name, svc := range m.services {
		if _, exists := newServices[name]; !exists {
			svc.Stop()
			delete(m.services, name)
		}
	}

	// Add or update services (iterate over newOrder to preserve order)
	for _, name := range newOrder {
		svcCfg := newServices[name]
		if existing, exists := m.services[name]; exists {
			// Service exists, check if config changed
			if !configEqual(existing.Config, svcCfg) {
				// Config changed, restart service if enabled
				existing.Stop()
				newSvc := service.New(svcCfg)
				m.services[name] = newSvc
				if svcCfg.IsEnabled() {
					if err := newSvc.Start(); err != nil {
						fmt.Printf("Warning: Failed to start service %s: %v\n", name, err)
					}
				}
			}
		} else {
			// New service, only start if enabled
			svc := service.New(svcCfg)
			m.services[name] = svc
			if svcCfg.IsEnabled() {
				if err := svc.Start(); err != nil {
					fmt.Printf("Warning: Failed to start service %s: %v\n", name, err)
				}
			}
		}
	}

	// Update order
	m.order = newOrder

	// Update modification time
	if info, err := os.Stat("services.yaml"); err == nil {
		m.lastModTime = info.ModTime()
	}

	return nil
}

// GetService returns a service by name
func (m *Manager) GetService(name string) (*service.Service, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	svc, exists := m.services[name]
	if !exists {
		return nil, fmt.Errorf("service %s not found", name)
	}

	return svc, nil
}

// GetAllServices returns all services in YAML order
func (m *Manager) GetAllServices() []*service.Service {
	m.mu.RLock()
	defer m.mu.RUnlock()

	services := make([]*service.Service, 0, len(m.order))
	for _, name := range m.order {
		if svc, exists := m.services[name]; exists {
			services = append(services, svc)
		}
	}

	return services
}

// StartService starts a service by name and sets enabled=true in YAML
func (m *Manager) StartService(name string) error {
	svc, err := m.GetService(name)
	if err != nil {
		return err
	}

	// Update enabled flag in YAML
	if err := config.SetServiceEnabled(name, true); err != nil {
		return fmt.Errorf("failed to update enabled flag: %w", err)
	}

	return svc.Start()
}

// StopService stops a service by name and sets enabled=false in YAML
func (m *Manager) StopService(name string) error {
	svc, err := m.GetService(name)
	if err != nil {
		return err
	}

	// Update enabled flag in YAML
	if err := config.SetServiceEnabled(name, false); err != nil {
		return fmt.Errorf("failed to update enabled flag: %w", err)
	}

	return svc.Stop()
}

// RestartService restarts a service by name
func (m *Manager) RestartService(name string) error {
	svc, err := m.GetService(name)
	if err != nil {
		return err
	}

	return svc.Restart()
}

// CreateService creates a new service and adds it to the config
func (m *Manager) CreateService(cfg config.ServiceConfig) error {
	// Check if service already exists
	m.mu.RLock()
	if _, exists := m.services[cfg.Name]; exists {
		m.mu.RUnlock()
		return fmt.Errorf("service %s already exists", cfg.Name)
	}
	m.mu.RUnlock()

	// Add to YAML file
	if err := config.AddService(cfg); err != nil {
		return fmt.Errorf("failed to add service to config: %w", err)
	}

	// Reload config
	return m.ReloadConfig()
}

// UpdateService updates an existing service in the config
func (m *Manager) UpdateService(name string, cfg config.ServiceConfig) error {
	// Check if service exists
	m.mu.RLock()
	if _, exists := m.services[name]; !exists {
		m.mu.RUnlock()
		return fmt.Errorf("service %s not found", name)
	}
	m.mu.RUnlock()

	// Update YAML file
	if err := config.UpdateService(name, cfg); err != nil {
		return fmt.Errorf("failed to update service in config: %w", err)
	}

	// Reload config
	return m.ReloadConfig()
}

// DeleteService deletes a service from the config
func (m *Manager) DeleteService(name string) error {
	// Check if service exists
	m.mu.RLock()
	if _, exists := m.services[name]; !exists {
		m.mu.RUnlock()
		return fmt.Errorf("service %s not found", name)
	}
	m.mu.RUnlock()

	// Delete from YAML file
	if err := config.DeleteService(name); err != nil {
		return fmt.Errorf("failed to delete service from config: %w", err)
	}

	// Reload config
	return m.ReloadConfig()
}

// StartConfigWatch starts watching the config file for changes
func (m *Manager) StartConfigWatch() {
	ticker := time.NewTicker(5 * time.Second)
	go func() {
		for {
			select {
			case <-ticker.C:
				// Check if file was modified
				info, err := os.Stat("services.yaml")
				if err != nil {
					continue
				}

				m.mu.RLock()
				lastMod := m.lastModTime
				m.mu.RUnlock()

				if info.ModTime().After(lastMod) {
					fmt.Println("Config file changed, reloading...")
					if err := m.ReloadConfig(); err != nil {
						fmt.Printf("Error reloading config: %v\n", err)
					} else {
						fmt.Println("Config reloaded successfully")
					}
				}

			case <-m.stopWatchChan:
				ticker.Stop()
				return
			}
		}
	}()
}

// StopAll stops all services and the config watcher
func (m *Manager) StopAll() {
	// Stop config watcher
	close(m.stopWatchChan)

	m.mu.Lock()
	defer m.mu.Unlock()

	for _, svc := range m.services {
		svc.Stop()
	}
}

// configEqual compares two service configs for equality
func configEqual(a, b config.ServiceConfig) bool {
	if a.Name != b.Name || a.Command != b.Command || a.Workdir != b.Workdir {
		return false
	}

	// Check enabled flag
	if a.IsEnabled() != b.IsEnabled() {
		return false
	}

	if len(a.Args) != len(b.Args) {
		return false
	}
	for i := range a.Args {
		if a.Args[i] != b.Args[i] {
			return false
		}
	}

	if len(a.Env) != len(b.Env) {
		return false
	}
	for k, v := range a.Env {
		if b.Env[k] != v {
			return false
		}
	}

	return true
}
