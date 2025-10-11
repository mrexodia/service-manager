package manager

import (
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/robfig/cron/v3"
	"service-manager/config"
	"service-manager/service"
	"service-manager/webhook"
)

// Manager manages all services
type Manager struct {
	services          map[string]*service.Service
	order             []string // Maintains service order from YAML
	cronScheduler     *cron.Cron
	cronEntries       map[string]cron.EntryID // Maps service name to cron entry ID
	lastModTime       time.Time
	globalConfig      config.GlobalConfig
	webhookNotifier   *webhook.Notifier
	webhookSent       map[string]bool // Track if webhook was sent for a service (reset on success)
	webhookWg         sync.WaitGroup  // Track pending webhook goroutines
	mu                sync.RWMutex
	stopWatchChan     chan struct{}
}

// New creates a new manager
func New() *Manager {
	cronScheduler := cron.New()
	cronScheduler.Start()

	return &Manager{
		services:      make(map[string]*service.Service),
		order:         make([]string, 0),
		cronScheduler: cronScheduler,
		cronEntries:   make(map[string]cron.EntryID),
		webhookSent:   make(map[string]bool),
		stopWatchChan: make(chan struct{}),
	}
}

// LoadConfig loads services from configuration and starts them
func (m *Manager) LoadConfig(cfg *config.Config) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Store global config and create webhook notifier
	m.globalConfig = cfg.Global
	m.webhookNotifier = webhook.NewNotifier(cfg.Global.FailureWebhookURL)

	// Record the modification time
	if info, err := os.Stat("services.yaml"); err == nil {
		m.lastModTime = info.ModTime()
	}

	for _, svcCfg := range cfg.Services {
		svc := service.New(svcCfg)
		m.services[svcCfg.Name] = svc
		m.order = append(m.order, svcCfg.Name)

		// Set failure callback
		svc.SetFailureCallback(m.handleServiceFailure)

		if !svcCfg.IsEnabled() {
			continue
		}

		// Scheduled services use cron
		if svcCfg.IsScheduled() {
			if err := m.scheduleService(svcCfg.Name, svc); err != nil {
				fmt.Printf("Warning: Failed to schedule service %s: %v\n", svcCfg.Name, err)
			}
		} else {
			// Continuous services start immediately
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

	// Update global config and webhook notifier
	m.globalConfig = cfg.Global
	m.webhookNotifier = webhook.NewNotifier(cfg.Global.FailureWebhookURL)

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
			m.unscheduleService(name) // Remove from cron if scheduled
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
				// Config changed, stop existing and create new
				m.unscheduleService(name)
				existing.Stop()
				newSvc := service.New(svcCfg)
				newSvc.SetFailureCallback(m.handleServiceFailure)
				m.services[name] = newSvc

				if svcCfg.IsEnabled() {
					if svcCfg.IsScheduled() {
						if err := m.scheduleService(name, newSvc); err != nil {
							fmt.Printf("Warning: Failed to schedule service %s: %v\n", name, err)
						}
					} else {
						if err := newSvc.Start(); err != nil {
							fmt.Printf("Warning: Failed to start service %s: %v\n", name, err)
						}
					}
				}
			}
		} else {
			// New service
			svc := service.New(svcCfg)
			svc.SetFailureCallback(m.handleServiceFailure)
			m.services[name] = svc

			if svcCfg.IsEnabled() {
				if svcCfg.IsScheduled() {
					if err := m.scheduleService(name, svc); err != nil {
						fmt.Printf("Warning: Failed to schedule service %s: %v\n", name, err)
					}
				} else {
					if err := svc.Start(); err != nil {
						fmt.Printf("Warning: Failed to start service %s: %v\n", name, err)
					}
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

// GetGlobalConfig returns the global configuration
func (m *Manager) GetGlobalConfig() config.GlobalConfig {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.globalConfig
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

// EnableService sets a service as enabled in YAML
func (m *Manager) EnableService(name string) error {
	// Verify service exists
	if _, err := m.GetService(name); err != nil {
		return err
	}

	// Update enabled flag in YAML
	if err := config.SetServiceEnabled(name, true); err != nil {
		return fmt.Errorf("failed to update enabled flag: %w", err)
	}

	// Reload config to apply changes
	return m.ReloadConfig()
}

// DisableService sets a service as disabled in YAML
func (m *Manager) DisableService(name string) error {
	// Verify service exists
	if _, err := m.GetService(name); err != nil {
		return err
	}

	// Update enabled flag in YAML
	if err := config.SetServiceEnabled(name, false); err != nil {
		return fmt.Errorf("failed to update enabled flag: %w", err)
	}

	// Reload config to apply changes (this will stop the service if running)
	return m.ReloadConfig()
}

// StartService starts a service by name (runtime control only)
func (m *Manager) StartService(name string) error {
	svc, err := m.GetService(name)
	if err != nil {
		return err
	}

	return svc.Start()
}

// StopService stops a service by name (runtime control only)
func (m *Manager) StopService(name string) error {
	svc, err := m.GetService(name)
	if err != nil {
		return err
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

// scheduleService adds a service to the cron scheduler
func (m *Manager) scheduleService(name string, svc *service.Service) error {
	// Remove existing schedule if any
	m.unscheduleService(name)

	entryID, err := m.cronScheduler.AddFunc(svc.Config.Schedule, func() {
		// Check if already running (overlap prevention)
		if svc.IsRunning() {
			// Log overlap skip to stderr
			logMsg := fmt.Sprintf("[%s] Scheduled run skipped: previous instance still running\n",
				time.Now().Format("2006-01-02 15:04:05"))
			svc.WriteStderrLog(logMsg)
			return
		}

		// Start the service
		if err := svc.Start(); err != nil {
			fmt.Printf("Failed to start scheduled service %s: %v\n", name, err)
		}
	})

	if err != nil {
		return fmt.Errorf("failed to parse cron schedule %q: %w", svc.Config.Schedule, err)
	}

	m.cronEntries[name] = entryID
	return nil
}

// unscheduleService removes a service from the cron scheduler
func (m *Manager) unscheduleService(name string) {
	if entryID, exists := m.cronEntries[name]; exists {
		m.cronScheduler.Remove(entryID)
		delete(m.cronEntries, name)
	}
}

// GetNextRunTime returns the next scheduled run time for a service
func (m *Manager) GetNextRunTime(name string) (time.Time, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	entryID, exists := m.cronEntries[name]
	if !exists {
		return time.Time{}, false
	}

	entry := m.cronScheduler.Entry(entryID)
	return entry.Next, true
}

// StopAll stops all services, the cron scheduler, and the config watcher
func (m *Manager) StopAll() {
	// Stop config watcher
	close(m.stopWatchChan)

	// Stop cron scheduler
	ctx := m.cronScheduler.Stop()
	<-ctx.Done()

	m.mu.Lock()
	defer m.mu.Unlock()

	for _, svc := range m.services {
		svc.Stop()
	}

	// Wait for pending webhooks with timeout
	done := make(chan struct{})
	go func() {
		m.webhookWg.Wait()
		close(done)
	}()

	select {
	case <-done:
		// All webhooks completed
	case <-time.After(5 * time.Second):
		// Timeout waiting for webhooks
		fmt.Fprintf(os.Stderr, "Warning: Timed out waiting for pending webhooks\n")
	}
}

// handleServiceFailure is called when a service fails or succeeds (to reset state)
// Note: This callback is triggered on every service exit, not just failures
func (m *Manager) handleServiceFailure(serviceName string, consecutiveFailures int, exitCode int, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Reset webhook sent flag if service succeeded (exitCode 0 means consecutiveFailures will be 0)
	if exitCode == 0 {
		delete(m.webhookSent, serviceName)
		return
	}

	// Check if we should send webhook
	maxRetries := m.globalConfig.MaxFailureRetries
	if consecutiveFailures < maxRetries {
		return // Not enough failures yet
	}

	// Check if we already sent webhook for this service
	if m.webhookSent[serviceName] {
		return // Already sent, don't spam
	}

	// Send webhook
	if m.webhookNotifier != nil {
		payload := webhook.FailurePayload{
			ServiceName:       serviceName,
			Timestamp:         time.Now(),
			FailureCount:      consecutiveFailures,
			LastExitCode:      exitCode,
			ErrorMessage:      "",
			ConsecutiveErrors: consecutiveFailures,
		}

		if err != nil {
			payload.ErrorMessage = err.Error()
		}

		m.webhookWg.Add(1)
		go func() {
			defer m.webhookWg.Done()
			if err := m.webhookNotifier.NotifyFailure(payload); err != nil {
				fmt.Fprintf(os.Stderr, "Failed to send webhook for service %s: %v\n", serviceName, err)
			} else {
				fmt.Printf("Webhook sent for service %s (consecutive failures: %d)\n", serviceName, consecutiveFailures)
			}
		}()

		// Mark that we sent webhook for this service
		m.webhookSent[serviceName] = true
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

	// Check schedule
	if a.Schedule != b.Schedule {
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
