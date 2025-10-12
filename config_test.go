package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// ============================================================================
// Test Fixtures and Helpers
// ============================================================================

type mockConfigListener struct {
	updates []updateEvent
}

type updateEvent struct {
	services []ServiceConfig
	toKill   []string
}

func (m *mockConfigListener) OnServicesUpdated(services []ServiceConfig, toKill []string) {
	m.updates = append(m.updates, updateEvent{
		services: services,
		toKill:   toKill,
	})
}

func createTempYAML(t *testing.T, content string) string {
	t.Helper()
	tmpDir := t.TempDir()
	yamlPath := filepath.Join(tmpDir, "services.yaml")

	if content != "" {
		if err := os.WriteFile(yamlPath, []byte(content), 0644); err != nil {
			t.Fatalf("Failed to create temp YAML: %v", err)
		}
	}

	return yamlPath
}

// ============================================================================
// LoadGlobalConfig Tests
// ============================================================================

func TestLoadGlobalConfig_NonExistentFile(t *testing.T) {
	tmpDir := t.TempDir()
	nonExistentPath := filepath.Join(tmpDir, "nonexistent.yaml")

	config, err := LoadGlobalConfig(nonExistentPath)
	if err != nil {
		t.Fatalf("Expected no error for non-existent file, got: %v", err)
	}

	// Check defaults
	if config.Host != "127.0.0.1" {
		t.Errorf("Expected default host 127.0.0.1, got: %s", config.Host)
	}
	if config.Port != 4321 {
		t.Errorf("Expected default port 4321, got: %d", config.Port)
	}
	if config.FailureRetries != 3 {
		t.Errorf("Expected default failure retries 3, got: %d", config.FailureRetries)
	}
}

func TestLoadGlobalConfig_ValidFile(t *testing.T) {
	content := `host: 0.0.0.0
port: 8080
failure_webhook_url: https://example.com/webhook
failure_retries: 5
authorization: user:pass
services:
  - name: test-service
    command: echo
`
	yamlPath := createTempYAML(t, content)

	config, err := LoadGlobalConfig(yamlPath)
	if err != nil {
		t.Fatalf("Failed to load config: %v", err)
	}

	if config.Host != "0.0.0.0" {
		t.Errorf("Expected host 0.0.0.0, got: %s", config.Host)
	}
	if config.Port != 8080 {
		t.Errorf("Expected port 8080, got: %d", config.Port)
	}
	if config.FailureWebhookURL != "https://example.com/webhook" {
		t.Errorf("Expected webhook URL https://example.com/webhook, got: %s", config.FailureWebhookURL)
	}
	if config.FailureRetries != 5 {
		t.Errorf("Expected failure retries 5, got: %d", config.FailureRetries)
	}
	if config.Authorization != "user:pass" {
		t.Errorf("Expected authorization user:pass, got: %s", config.Authorization)
	}
}

func TestLoadGlobalConfig_PartialDefaults(t *testing.T) {
	content := `failure_webhook_url: https://example.com/webhook
services:
  - name: test-service
    command: echo
`
	yamlPath := createTempYAML(t, content)

	config, err := LoadGlobalConfig(yamlPath)
	if err != nil {
		t.Fatalf("Failed to load config: %v", err)
	}

	// Should have defaults
	if config.Host != "127.0.0.1" {
		t.Errorf("Expected default host, got: %s", config.Host)
	}
	if config.Port != 4321 {
		t.Errorf("Expected default port, got: %d", config.Port)
	}
	if config.FailureRetries != 3 {
		t.Errorf("Expected default retries, got: %d", config.FailureRetries)
	}
	// Should have custom value
	if config.FailureWebhookURL != "https://example.com/webhook" {
		t.Errorf("Expected custom webhook URL, got: %s", config.FailureWebhookURL)
	}
}

func TestLoadGlobalConfig_InvalidYAML(t *testing.T) {
	content := `this is: [invalid yaml`
	yamlPath := createTempYAML(t, content)

	_, err := LoadGlobalConfig(yamlPath)
	if err == nil {
		t.Fatal("Expected error for invalid YAML, got nil")
	}
}

// ============================================================================
// ServiceConfig Tests
// ============================================================================

func TestServiceConfig_IsEnabled_NilMeansTrue(t *testing.T) {
	svc := ServiceConfig{Name: "test", Command: "echo"}

	if !svc.IsEnabled() {
		t.Error("Expected nil Enabled field to be treated as enabled")
	}
}

func TestServiceConfig_IsEnabled_ExplicitTrue(t *testing.T) {
	enabled := true
	svc := ServiceConfig{Name: "test", Command: "echo", Enabled: &enabled}

	if !svc.IsEnabled() {
		t.Error("Expected explicitly enabled service to be enabled")
	}
}

