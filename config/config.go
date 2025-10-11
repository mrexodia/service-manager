package config

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

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
	Args     []string          `yaml:"args,omitempty"`
	Workdir  string            `yaml:"workdir,omitempty"`
	Env      map[string]string `yaml:"env,omitempty"`
	Enabled  *bool             `yaml:"enabled,omitempty"`  // nil means true for backwards compatibility
	Schedule string            `yaml:"schedule,omitempty"` // Cron schedule (empty = continuous service)
}

// Config represents the entire configuration file
type Config struct {
	Global   GlobalConfig    `yaml:"config,omitempty"`
	Services []ServiceConfig `yaml:"services"`
}

const configFile = "services.yaml"

// Load reads and parses the YAML configuration file
func Load() (*Config, error) {
	data, err := os.ReadFile(configFile)
	if err != nil {
		return nil, fmt.Errorf("failed to read config file: %w", err)
	}

	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("failed to parse config file: %w", err)
	}

	// Set defaults
	if cfg.Global.Host == "" {
		cfg.Global.Host = "127.0.0.1"
	}
	if cfg.Global.Port == 0 {
		cfg.Global.Port = 4321
	}
	if cfg.Global.FailureRetries == 0 {
		cfg.Global.FailureRetries = 3 // Default: trigger webhook after 3 consecutive failures
	}

	return &cfg, nil
}

// AddService adds a new service to the YAML file while preserving comments
func AddService(service ServiceConfig) error {
	// Read the raw YAML file
	data, err := os.ReadFile(configFile)
	if err != nil {
		return fmt.Errorf("failed to read config file: %w", err)
	}

	// Parse into a Node to preserve structure and comments
	var root yaml.Node
	if err := yaml.Unmarshal(data, &root); err != nil {
		return fmt.Errorf("failed to parse YAML: %w", err)
	}

	// Navigate to the services array
	servicesNode, err := findServicesNode(&root)
	if err != nil {
		return err
	}

	// Create a new service node
	var serviceNode yaml.Node
	if err := serviceNode.Encode(service); err != nil {
		return fmt.Errorf("failed to encode service: %w", err)
	}

	// Add the new service to the array
	servicesNode.Content = append(servicesNode.Content, &serviceNode)

	// Write back to file
	return writeYAML(&root)
}

// UpdateService updates an existing service in the YAML file while preserving comments
func UpdateService(name string, service ServiceConfig) error {
	data, err := os.ReadFile(configFile)
	if err != nil {
		return fmt.Errorf("failed to read config file: %w", err)
	}

	var root yaml.Node
	if err := yaml.Unmarshal(data, &root); err != nil {
		return fmt.Errorf("failed to parse YAML: %w", err)
	}

	servicesNode, err := findServicesNode(&root)
	if err != nil {
		return err
	}

	// Find the service to update
	index := -1
	for i, node := range servicesNode.Content {
		var svc ServiceConfig
		if err := node.Decode(&svc); err != nil {
			continue
		}
		if svc.Name == name {
			index = i
			break
		}
	}

	if index == -1 {
		return fmt.Errorf("service %s not found", name)
	}

	// Encode the updated service
	var serviceNode yaml.Node
	if err := serviceNode.Encode(service); err != nil {
		return fmt.Errorf("failed to encode service: %w", err)
	}

	// Replace the service node
	servicesNode.Content[index] = &serviceNode

	return writeYAML(&root)
}

// DeleteService removes a service from the YAML file while preserving comments
func DeleteService(name string) error {
	data, err := os.ReadFile(configFile)
	if err != nil {
		return fmt.Errorf("failed to read config file: %w", err)
	}

	var root yaml.Node
	if err := yaml.Unmarshal(data, &root); err != nil {
		return fmt.Errorf("failed to parse YAML: %w", err)
	}

	servicesNode, err := findServicesNode(&root)
	if err != nil {
		return err
	}

	// Find and remove the service
	index := -1
	for i, node := range servicesNode.Content {
		var svc ServiceConfig
		if err := node.Decode(&svc); err != nil {
			continue
		}
		if svc.Name == name {
			index = i
			break
		}
	}

	if index == -1 {
		return fmt.Errorf("service %s not found", name)
	}

	// Remove from slice
	servicesNode.Content = append(servicesNode.Content[:index], servicesNode.Content[index+1:]...)

	return writeYAML(&root)
}

// SetServiceEnabled updates the enabled flag for a service
func SetServiceEnabled(name string, enabled bool) error {
	data, err := os.ReadFile(configFile)
	if err != nil {
		return fmt.Errorf("failed to read config file: %w", err)
	}

	var root yaml.Node
	if err := yaml.Unmarshal(data, &root); err != nil {
		return fmt.Errorf("failed to parse YAML: %w", err)
	}

	servicesNode, err := findServicesNode(&root)
	if err != nil {
		return err
	}

	// Find the service
	index := -1
	for i, node := range servicesNode.Content {
		var svc ServiceConfig
		if err := node.Decode(&svc); err != nil {
			continue
		}
		if svc.Name == name {
			index = i
			break
		}
	}

	if index == -1 {
		return fmt.Errorf("service %s not found", name)
	}

	serviceNode := servicesNode.Content[index]

	// Find or add the enabled field in the service mapping
	if serviceNode.Kind != yaml.MappingNode {
		return fmt.Errorf("service node is not a mapping")
	}

	// Look for existing enabled field
	foundEnabled := false
	for i := 0; i < len(serviceNode.Content); i += 2 {
		keyNode := serviceNode.Content[i]
		if keyNode.Value == "enabled" {
			// Update existing enabled value
			valueNode := serviceNode.Content[i+1]
			valueNode.Value = fmt.Sprintf("%t", enabled)
			foundEnabled = true
			break
		}
	}

	// If not found, add it
	if !foundEnabled {
		keyNode := &yaml.Node{
			Kind:  yaml.ScalarNode,
			Value: "enabled",
		}
		valueNode := &yaml.Node{
			Kind:  yaml.ScalarNode,
			Value: fmt.Sprintf("%t", enabled),
			Tag:   "!!bool",
		}
		serviceNode.Content = append(serviceNode.Content, keyNode, valueNode)
	}

	return writeYAML(&root)
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

// findServicesNode locates the services array node in the YAML tree
func findServicesNode(root *yaml.Node) (*yaml.Node, error) {
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
func writeYAML(root *yaml.Node) error {
	// Write to temporary file first
	tmpFile := configFile + ".tmp"
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
	if err := os.Rename(tmpFile, configFile); err != nil {
		return fmt.Errorf("failed to rename temp file: %w", err)
	}

	return nil
}
