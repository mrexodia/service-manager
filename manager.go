package main

import (
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/robfig/cron/v3"
)

// ServiceManager manages all services and implements ConfigListener
type ServiceManager struct {
	services        map[string]*Service
	order           []string // Maintains service order from YAML
	cronScheduler   *cron.Cron
	cronEntries     map[string]cron.EntryID // Maps service name to cron entry ID
	globalConfig    GlobalConfig
	webhookNotifier *Notifier
	webhookSent     map[string]bool // Track if webhook was sent for a service (reset on success)
	webhookWg       sync.WaitGroup  // Track pending webhook goroutines
	mu              sync.RWMutex
}

// New creates a new manager
func NewServiceManager(globalConfig GlobalConfig) *ServiceManager {
	cronScheduler := cron.New()
	cronScheduler.Start()

	return &ServiceManager{
		services:        make(map[string]*Service),
		order:           make([]string, 0),
		cronScheduler:   cronScheduler,
		cronEntries:     make(map[string]cron.EntryID),
		webhookSent:     make(map[string]bool),
		globalConfig:    globalConfig,
		webhookNotifier: NewNotifier(globalConfig.FailureWebhookURL),
	}
}

// ============================================================================
// ConfigListener Interface Implementation
// ============================================================================

// OnServicesUpdated implements ConfigListener - called when services configuration changes
func (m *ServiceManager) OnServicesUpdated(services []ServiceConfig, toKill []string) {
	fmt.Printf("[Manager] Services updated\n")

	m.mu.Lock()
	defer m.mu.Unlock()

	// Step 1: Kill services that need to be stopped
	if len(toKill) > 0 {
		fmt.Printf("[Manager]   ToKill: %v\n", toKill)
		for _, name := range toKill {
			if state, exists := m.services[name]; exists {
				fmt.Printf("[Manager]     Stopping: %s\n", name)
				m.unscheduleService(name)
				// Only call Stop if the service is actually running
				if state.IsRunning() {
					state.Stop()
				}
				delete(m.services, name)
			}
		}
	}

	// Step 2: Build new service map and order
	newServiceMap := make(map[string]ServiceConfig)
	newOrder := make([]string, 0, len(services))
	for _, svc := range services {
		newServiceMap[svc.Name] = svc
		newOrder = append(newOrder, svc.Name)
	}

	// Step 3: Remove services no longer in config
	for name := range m.services {
		if _, exists := newServiceMap[name]; !exists {
			fmt.Printf("[Manager]   Removing: %s (no longer in config)\n", name)
			m.unscheduleService(name)
			m.services[name].Stop()
			delete(m.services, name)
		}
	}

	// Step 4: Create or update services
	newCount := 0
	for _, svc := range services {
		state, exists := m.services[svc.Name]

		if !exists {
			// New service
			newCount++
			state = NewService(svc)
			state.SetFailureCallback(m.handleServiceFailure)
			m.services[svc.Name] = state

			// Start if enabled
			if svc.IsEnabled() {
				if svc.IsScheduled() {
					if err := m.scheduleService(svc.Name, state); err != nil {
						fmt.Printf("[Manager]     Failed to schedule %s: %v\n", svc.Name, err)
					} else {
						fmt.Printf("[Manager]     Scheduled: %s (%s)\n", svc.Name, svc.Schedule)
					}
				} else {
					if err := state.Start(); err != nil {
						fmt.Printf("[Manager]     Failed to start %s: %v\n", svc.Name, err)
					} else {
						fmt.Printf("[Manager]     Started: %s\n", svc.Name)
					}
				}
			}
		} else {
			// Existing service - just update config reference
			state.Config = svc
		}
	}

	// Update order
	m.order = newOrder

	if newCount > 0 {
		fmt.Printf("[Manager]   Created: %d new services\n", newCount)
	}

	fmt.Printf("[Manager] Update complete. Total services: %d\n", len(m.services))
}

// GetGlobalConfig returns the global configuration
func (m *ServiceManager) GetGlobalConfig() GlobalConfig {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.globalConfig
}

// GetService returns a service by name
func (m *ServiceManager) GetService(name string) (*Service, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	svc, exists := m.services[name]
	if !exists {
		return nil, fmt.Errorf("service %s not found", name)
	}

	return svc, nil
}

// GetAllServices returns all services in YAML order
func (m *ServiceManager) GetAllServices() []*Service {
	m.mu.RLock()
	defer m.mu.RUnlock()

	services := make([]*Service, 0, len(m.order))
	for _, name := range m.order {
		if svc, exists := m.services[name]; exists {
			services = append(services, svc)
		}
	}

	return services
}

// StartService starts a service by name (runtime control only)
func (m *ServiceManager) StartService(name string) error {
	svc, err := m.GetService(name)
	if err != nil {
		return err
	}

	return svc.Start()
}

// StopService stops a service by name (runtime control only)
func (m *ServiceManager) StopService(name string) error {
	svc, err := m.GetService(name)
	if err != nil {
		return err
	}

	return svc.Stop()
}

// RestartService restarts a service by name
func (m *ServiceManager) RestartService(name string) error {
	svc, err := m.GetService(name)
	if err != nil {
		return err
	}

	return svc.Restart()
}

// scheduleService adds a service to the cron scheduler
func (m *ServiceManager) scheduleService(name string, svc *Service) error {
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
func (m *ServiceManager) unscheduleService(name string) {
	if entryID, exists := m.cronEntries[name]; exists {
		m.cronScheduler.Remove(entryID)
		delete(m.cronEntries, name)
	}
}

// GetNextRunTime returns the next scheduled run time for a service
func (m *ServiceManager) GetNextRunTime(name string) (time.Time, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	entryID, exists := m.cronEntries[name]
	if !exists {
		return time.Time{}, false
	}

	entry := m.cronScheduler.Entry(entryID)
	return entry.Next, true
}

// StopAll stops all services and the cron scheduler
func (m *ServiceManager) StopAll() {
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
func (m *ServiceManager) handleServiceFailure(serviceName string, consecutiveFailures int, exitCode int, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Reset webhook sent flag if service succeeded (exitCode 0 means consecutiveFailures will be 0)
	if exitCode == 0 {
		delete(m.webhookSent, serviceName)
		return
	}

	// Check if we should send webhook
	maxRetries := m.globalConfig.FailureRetries
	if consecutiveFailures < maxRetries {
		return // Not enough failures yet
	}

	// Check if we already sent webhook for this service
	if m.webhookSent[serviceName] {
		return // Already sent, don't spam
	}

	// Send webhook
	if m.webhookNotifier != nil {
		payload := FailurePayload{
			ServiceName:  serviceName,
			Timestamp:    time.Now(),
			FailureCount: consecutiveFailures,
			LastExitCode: exitCode,
			ErrorMessage: "",
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