func TestServiceConfig_IsEnabled_ExplicitFalse(t *testing.T) {
	enabled := false
	svc := ServiceConfig{Name: "test", Command: "echo", Enabled: &enabled}

	if svc.IsEnabled() {
		t.Error("Expected explicitly disabled service to be disabled")
	}
}

func TestServiceConfig_IsScheduled(t *testing.T) {
	tests := []struct {
		name     string
		schedule string
		expected bool
	}{
		{"empty schedule", "", false},
		{"cron schedule", "*/5 * * * *", true},
		{"daily schedule", "@daily", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc := ServiceConfig{Name: "test", Command: "echo", Schedule: tt.schedule}
			if svc.IsScheduled() != tt.expected {
				t.Errorf("Expected IsScheduled() to be %v, got %v", tt.expected, svc.IsScheduled())
			}
		})
	}
}

// ============================================================================
// ConfigManager Basic Operations Tests
// ============================================================================

func TestNewConfigManager(t *testing.T) {
	yamlPath := createTempYAML(t, "")
	cm := NewConfigManager(yamlPath)

	if cm.yamlPath != yamlPath {
		t.Errorf("Expected yamlPath %s, got %s", yamlPath, cm.yamlPath)
	}
	if cm.services == nil {
		t.Error("Expected services to be initialized")
	}
	if cm.checkInterval != 5*time.Second {
		t.Errorf("Expected check interval 5s, got %v", cm.checkInterval)
	}
}

func TestConfigManager_AddService(t *testing.T) {
	content := `services: []`
	yamlPath := createTempYAML(t, content)
	cm := NewConfigManager(yamlPath)

	if err := cm.loadFromDisk(); err != nil {
		t.Fatalf("Failed to load initial config: %v", err)
	}

	newService := ServiceConfig{
		Name:    "test-service",
		Command: "echo hello",
	}

	err := cm.AddService(newService)
	if err != nil {
		t.Fatalf("Failed to add service: %v", err)
	}

	// After the fix, cm.services is not immediately updated by API methods
	// Need to reload from disk to see the changes
	if err := cm.loadFromDisk(); err != nil {
		t.Fatalf("Failed to reload from disk: %v", err)
	}

	if cm.ServiceCount() != 1 {
		t.Errorf("Expected 1 service, got %d", cm.ServiceCount())
	}

	svc, _, found := cm.GetService("test-service")
	if !found {
		t.Fatal("Service not found after adding")
	}
	if svc.Name != "test-service" {
		t.Errorf("Expected service name test-service, got %s", svc.Name)
	}
}

func TestConfigManager_AddService_Duplicate(t *testing.T) {
	content := `services:
  - name: existing-service
    command: echo
`
	yamlPath := createTempYAML(t, content)
	cm := NewConfigManager(yamlPath)

	if err := cm.loadFromDisk(); err != nil {
		t.Fatalf("Failed to load initial config: %v", err)
	}

	duplicateService := ServiceConfig{
		Name:    "existing-service",
		Command: "ls",
	}

	err := cm.AddService(duplicateService)
	if err == nil {
		t.Fatal("Expected error when adding duplicate service, got nil")
	}

	// Should still have only 1 service
	if cm.ServiceCount() != 1 {
		t.Errorf("Expected 1 service after failed add, got %d", cm.ServiceCount())
	}
}

func TestConfigManager_UpdateService(t *testing.T) {
	content := `services:
  - name: test-service
    command: "echo old"
`
	yamlPath := createTempYAML(t, content)
	cm := NewConfigManager(yamlPath)

	if err := cm.loadFromDisk(); err != nil {
		t.Fatalf("Failed to load initial config: %v", err)
	}

	updatedService := ServiceConfig{
		Name:    "test-service",
		Command: "echo new",
	}

	err := cm.UpdateService("test-service", updatedService)
	if err != nil {
		t.Fatalf("Failed to update service: %v", err)
	}

	// After the fix, cm.services is not immediately updated by API methods
	// Need to reload from disk to see the changes
	if err := cm.loadFromDisk(); err != nil {
		t.Fatalf("Failed to reload from disk: %v", err)
	}

	svc, _, found := cm.GetService("test-service")
	if !found {
		t.Fatal("Service not found after update")
	}
	if svc.Command != "echo new" {
		t.Errorf("Expected command 'echo new', got %s", svc.Command)
	}
}

func TestConfigManager_UpdateService_NotFound(t *testing.T) {
	content := `services: []`
	yamlPath := createTempYAML(t, content)
	cm := NewConfigManager(yamlPath)

	if err := cm.loadFromDisk(); err != nil {
		t.Fatalf("Failed to load initial config: %v", err)
	}

	updatedService := ServiceConfig{
		Name:    "nonexistent",
		Command: "echo",
	}

	err := cm.UpdateService("nonexistent", updatedService)
	if err == nil {
		t.Fatal("Expected error when updating nonexistent service, got nil")
	}
}

