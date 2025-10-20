package main

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/goccy/go-yaml"
)

// ============================================================================
// Configuration Structures
// ============================================================================

// GlobalConfig represents global service manager settings
type GlobalConfig struct {
	Host              string `yaml:"host,omitempty"`
	Port              int    `yaml:"port,omitempty"`
	FailureWebhookURL string `yaml:"failure_webhook_url,omitempty"`
	FailureRetries    int    `yaml:"failure_retries,omitempty"` // Number of consecutive failures before webhook triggers
	Authorization     string `yaml:"authorization,omitempty"`   // BasicAuth credentials in format "username:password"
}

// ServiceConfig represents a single service configuration
type ServiceConfig struct {
	Name     string            `yaml:"name"`
	Command  string            `yaml:"command"` // Full command with arguments (e.g. "python -u server.py")
	Workdir  string            `yaml:"workdir,omitempty"`
	Env      map[string]string `yaml:"env,omitempty"`
	Enabled  *bool             `yaml:"enabled,omitempty"`  // nil means true for backwards compatibility
	Schedule string            `yaml:"schedule,omitempty"` // Cron schedule (empty = continuous service)
}

// IsEnabled returns true if the service is enabled (nil means enabled for backwards compatibility)
func (sc *ServiceConfig) IsEnabled() bool {
	if sc.Enabled == nil {
		return true
	}
	return *sc.Enabled
}

// IsScheduled returns true if the service has a cron schedule
func (sc *ServiceConfig) IsScheduled() bool {
	return sc.Schedule != ""
}

// RootConfig wraps both global config and services in services.yaml
type RootConfig struct {
	GlobalConfig `yaml:",inline"` // Embed global config at top level
	Services     []ServiceConfig  `yaml:"services"`
}

// LoadGlobalConfig loads the global configuration from services.yaml
func LoadGlobalConfig(path string) (GlobalConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			// Return default config
			return GlobalConfig{
				Host:           "127.0.0.1",
				Port:           4321,
				FailureRetries: 3,
			}, nil
		}
		return GlobalConfig{}, fmt.Errorf("failed to read config file: %w", err)
	}

	var root RootConfig
	if err := yaml.Unmarshal(data, &root); err != nil {
		return GlobalConfig{}, fmt.Errorf("failed to parse config file: %w", err)
	}

	// Set defaults
	if root.Host == "" {
		root.Host = "127.0.0.1"
	}
	if root.Port == 0 {
		root.Port = 4321
	}
	if root.FailureRetries == 0 {
		root.FailureRetries = 3
	}

	return root.GlobalConfig, nil
}

// ============================================================================
// Configuration Listener Interface
// ============================================================================

// ConfigListener receives notifications about configuration changes
type ConfigListener interface {
	// OnServicesUpdated is called when services configuration changes
	// services: complete ordered list of all services
	// toKill: names of services that need to be stopped
	OnServicesUpdated(services []ServiceConfig, toKill []string)
}

// ConfigManager manages the services.yaml file and notifies listeners of changes
type ConfigManager struct {
	yamlPath string
	comments yaml.CommentMap // Preserves comments automatically

	services []ServiceConfig // NEVER modified by API methods, only by watcher

	// For change detection
	lastModTime  time.Time
	lastChecksum string

	// File watching
	checkInterval  time.Duration
	stopChan       chan struct{}
	reloadChan     chan struct{} // for immediate reload after API changes
	reloadCooldown time.Duration
	lastReload     time.Time

	mu sync.RWMutex
}

// NewConfigManager creates a new configuration manager
func NewConfigManager(yamlPath string) *ConfigManager {
	return &ConfigManager{
		yamlPath:       yamlPath,
		services:       make([]ServiceConfig, 0),
		comments:       yaml.CommentMap{},
		checkInterval:  5 * time.Second,
		reloadCooldown: 2 * time.Second,
		stopChan:       make(chan struct{}),
		reloadChan:     make(chan struct{}, 1), // buffered to avoid blocking
	}
}

