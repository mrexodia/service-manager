package main

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"sync"
	"time"

	"gopkg.in/yaml.v3"
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
	Command  string            `yaml:"command"`
	Args     []string          `yaml:"args,omitempty,flow"`
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
	yamlRoot *yaml.Node

	services []ServiceConfig

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

	var root yaml.Node
	if err := yaml.Unmarshal(data, &root); err != nil {
		return fmt.Errorf("invalid YAML: %w", err)
	}

	var rootConfig RootConfig
	if err := yaml.Unmarshal(data, &rootConfig); err != nil {
		return err
	}

	// Compare and calculate changes
	// Note: cm.services is NOT modified by API methods, only by this watcher
	// So it correctly represents the OLD state before the file change
	toKill := calculateServicesToKill(cm.services, rootConfig.Services)

	// Update internal state
	cm.yamlRoot = &root
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

	var root yaml.Node
	if err := yaml.Unmarshal(data, &root); err != nil {
		return err
	}

	var rootConfig RootConfig
	if err := yaml.Unmarshal(data, &rootConfig); err != nil {
		return err
	}

	cm.yamlRoot = &root
	cm.services = rootConfig.Services
	cm.lastModTime = fileInfo.ModTime()
	cm.lastChecksum, _ = cm.fileChecksum()

	return nil
}

// saveToDisk saves the given services to disk while preserving comments and formatting
// Caller must hold the lock
func (cm *ConfigManager) saveToDisk(servicesToSave []ServiceConfig) error {
	// If we don't have a yamlRoot yet (new file), use simple marshal
	if cm.yamlRoot == nil {
		return cm.saveToDiskSimple(servicesToSave)
	}

	// Use yamlRoot to preserve comments and structure
	servicesNode, err := cm.findServicesNode(cm.yamlRoot)
	if err != nil {
		// If we can't find services node, fall back to simple marshal
		return cm.saveToDiskSimple(servicesToSave)
	}

	// Build map of existing service nodes by name
	existingNodes := make(map[string]*yaml.Node)
	for _, node := range servicesNode.Content {
		var svc ServiceConfig
		if err := node.Decode(&svc); err != nil {
			continue
		}
		existingNodes[svc.Name] = node
	}

	// Build new content array, reusing or creating nodes as needed
	newContent := make([]*yaml.Node, 0, len(servicesToSave))
	for _, svc := range servicesToSave {
		if existingNode, exists := existingNodes[svc.Name]; exists {
			// Update existing node in-place to preserve comments
			if err := cm.updateServiceNode(existingNode, svc); err != nil {
				return fmt.Errorf("failed to update service node: %w", err)
			}
			newContent = append(newContent, existingNode)
		} else {
			// New service - create new node
			var serviceNode yaml.Node
			if err := serviceNode.Encode(svc); err != nil {
				return fmt.Errorf("failed to encode service: %w", err)
			}
			// Set block style to match existing services
			setBlockStyle(&serviceNode)
			newContent = append(newContent, &serviceNode)
		}
	}

	// Replace services content
	servicesNode.Content = newContent

	// Write back to file
	return cm.writeYAML(cm.yamlRoot)
}

// saveToDiskSimple uses yaml.Marshal for initial file creation or fallback
func (cm *ConfigManager) saveToDiskSimple(servicesToSave []ServiceConfig) error {
	// Read existing file to preserve global config
	existingData, err := os.ReadFile(cm.yamlPath)
	if err != nil && !os.IsNotExist(err) {
		return err
	}

	var rootConfig RootConfig
	if len(existingData) > 0 {
		// Preserve existing global config
		if err := yaml.Unmarshal(existingData, &rootConfig); err != nil {
			return err
		}
	}

	// Update only the services part
	rootConfig.Services = servicesToSave

	// Encode to YAML
	data, err := yaml.Marshal(rootConfig)
	if err != nil {
		return err
	}

	tempPath := cm.yamlPath + ".tmp"
	if err := os.WriteFile(tempPath, data, 0644); err != nil {
		return err
	}

	if err := os.Rename(tempPath, cm.yamlPath); err != nil {
		os.Remove(tempPath)
		return err
	}

	// Don't update lastModTime/checksum here - let the watcher detect it
	// This ensures consistent behavior between API and file changes

	return nil
}

// updateServiceNode updates a service node in-place, preserving comments
func (cm *ConfigManager) updateServiceNode(node *yaml.Node, svc ServiceConfig) error {
	if node.Kind != yaml.MappingNode {
		return fmt.Errorf("service node is not a mapping")
	}

	// Build a map of what we need to set
	updates := map[string]interface{}{
		"name":     svc.Name,
		"command":  svc.Command,
		"args":     svc.Args,
		"workdir":  svc.Workdir,
		"env":      svc.Env,
		"enabled":  svc.Enabled,
		"schedule": svc.Schedule,
	}

	// Update or add each field
	for key, value := range updates {
		cm.setOrUpdateField(node, key, value)
	}

	return nil
}

// setOrUpdateField sets or updates a field in a mapping node
func (cm *ConfigManager) setOrUpdateField(node *yaml.Node, key string, value interface{}) {
	// Find existing key
	for i := 0; i < len(node.Content); i += 2 {
		keyNode := node.Content[i]
		if keyNode.Value == key {
			// Update existing value
			valueNode := node.Content[i+1]
			cm.encodeValue(valueNode, value)
			return
		}
	}

	// Key not found - add it
	keyNode := &yaml.Node{
		Kind:  yaml.ScalarNode,
		Value: key,
	}
	valueNode := &yaml.Node{}
	cm.encodeValue(valueNode, value)
	node.Content = append(node.Content, keyNode, valueNode)
}