func TestConfigManager_UpdateService_RenameWithCollision(t *testing.T) {
	content := `services:
  - name: service-a
    command: echo
  - name: service-b
    command: ls
`
	yamlPath := createTempYAML(t, content)
	cm := NewConfigManager(yamlPath)

	if err := cm.loadFromDisk(); err != nil {
		t.Fatalf("Failed to load initial config: %v", err)
	}

	// Try to rename service-a to service-b
	updatedService := ServiceConfig{
		Name:    "service-b",
		Command: "echo",
	}

	err := cm.UpdateService("service-a", updatedService)
	if err == nil {
		t.Fatal("Expected error when renaming causes collision, got nil")
	}

	// service-a should still exist
	_, _, found := cm.GetService("service-a")
	if !found {
		t.Error("service-a should still exist after failed rename")
	}
}

func TestConfigManager_DeleteService(t *testing.T) {
	content := `services:
  - name: test-service
    command: echo
`
	yamlPath := createTempYAML(t, content)
	cm := NewConfigManager(yamlPath)

	if err := cm.loadFromDisk(); err != nil {
		t.Fatalf("Failed to load initial config: %v", err)
	}

	err := cm.DeleteService("test-service")
	if err != nil {
		t.Fatalf("Failed to delete service: %v", err)
	}

	// After the fix, cm.services is not immediately updated by API methods
	// Need to reload from disk to see the changes
	if err := cm.loadFromDisk(); err != nil {
		t.Fatalf("Failed to reload from disk: %v", err)
	}

	if cm.ServiceCount() != 0 {
		t.Errorf("Expected 0 services after delete, got %d", cm.ServiceCount())
	}

	_, _, found := cm.GetService("test-service")
	if found {
		t.Error("Service should not be found after deletion")
	}
}

func TestConfigManager_DeleteService_NotFound(t *testing.T) {
	content := `services: []`
	yamlPath := createTempYAML(t, content)
	cm := NewConfigManager(yamlPath)

	if err := cm.loadFromDisk(); err != nil {
		t.Fatalf("Failed to load initial config: %v", err)
	}

	err := cm.DeleteService("nonexistent")
	if err == nil {
		t.Fatal("Expected error when deleting nonexistent service, got nil")
	}
}

func TestConfigManager_SetServiceEnabled(t *testing.T) {
	content := `services:
  - name: test-service
    command: echo
`
	yamlPath := createTempYAML(t, content)
	cm := NewConfigManager(yamlPath)

	if err := cm.loadFromDisk(); err != nil {
		t.Fatalf("Failed to load initial config: %v", err)
	}

	// Disable the service
	err := cm.SetServiceEnabled("test-service", false)
	if err != nil {
		t.Fatalf("Failed to disable service: %v", err)
	}

	// After the fix, cm.services is not immediately updated by API methods
	// Need to reload from disk to see the changes
	if err := cm.loadFromDisk(); err != nil {
		t.Fatalf("Failed to reload from disk: %v", err)
	}

	svc, _, found := cm.GetService("test-service")
	if !found {
		t.Fatal("Service not found after setting enabled")
	}
	if svc.IsEnabled() {
		t.Error("Service should be disabled")
	}

	// Enable the service
	err = cm.SetServiceEnabled("test-service", true)
	if err != nil {
		t.Fatalf("Failed to enable service: %v", err)
	}

	// Reload again to see the change
	if err := cm.loadFromDisk(); err != nil {
		t.Fatalf("Failed to reload from disk: %v", err)
	}

	svc, _, found = cm.GetService("test-service")
	if !found {
		t.Fatal("Service not found after setting enabled")
	}
	if !svc.IsEnabled() {
		t.Error("Service should be enabled")
	}
}

func TestConfigManager_SetServiceEnabled_NotFound(t *testing.T) {
	content := `services: []`
	yamlPath := createTempYAML(t, content)
	cm := NewConfigManager(yamlPath)

	if err := cm.loadFromDisk(); err != nil {
		t.Fatalf("Failed to load initial config: %v", err)
	}

	err := cm.SetServiceEnabled("nonexistent", true)
	if err == nil {
		t.Fatal("Expected error when setting enabled on nonexistent service, got nil")
	}
}