// StartWatching loads initial config and starts the file watcher in the background
func (cm *ConfigManager) StartWatching(ctx context.Context, listener ConfigListener) error {
	// Initial load from disk
	if err := cm.loadFromDisk(); err != nil {
		return err
	}

	ticker := time.NewTicker(cm.checkInterval)

	go func() {
		defer ticker.Stop()

		// Emit initial state (everything is "new")
		cm.mu.RLock()
		initialServices := cm.copyServices()
		cm.mu.RUnlock()
		listener.OnServicesUpdated(initialServices, []string{})

		for {
			select {
			case <-ctx.Done():
				return
			case <-cm.stopChan:
				return
			case <-ticker.C:
				if err := cm.checkAndReload(listener, false); err != nil {
					fmt.Printf("[Watcher] Error checking for updates: %v\n", err)
				}
			case <-cm.reloadChan:
				// Immediate reload requested (from API change)
				if err := cm.checkAndReload(listener, true); err != nil {
					fmt.Printf("[Watcher] Error reloading after API change: %v\n", err)
				}
			}
		}
	}()

	return nil
}

// Stop stops the file watcher
func (cm *ConfigManager) Stop() {
	close(cm.stopChan)
}

// ============================================================================
// Watcher - The Only Place That Emits Events
// ============================================================================

func (cm *ConfigManager) checkAndReload(listener ConfigListener, skipCooldown bool) error {
	needsReload, reason, err := cm.needsReload()
	if err != nil {
		return err
	}

	if !needsReload {
		return nil
	}

	// Cooldown check (unless explicitly skipped)
	if !skipCooldown {
		cm.mu.RLock()
		timeSinceLastReload := time.Since(cm.lastReload)
		cm.mu.RUnlock()

		if timeSinceLastReload < cm.reloadCooldown {
			return nil
		}
	}

	fmt.Printf("[Watcher] Change detected (%s), reloading configuration\n", reason)
	return cm.reloadAndNotify(listener)
}

// reloadAndNotify is the ONLY method that notifies listeners
func (cm *ConfigManager) reloadAndNotify(listener ConfigListener) error {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	// Load file
	fileInfo, err := os.Stat(cm.yamlPath)
	if err != nil {
		return err
	}

	checksum, err := cm.fileChecksum()
	if err != nil {
		return err
	}

	data, err := os.ReadFile(cm.yamlPath)
	if err != nil {
		return err
	}

	// Parse with comment preservation
	newComments := yaml.CommentMap{}
	var rootConfig RootConfig
	if err := yaml.UnmarshalWithOptions(data, &rootConfig, yaml.CommentToMap(newComments)); err != nil {
		return fmt.Errorf("invalid YAML: %w", err)
	}

	// Compare and calculate changes
	// Note: cm.services is NOT modified by API methods, only by this watcher
	// So it correctly represents the OLD state before the file change
	toKill := calculateServicesToKill(cm.services, rootConfig.Services)

	// Update internal state
	cm.comments = newComments
	cm.services = rootConfig.Services
	cm.lastModTime = fileInfo.ModTime()
	cm.lastChecksum = checksum
	cm.lastReload = time.Now()

	// Notify listeners AFTER state is updated
	fmt.Printf("[Watcher]   Services updated. ToKill: %v\n", toKill)
	listener.OnServicesUpdated(cm.copyServices(), toKill)

	return nil
}

func (cm *ConfigManager) needsReload() (bool, string, error) {
	fileInfo, err := os.Stat(cm.yamlPath)
	if err != nil {
		return false, "", err
	}

	modTime := fileInfo.ModTime()

	cm.mu.RLock()
	lastMod := cm.lastModTime
	cm.mu.RUnlock()

	if !modTime.After(lastMod) {
		return false, "", nil
	}

	checksum, err := cm.fileChecksum()
	if err != nil {
		return false, "", err
	}

	cm.mu.RLock()
	lastChecksum := cm.lastChecksum
	cm.mu.RUnlock()

	if checksum == lastChecksum {
		// Only modtime changed, not content
		cm.mu.Lock()
		cm.lastModTime = modTime
		cm.mu.Unlock()
		return false, "", nil
	}

	return true, "content changed", nil
}