// encodeValue encodes a value into a yaml.Node
func (cm *ConfigManager) encodeValue(node *yaml.Node, value interface{}) {
	// Handle nil pointers (for enabled field)
	if value == nil {
		node.Kind = yaml.ScalarNode
		node.Tag = "!!null"
		node.Value = ""
		return
	}

	// Handle pointer to bool (for enabled field)
	if boolPtr, ok := value.(*bool); ok {
		if boolPtr == nil {
			node.Kind = yaml.ScalarNode
			node.Tag = "!!null"
			node.Value = ""
			return
		}
		node.Kind = yaml.ScalarNode
		node.Tag = "!!bool"
		if *boolPtr {
			node.Value = "true"
		} else {
			node.Value = "false"
		}
		return
	}

	// Handle empty strings (omitempty behavior)
	if str, ok := value.(string); ok && str == "" {
		node.Kind = yaml.ScalarNode
		node.Tag = "!!str"
		node.Value = ""
		return
	}

	// Handle empty slices (omitempty behavior)
	if args, ok := value.([]string); ok && len(args) == 0 {
		node.Kind = yaml.SequenceNode
		node.Tag = "!!seq"
		node.Content = nil
		node.Style = yaml.FlowStyle
		return
	}

	// Handle non-empty slices
	if args, ok := value.([]string); ok {
		node.Kind = yaml.SequenceNode
		node.Tag = "!!seq"
		node.Style = yaml.FlowStyle
		node.Content = make([]*yaml.Node, 0, len(args))
		for _, arg := range args {
			node.Content = append(node.Content, &yaml.Node{
				Kind:  yaml.ScalarNode,
				Tag:   "!!str",
				Value: arg,
			})
		}
		return
	}

	// Handle empty maps (omitempty behavior)
	if env, ok := value.(map[string]string); ok && len(env) == 0 {
		node.Kind = yaml.MappingNode
		node.Tag = "!!map"
		node.Content = nil
		return
	}

	// Handle non-empty maps
	if env, ok := value.(map[string]string); ok {
		node.Kind = yaml.MappingNode
		node.Tag = "!!map"
		node.Content = make([]*yaml.Node, 0, len(env)*2)
		for k, v := range env {
			node.Content = append(node.Content,
				&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: k},
				&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: v},
			)
		}
		return
	}

	// For anything else, use default encoding
	var temp yaml.Node
	temp.Encode(value)
	*node = temp
}

// setBlockStyle recursively sets YAML nodes to use block style formatting
func setBlockStyle(node *yaml.Node) {
	if node == nil {
		return
	}

	switch node.Kind {
	case yaml.MappingNode:
		// Use block style for mappings (multi-line)
		node.Style = 0 // Default style for mappings is block
		// Recursively set style for all child nodes
		for _, child := range node.Content {
			setBlockStyle(child)
		}
	case yaml.SequenceNode:
		// Keep flow style for sequences (inline like [a, b, c])
		// This is already set by encodeValue, but ensure it stays
		if node.Style == 0 {
			node.Style = yaml.FlowStyle
		}
		for _, child := range node.Content {
			setBlockStyle(child)
		}
	case yaml.ScalarNode:
		// Use default style for scalars
		node.Style = 0
	}
}

// findServicesNode locates the services array node in the YAML tree
func (cm *ConfigManager) findServicesNode(root *yaml.Node) (*yaml.Node, error) {
	// Root is a document node, content[0] is the mapping node
	if len(root.Content) == 0 {
		return nil, fmt.Errorf("empty YAML document")
	}

	docNode := root.Content[0]
	if docNode.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("expected mapping node at document root")
	}

	// Find the "services" key
	for i := 0; i < len(docNode.Content); i += 2 {
		keyNode := docNode.Content[i]
		valueNode := docNode.Content[i+1]

		if keyNode.Value == "services" {
			if valueNode.Kind != yaml.SequenceNode {
				return nil, fmt.Errorf("services is not a sequence")
			}
			return valueNode, nil
		}
	}

	return nil, fmt.Errorf("services key not found in YAML")
}

// writeYAML writes a YAML node back to the config file atomically
func (cm *ConfigManager) writeYAML(root *yaml.Node) error {
	// Write to temporary file first
	tmpFile := cm.yamlPath + ".tmp"
	file, err := os.Create(tmpFile)
	if err != nil {
		return fmt.Errorf("failed to create temp config file: %w", err)
	}

	// Ensure temp file is removed if something goes wrong
	defer func() {
		if file != nil {
			file.Close()
		}
		// Remove temp file if it still exists (in case of error)
		if _, err := os.Stat(tmpFile); err == nil {
			os.Remove(tmpFile)
		}
	}()

	encoder := yaml.NewEncoder(file)
	encoder.SetIndent(2)

	if err := encoder.Encode(root); err != nil {
		encoder.Close()
		return fmt.Errorf("failed to encode YAML: %w", err)
	}

	encoder.Close()
	file.Close()
	file = nil // Mark as closed so defer doesn't close again

	// Atomically replace the original file
	if err := os.Rename(tmpFile, cm.yamlPath); err != nil {
		return fmt.Errorf("failed to rename temp file: %w", err)
	}

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