func TestConfigManager_GetService(t *testing.T) {
	content := `services:
  - name: service-a
    command: echo
  - name: service-b
    command: ls
`
	yamlPath := createTempYAML(t, content)
	cm := NewConfigManager(yamlPath)

	if err := cm.loadFromDisk(); err != nil {
		t.Fatalf("Failed to load initial config: %v", err)
	}

	svc, idx, found := cm.GetService("service-b")
	if !found {
		t.Fatal("Expected to find service-b")
	}
	if svc.Name != "service-b" {
		t.Errorf("Expected service name service-b, got %s", svc.Name)
	}
	if idx != 1 {
		t.Errorf("Expected index 1, got %d", idx)
	}

	_, _, found = cm.GetService("nonexistent")
	if found {
		t.Error("Should not find nonexistent service")
	}
}

func TestConfigManager_ListServices(t *testing.T) {
	content := `services:
  - name: service-a
    command: echo
  - name: service-b
    command: ls
`
	yamlPath := createTempYAML(t, content)
	cm := NewConfigManager(yamlPath)

	if err := cm.loadFromDisk(); err != nil {
		t.Fatalf("Failed to load initial config: %v", err)
	}

	services := cm.ListServices()
	if len(services) != 2 {
		t.Fatalf("Expected 2 services, got %d", len(services))
	}

	if services[0].Name != "service-a" {
		t.Errorf("Expected first service to be service-a, got %s", services[0].Name)
	}
	if services[1].Name != "service-b" {
		t.Errorf("Expected second service to be service-b, got %s", services[1].Name)
	}
}

func TestConfigManager_ServiceCount(t *testing.T) {
	content := `services:
  - name: service-a
    command: echo
  - name: service-b
    command: ls
  - name: service-c
    command: pwd
`
	yamlPath := createTempYAML(t, content)
	cm := NewConfigManager(yamlPath)

	if err := cm.loadFromDisk(); err != nil {
		t.Fatalf("Failed to load initial config: %v", err)
	}

	count := cm.ServiceCount()
	if count != 3 {
		t.Errorf("Expected service count 3, got %d", count)
	}
}

// ============================================================================
// File Watching Tests
// ============================================================================