// ============================================================================
// API Methods - Only Save, Never Notify Directly
// ============================================================================
// Note: Global config modification at runtime is not supported

func (cm *ConfigManager) AddService(config ServiceConfig) error {
	cm.mu.Lock()

	// Check for duplicate name
	for _, svc := range cm.services {
		if svc.Name == config.Name {
			cm.mu.Unlock()
			return fmt.Errorf("service %s already exists", config.Name)
		}
	}

	// Create a copy of services and add to the copy (don't touch cm.services)
	modifiedServices := cm.copyServices()
	modifiedServices = append(modifiedServices, config)

	if err := cm.saveToDisk(modifiedServices); err != nil {
		cm.mu.Unlock()
		return err
	}

	cm.mu.Unlock()

	// Trigger immediate reload - watcher will notify listeners
	cm.triggerReload()

	return nil
}

func (cm *ConfigManager) UpdateService(name string, config ServiceConfig) error {
	cm.mu.Lock()

	index := -1
	for i, svc := range cm.services {
		if svc.Name == name {
			index = i
			break
		}
	}

	if index == -1 {
		cm.mu.Unlock()
		return fmt.Errorf("service %s not found", name)
	}

	// Check for name collision if renaming
	if config.Name != name {
		for _, svc := range cm.services {
			if svc.Name == config.Name {
				cm.mu.Unlock()
				return fmt.Errorf("service %s already exists", config.Name)
			}
		}
	}

	// Create a copy of services and modify the copy (don't touch cm.services)
	modifiedServices := cm.copyServices()
	modifiedServices[index] = config

	if err := cm.saveToDisk(modifiedServices); err != nil {
		cm.mu.Unlock()
		return err
	}

	cm.mu.Unlock()

	cm.triggerReload()
	return nil
}

func (cm *ConfigManager) DeleteService(name string) error {
	cm.mu.Lock()

	index := -1
	for i, svc := range cm.services {
		if svc.Name == name {
			index = i
			break
		}
	}

	if index == -1 {
		cm.mu.Unlock()
		return fmt.Errorf("service %s not found", name)
	}

	// Create a copy of services and delete from the copy (don't touch cm.services)
	modifiedServices := cm.copyServices()
	modifiedServices = append(modifiedServices[:index], modifiedServices[index+1:]...)

	if err := cm.saveToDisk(modifiedServices); err != nil {
		cm.mu.Unlock()
		return err
	}

	cm.mu.Unlock()

	cm.triggerReload()
	return nil
}

func (cm *ConfigManager) SetServiceEnabled(name string, enabled bool) error {
	cm.mu.Lock()

	index := -1
	for i, svc := range cm.services {
		if svc.Name == name {
			index = i
			break
		}
	}

	if index == -1 {
		cm.mu.Unlock()
		return fmt.Errorf("service %s not found", name)
	}

	// Create a copy of services and modify the copy (don't touch cm.services)
	// This allows the watcher to detect the change by comparing old (cm.services) vs new (from disk)
	modifiedServices := cm.copyServices()
	modifiedServices[index].Enabled = &enabled

	if err := cm.saveToDisk(modifiedServices); err != nil {
		cm.mu.Unlock()
		return err
	}

	cm.mu.Unlock()

	cm.triggerReload()
	return nil
}

func (cm *ConfigManager) GetService(name string) (ServiceConfig, int, bool) {
	cm.mu.RLock()
	defer cm.mu.RUnlock()

	for i, svc := range cm.services {
		if svc.Name == name {
			return svc, i, true
		}
	}

	return ServiceConfig{}, -1, false
}

func (cm *ConfigManager) ListServices() []ServiceConfig {
	cm.mu.RLock()
	defer cm.mu.RUnlock()
	return cm.copyServices()
}

func (cm *ConfigManager) ServiceCount() int {
	cm.mu.RLock()
	defer cm.mu.RUnlock()
	return len(cm.services)
}

// triggerReload sends a signal to reload immediately
func (cm *ConfigManager) triggerReload() {
	select {
	case cm.reloadChan <- struct{}{}:
		// Reload signal sent
	default:
		// Already a pending reload, skip
	}
}