func TestConfigManager_StartWatching_InitialLoad(t *testing.T) {
	content := `services:
  - name: test-service
    command: echo
`
	yamlPath := createTempYAML(t, content)
	cm := NewConfigManager(yamlPath)

	listener := &mockConfigListener{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	err := cm.StartWatching(ctx, listener)
	if err != nil {
		t.Fatalf("Failed to start watching: %v", err)
	}
	defer cm.Stop()

	// Wait for initial notification
	time.Sleep(100 * time.Millisecond)

	if len(listener.updates) != 1 {
		t.Fatalf("Expected 1 initial update, got %d", len(listener.updates))
	}

	update := listener.updates[0]
	if len(update.services) != 1 {
		t.Errorf("Expected 1 service in initial update, got %d", len(update.services))
	}
	if len(update.toKill) != 0 {
		t.Errorf("Expected 0 services to kill in initial update, got %d", len(update.toKill))
	}
}

func TestConfigManager_StartWatching_FileChange(t *testing.T) {
	content := `services:
  - name: test-service
    command: echo
`
	yamlPath := createTempYAML(t, content)
	cm := NewConfigManager(yamlPath)
	cm.checkInterval = 100 * time.Millisecond // Speed up for testing

	listener := &mockConfigListener{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	err := cm.StartWatching(ctx, listener)
	if err != nil {
		t.Fatalf("Failed to start watching: %v", err)
	}
	defer cm.Stop()

	// Wait for initial notification
	time.Sleep(150 * time.Millisecond)

	// Modify the file
	newContent := `services:
  - name: test-service
    command: echo
  - name: new-service
    command: ls
`
	if err := os.WriteFile(yamlPath, []byte(newContent), 0644); err != nil {
		t.Fatalf("Failed to modify file: %v", err)
	}

	// Wait for file change detection
	time.Sleep(300 * time.Millisecond)

	if len(listener.updates) < 2 {
		t.Fatalf("Expected at least 2 updates (initial + file change), got %d", len(listener.updates))
	}

	lastUpdate := listener.updates[len(listener.updates)-1]
	if len(lastUpdate.services) != 2 {
		t.Errorf("Expected 2 services after file change, got %d", len(lastUpdate.services))
	}
}

func TestConfigManager_StartWatching_APIChange(t *testing.T) {
	content := `services:
  - name: test-service
    command: echo
`
	yamlPath := createTempYAML(t, content)
	cm := NewConfigManager(yamlPath)

	listener := &mockConfigListener{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	err := cm.StartWatching(ctx, listener)
	if err != nil {
		t.Fatalf("Failed to start watching: %v", err)
	}
	defer cm.Stop()

	// Wait for initial notification
	time.Sleep(100 * time.Millisecond)
	initialUpdateCount := len(listener.updates)

	// Add service via API
	newService := ServiceConfig{
		Name:    "new-service",
		Command: "ls",
	}
	if err := cm.AddService(newService); err != nil {
		t.Fatalf("Failed to add service: %v", err)
	}

	// Wait for reload notification
	time.Sleep(200 * time.Millisecond)

	if len(listener.updates) <= initialUpdateCount {
		t.Fatalf("Expected update after API change, got %d updates", len(listener.updates))
	}

	lastUpdate := listener.updates[len(listener.updates)-1]
	if len(lastUpdate.services) != 2 {
		t.Errorf("Expected 2 services after API add, got %d", len(lastUpdate.services))
	}
}

// ============================================================================
// Helper Function Tests
// ============================================================================

func TestCalculateServicesToKill_DeletedService(t *testing.T) {
	oldServices := []ServiceConfig{
		{Name: "service-a", Command: "echo"},
		{Name: "service-b", Command: "ls"},
	}
	newServices := []ServiceConfig{
		{Name: "service-a", Command: "echo"},
	}

	toKill := calculateServicesToKill(oldServices, newServices)

	if len(toKill) != 1 {
		t.Fatalf("Expected 1 service to kill, got %d", len(toKill))
	}
	if toKill[0] != "service-b" {
		t.Errorf("Expected service-b to be killed, got %s", toKill[0])
	}
}

func TestCalculateServicesToKill_ModifiedService(t *testing.T) {
	oldServices := []ServiceConfig{
		{Name: "test-service", Command: "echo old"},
	}
	newServices := []ServiceConfig{
		{Name: "test-service", Command: "echo new"},
	}

	toKill := calculateServicesToKill(oldServices, newServices)

	if len(toKill) != 1 {
		t.Fatalf("Expected 1 service to kill, got %d", len(toKill))
	}
	if toKill[0] != "test-service" {
		t.Errorf("Expected test-service to be killed, got %s", toKill[0])
	}
}

func TestCalculateServicesToKill_UnchangedService(t *testing.T) {
	oldServices := []ServiceConfig{
		{Name: "test-service", Command: "echo hello"},
	}
	newServices := []ServiceConfig{
		{Name: "test-service", Command: "echo hello"},
	}

	toKill := calculateServicesToKill(oldServices, newServices)

	if len(toKill) != 0 {
		t.Errorf("Expected 0 services to kill for unchanged service, got %d", len(toKill))
	}
}

func TestCalculateServicesToKill_NewService(t *testing.T) {
	oldServices := []ServiceConfig{
		{Name: "service-a", Command: "echo"},
	}
	newServices := []ServiceConfig{
		{Name: "service-a", Command: "echo"},
		{Name: "service-b", Command: "ls"},
	}

	toKill := calculateServicesToKill(oldServices, newServices)

	if len(toKill) != 0 {
		t.Errorf("Expected 0 services to kill for new service, got %d", len(toKill))
	}
}

func TestServiceConfigsEqual_Identical(t *testing.T) {
	a := ServiceConfig{
		Name:     "test",
		Command:  "echo hello world",
		Workdir:  "/tmp",
		Schedule: "*/5 * * * *",
		Env:      map[string]string{"KEY": "value"},
	}
	b := ServiceConfig{
		Name:     "test",
		Command:  "echo hello world",
		Workdir:  "/tmp",
		Schedule: "*/5 * * * *",
		Env:      map[string]string{"KEY": "value"},
	}

	if !serviceConfigsEqual(a, b) {
		t.Error("Expected identical configs to be equal")
	}
}

func TestServiceConfigsEqual_DifferentName(t *testing.T) {
	a := ServiceConfig{Name: "test-a", Command: "echo"}
	b := ServiceConfig{Name: "test-b", Command: "echo"}

	if serviceConfigsEqual(a, b) {
		t.Error("Expected configs with different names to be unequal")
	}
}

func TestServiceConfigsEqual_DifferentCommand(t *testing.T) {
	a := ServiceConfig{Name: "test", Command: "echo"}
	b := ServiceConfig{Name: "test", Command: "ls"}

	if serviceConfigsEqual(a, b) {
		t.Error("Expected configs with different commands to be unequal")
	}
}

func TestServiceConfigsEqual_DifferentArgs(t *testing.T) {
	a := ServiceConfig{Name: "test", Command: "echo a"}
	b := ServiceConfig{Name: "test", Command: "echo b"}

	if serviceConfigsEqual(a, b) {
		t.Error("Expected configs with different args to be unequal")
	}
}

func TestServiceConfigsEqual_DifferentArgLength(t *testing.T) {
	a := ServiceConfig{Name: "test", Command: "echo a"}
	b := ServiceConfig{Name: "test", Command: "echo a b"}

	if serviceConfigsEqual(a, b) {
		t.Error("Expected configs with different arg lengths to be unequal")
	}
}

func TestServiceConfigsEqual_DifferentEnv(t *testing.T) {
	a := ServiceConfig{Name: "test", Command: "echo", Env: map[string]string{"KEY": "value1"}}
	b := ServiceConfig{Name: "test", Command: "echo", Env: map[string]string{"KEY": "value2"}}

	if serviceConfigsEqual(a, b) {
		t.Error("Expected configs with different env to be unequal")
	}
}

func TestServiceConfigsEqual_DifferentEnvLength(t *testing.T) {
	a := ServiceConfig{Name: "test", Command: "echo", Env: map[string]string{"KEY": "value"}}
	b := ServiceConfig{Name: "test", Command: "echo", Env: map[string]string{"KEY": "value", "KEY2": "value2"}}

	if serviceConfigsEqual(a, b) {
		t.Error("Expected configs with different env lengths to be unequal")
	}
}

func TestServiceConfigsEqual_DifferentEnabled(t *testing.T) {
	enabled := true
	disabled := false

	a := ServiceConfig{Name: "test", Command: "echo", Enabled: &enabled}
	b := ServiceConfig{Name: "test", Command: "echo", Enabled: &disabled}

	if serviceConfigsEqual(a, b) {
		t.Error("Expected configs with different enabled states to be unequal")
	}
}

func TestServiceConfigsEqual_EnabledNilVsTrue(t *testing.T) {
	enabled := true

	a := ServiceConfig{Name: "test", Command: "echo", Enabled: nil}
	b := ServiceConfig{Name: "test", Command: "echo", Enabled: &enabled}

	// Both are "enabled" but with different representations - should be equal
	if !serviceConfigsEqual(a, b) {
		t.Error("Expected nil enabled and explicit true to be equal")
	}
}

func TestServiceConfigsEqual_DifferentSchedule(t *testing.T) {
	a := ServiceConfig{Name: "test", Command: "echo", Schedule: "*/5 * * * *"}
	b := ServiceConfig{Name: "test", Command: "echo", Schedule: "@daily"}

	if serviceConfigsEqual(a, b) {
		t.Error("Expected configs with different schedules to be unequal")
	}
}

// ============================================================================
// File Persistence Tests
// ============================================================================

func TestConfigManager_PersistenceAcrossOperations(t *testing.T) {
	content := `host: localhost
port: 8080
services:
  - name: original-service
    command: echo
`
	yamlPath := createTempYAML(t, content)

	// First manager - add a service
	cm1 := NewConfigManager(yamlPath)
	if err := cm1.loadFromDisk(); err != nil {
		t.Fatalf("Failed to load initial config: %v", err)
	}

	newService := ServiceConfig{
		Name:    "new-service",
		Command: "ls",
	}
	if err := cm1.AddService(newService); err != nil {
		t.Fatalf("Failed to add service: %v", err)
	}

	// Second manager - verify service was persisted
	cm2 := NewConfigManager(yamlPath)
	if err := cm2.loadFromDisk(); err != nil {
		t.Fatalf("Failed to load config with second manager: %v", err)
	}

	if cm2.ServiceCount() != 2 {
		t.Errorf("Expected 2 services in second manager, got %d", cm2.ServiceCount())
	}

	_, _, found := cm2.GetService("new-service")
	if !found {
		t.Error("New service not found in second manager")
	}

	// Verify global config was preserved
	globalConfig, err := LoadGlobalConfig(yamlPath)
	if err != nil {
		t.Fatalf("Failed to load global config: %v", err)
	}
	if globalConfig.Host != "localhost" {
		t.Errorf("Expected host localhost to be preserved, got %s", globalConfig.Host)
	}
	if globalConfig.Port != 8080 {
		t.Errorf("Expected port 8080 to be preserved, got %d", globalConfig.Port)
	}
}

// ============================================================================
// Regression Tests - Enable/Disable State Changes
// ============================================================================

func TestConfigManager_Regression_EnableDisabledService(t *testing.T) {
	// Regression test: When a disabled service is enabled, it should appear in toKill
	// so the manager can restart it with the new enabled state
	content := `services:
  - name: test-service
    command: echo
    enabled: false
`
	yamlPath := createTempYAML(t, content)
	cm := NewConfigManager(yamlPath)

	listener := &mockConfigListener{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	err := cm.StartWatching(ctx, listener)
	if err != nil {
		t.Fatalf("Failed to start watching: %v", err)
	}
	defer cm.Stop()

	// Wait for initial notification
	time.Sleep(100 * time.Millisecond)
	initialUpdateCount := len(listener.updates)

	// Verify service is disabled initially
	svc, _, found := cm.GetService("test-service")
	if !found {
		t.Fatal("Service not found")
	}
	if svc.IsEnabled() {
		t.Error("Service should be disabled initially")
	}

	// Enable the service via API
	if err := cm.SetServiceEnabled("test-service", true); err != nil {
		t.Fatalf("Failed to enable service: %v", err)
	}

	// Wait for reload notification
	time.Sleep(200 * time.Millisecond)

	if len(listener.updates) <= initialUpdateCount {
		t.Fatalf("Expected update after enabling service, got %d updates", len(listener.updates))
	}

	lastUpdate := listener.updates[len(listener.updates)-1]

	// BUG: The service should appear in toKill so it can be started
	// Currently this fails because API modifies cm.services in-memory before saving,
	// so when watcher reloads from disk, old and new configs match
	if len(lastUpdate.toKill) != 1 {
		t.Errorf("Expected 1 service to kill (for restart), got %d. ToKill: %v",
			len(lastUpdate.toKill), lastUpdate.toKill)
	}
	if len(lastUpdate.toKill) > 0 && lastUpdate.toKill[0] != "test-service" {
		t.Errorf("Expected test-service to be in toKill, got %v", lastUpdate.toKill)
	}

	// Verify service is enabled after API call
	svc, _, found = cm.GetService("test-service")
	if !found {
		t.Fatal("Service not found after enable")
	}
	if !svc.IsEnabled() {
		t.Error("Service should be enabled after API call")
	}
}

func TestConfigManager_Regression_DisableEnabledService(t *testing.T) {
	// Regression test: When an enabled service is disabled, it should appear in toKill
	// so the manager can stop the running service
	content := `services:
  - name: test-service
    command: echo
`
	yamlPath := createTempYAML(t, content)
	cm := NewConfigManager(yamlPath)

	listener := &mockConfigListener{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	err := cm.StartWatching(ctx, listener)
	if err != nil {
		t.Fatalf("Failed to start watching: %v", err)
	}
	defer cm.Stop()

	// Wait for initial notification
	time.Sleep(100 * time.Millisecond)
	initialUpdateCount := len(listener.updates)

	// Verify service is enabled initially (nil means enabled)
	svc, _, found := cm.GetService("test-service")
	if !found {
		t.Fatal("Service not found")
	}
	if !svc.IsEnabled() {
		t.Error("Service should be enabled initially")
	}

	// Disable the service via API
	if err := cm.SetServiceEnabled("test-service", false); err != nil {
		t.Fatalf("Failed to disable service: %v", err)
	}

	// Wait for reload notification
	time.Sleep(200 * time.Millisecond)

	if len(listener.updates) <= initialUpdateCount {
		t.Fatalf("Expected update after disabling service, got %d updates", len(listener.updates))
	}

	lastUpdate := listener.updates[len(listener.updates)-1]

	// BUG: The service should appear in toKill so it can be stopped
	// Currently this fails because API modifies cm.services in-memory before saving,
	// so when watcher reloads from disk, old and new configs match
	if len(lastUpdate.toKill) != 1 {
		t.Errorf("Expected 1 service to kill, got %d. ToKill: %v",
			len(lastUpdate.toKill), lastUpdate.toKill)
	}
	if len(lastUpdate.toKill) > 0 && lastUpdate.toKill[0] != "test-service" {
		t.Errorf("Expected test-service to be in toKill, got %v", lastUpdate.toKill)
	}

	// Verify service is disabled after API call
	svc, _, found = cm.GetService("test-service")
	if !found {
		t.Fatal("Service not found after disable")
	}
	if svc.IsEnabled() {
		t.Error("Service should be disabled after API call")
	}
}

func TestConfigManager_Regression_DisableThenEnable(t *testing.T) {
	// Regression test: Disable a service, then enable it - both should trigger toKill
	content := `services:
  - name: test-service
    command: echo
`
	yamlPath := createTempYAML(t, content)
	cm := NewConfigManager(yamlPath)

	listener := &mockConfigListener{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	err := cm.StartWatching(ctx, listener)
	if err != nil {
		t.Fatalf("Failed to start watching: %v", err)
	}
	defer cm.Stop()

	// Wait for initial notification
	time.Sleep(100 * time.Millisecond)
	initialUpdateCount := len(listener.updates)

	// Step 1: Disable the service
	if err := cm.SetServiceEnabled("test-service", false); err != nil {
		t.Fatalf("Failed to disable service: %v", err)
	}

	time.Sleep(200 * time.Millisecond)

	disableUpdateIdx := len(listener.updates) - 1
	if disableUpdateIdx < initialUpdateCount {
		t.Fatal("No update received after disable")
	}

	disableUpdate := listener.updates[disableUpdateIdx]
	if len(disableUpdate.toKill) != 1 || disableUpdate.toKill[0] != "test-service" {
		t.Errorf("After disable: expected [test-service] in toKill, got %v", disableUpdate.toKill)
	}

	// Step 2: Enable the service again
	if err := cm.SetServiceEnabled("test-service", true); err != nil {
		t.Fatalf("Failed to enable service: %v", err)
	}

	time.Sleep(200 * time.Millisecond)

	if len(listener.updates) <= disableUpdateIdx {
		t.Fatal("No update received after enable")
	}

	enableUpdate := listener.updates[len(listener.updates)-1]
	if len(enableUpdate.toKill) != 1 || enableUpdate.toKill[0] != "test-service" {
		t.Errorf("After enable: expected [test-service] in toKill, got %v", enableUpdate.toKill)
	}
}

func TestConfigManager_Regression_ModifyDisabledService(t *testing.T) {
	// Regression test: When a disabled service is modified, ConfigManager reports it in toKill
	// The ServiceManager is responsible for checking if it's enabled before taking action
	content := `services:
  - name: test-service
    command: "echo original"
    enabled: false
`
	yamlPath := createTempYAML(t, content)
	cm := NewConfigManager(yamlPath)

	listener := &mockConfigListener{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	err := cm.StartWatching(ctx, listener)
	if err != nil {
		t.Fatalf("Failed to start watching: %v", err)
	}
	defer cm.Stop()

	// Wait for initial notification
	time.Sleep(100 * time.Millisecond)
	initialUpdateCount := len(listener.updates)

	// Modify the disabled service (change command)
	updatedService := ServiceConfig{
		Name:    "test-service",
		Command: "echo modified",
		Enabled: boolPtr(false),
	}

	if err := cm.UpdateService("test-service", updatedService); err != nil {
		t.Fatalf("Failed to update service: %v", err)
	}

	// Wait for reload notification
	time.Sleep(200 * time.Millisecond)

	if len(listener.updates) <= initialUpdateCount {
		t.Fatalf("Expected update after modifying service, got %d updates", len(listener.updates))
	}

	lastUpdate := listener.updates[len(listener.updates)-1]

	// ConfigManager reports the change (it's the ServiceManager's job to check enabled state)
	if len(lastUpdate.toKill) != 1 {
		t.Errorf("Expected 1 service in toKill (ConfigManager reports all changes), got %d. ToKill: %v",
			len(lastUpdate.toKill), lastUpdate.toKill)
	}
	if len(lastUpdate.toKill) > 0 && lastUpdate.toKill[0] != "test-service" {
		t.Errorf("Expected test-service in toKill, got %v", lastUpdate.toKill)
	}

	// Verify service is still disabled but has new command
	svc := lastUpdate.services[0]
	if svc.IsEnabled() {
		t.Error("Service should still be disabled after modification")
	}
	if svc.Command != "echo modified" {
		t.Errorf("Service command should be updated to 'echo modified', got %s", svc.Command)
	}
}

func TestConfigManager_Regression_ModifyThenDisable(t *testing.T) {
	// Regression test: When a service is modified AND disabled in the same update,
	// ConfigManager reports it in toKill. ServiceManager will kill it (to stop it) but not restart
	content := `services:
  - name: test-service
    command: "echo original"
`
	yamlPath := createTempYAML(t, content)
	cm := NewConfigManager(yamlPath)

	listener := &mockConfigListener{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	err := cm.StartWatching(ctx, listener)
	if err != nil {
		t.Fatalf("Failed to start watching: %v", err)
	}
	defer cm.Stop()

	// Wait for initial notification
	time.Sleep(100 * time.Millisecond)
	initialUpdateCount := len(listener.updates)

	// Modify the service AND disable it
	updatedService := ServiceConfig{
		Name:    "test-service",
		Command: "echo modified",
		Enabled: boolPtr(false),
	}

	if err := cm.UpdateService("test-service", updatedService); err != nil {
		t.Fatalf("Failed to update service: %v", err)
	}

	// Wait for reload notification
	time.Sleep(200 * time.Millisecond)

	if len(listener.updates) <= initialUpdateCount {
		t.Fatalf("Expected update after modifying service, got %d updates", len(listener.updates))
	}

	lastUpdate := listener.updates[len(listener.updates)-1]

	// ConfigManager reports the change
	if len(lastUpdate.toKill) != 1 {
		t.Errorf("Expected 1 service in toKill (ConfigManager reports all changes), got %d. ToKill: %v",
			len(lastUpdate.toKill), lastUpdate.toKill)
	}
	if len(lastUpdate.toKill) > 0 && lastUpdate.toKill[0] != "test-service" {
		t.Errorf("Expected test-service to be in toKill, got %v", lastUpdate.toKill)
	}

	// Verify service is now disabled (ServiceManager will see this and not restart)
	svc := lastUpdate.services[0]
	if svc.IsEnabled() {
		t.Error("Service should be disabled after modification")
	}
}

// Helper function for tests
func boolPtr(b bool) *bool {
	return &b
}