// ============================================================================
// Internal Methods
// ============================================================================

func (cm *ConfigManager) copyServices() []ServiceConfig {
	result := make([]ServiceConfig, len(cm.services))
	copy(result, cm.services)
	return result
}

func (cm *ConfigManager) loadFromDisk() error {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	fileInfo, err := os.Stat(cm.yamlPath)
	if err != nil {
		if os.IsNotExist(err) {
			// Create empty services file
			return cm.saveToDisk(make([]ServiceConfig, 0))
		}
		return err
	}

	data, err := os.ReadFile(cm.yamlPath)
	if err != nil {
		return err
	}

	// Load with comment preservation
	cm.comments = yaml.CommentMap{}
	var rootConfig RootConfig
	if err := yaml.UnmarshalWithOptions(data, &rootConfig, yaml.CommentToMap(cm.comments)); err != nil {
		return err
	}

	cm.services = rootConfig.Services
	cm.lastModTime = fileInfo.ModTime()
	cm.lastChecksum, _ = cm.fileChecksum()

	return nil
}

// saveToDisk saves the given services to disk while preserving comments and global config
// Caller must hold the lock
func (cm *ConfigManager) saveToDisk(servicesToSave []ServiceConfig) error {
	// Read existing file to preserve global config
	var rootConfig RootConfig
	if data, err := os.ReadFile(cm.yamlPath); err == nil {
		// Load existing config to preserve global settings
		// Use temporary comment map for reading, we'll use cm.comments for writing
		tempComments := yaml.CommentMap{}
		yaml.UnmarshalWithOptions(data, &rootConfig, yaml.CommentToMap(tempComments))
		// Merge comments if we don't have any yet
		if len(cm.comments) == 0 {
			cm.comments = tempComments
		}
	}

	// Update only the services part
	rootConfig.Services = servicesToSave

	// Marshal with comments preserved
	data, err := yaml.MarshalWithOptions(rootConfig, yaml.WithComment(cm.comments))
	if err != nil {
		return fmt.Errorf("failed to marshal YAML: %w", err)
	}

	// Write atomically
	tempPath := cm.yamlPath + ".tmp"
	if err := os.WriteFile(tempPath, data, 0644); err != nil {
		return fmt.Errorf("failed to write temp file: %w", err)
	}

	if err := os.Rename(tempPath, cm.yamlPath); err != nil {
		os.Remove(tempPath)
		return fmt.Errorf("failed to rename temp file: %w", err)
	}

	// Don't update lastModTime/checksum here - let the watcher detect it
	// This ensures consistent behavior between API and file changes

	return nil
}

func (cm *ConfigManager) fileChecksum() (string, error) {
	data, err := os.ReadFile(cm.yamlPath)
	if err != nil {
		return "", err
	}
	hash := sha256.Sum256(data)
	return fmt.Sprintf("%x", hash), nil
}

// calculateServicesToKill determines which services need to be stopped
func calculateServicesToKill(oldServices, newServices []ServiceConfig) []string {
	oldMap := make(map[string]ServiceConfig)
	for _, svc := range oldServices {
		oldMap[svc.Name] = svc
	}

	newMap := make(map[string]ServiceConfig)
	for _, svc := range newServices {
		newMap[svc.Name] = svc
	}

	toKill := []string{}

	// Kill deleted services
	for name := range oldMap {
		if _, exists := newMap[name]; !exists {
			toKill = append(toKill, name)
		}
	}

	// Kill modified services (requires restart anyway)
	for name, newSvc := range newMap {
		if oldSvc, exists := oldMap[name]; exists {
			if !serviceConfigsEqual(oldSvc, newSvc) {
				toKill = append(toKill, name)
			}
		}
	}

	return toKill
}

// serviceConfigsEqual compares two service configs for equality
func serviceConfigsEqual(a, b ServiceConfig) bool {
	if a.Name != b.Name || a.Command != b.Command ||
		a.Workdir != b.Workdir || a.Schedule != b.Schedule ||
		a.IsEnabled() != b.IsEnabled() {
		return false
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