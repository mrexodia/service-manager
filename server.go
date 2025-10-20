package main

import (
	"embed"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/gorilla/websocket"
	"github.com/joho/godotenv"
)

//go:embed static
var staticFiles embed.FS

// ServiceRequest represents the JSON request for creating/updating services
type ServiceRequest struct {
	Name     string  `json:"name"`
	Command  string  `json:"command"`
	Workdir  string  `json:"workdir"`
	EnvRaw   string  `json:"env_raw"`   // Raw environment variables in dotenv format
	Env      map[string]string `json:"env"` // For backwards compatibility
	Enabled  *bool   `json:"enabled"`
	Schedule string  `json:"schedule"`
}

// Server represents the web server
type Server struct {
	serviceManager *ServiceManager
	configManager  *ConfigManager
	host           string
	port           int
	upgrader       websocket.Upgrader
	username       string // BasicAuth username (empty = no username required)
	password       string // BasicAuth password (empty = no auth)
}

// New creates a new web server
func NewServer(serviceManager *ServiceManager, configManager *ConfigManager) *Server {
	// Parse authorization config once
	var username, password string
	config := serviceManager.GetGlobalConfig()
	if config.Authorization != "" {
		if idx := strings.Index(config.Authorization, ":"); idx > 0 {
			username = config.Authorization[:idx]
			password = config.Authorization[idx+1:]
		} else {
			password = config.Authorization
		}
	}

	return &Server{
		serviceManager: serviceManager,
		configManager:  configManager,
		host:           config.Host,
		port:           config.Port,
		upgrader:       websocket.Upgrader{},
		username:       username,
		password:       password,
	}
}

// basicAuthMiddleware wraps the entire handler with BasicAuth authentication
func (s *Server) basicAuthMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// If no password configured, allow all requests
		if s.password == "" {
			next.ServeHTTP(w, r)
			return
		}

		// Get credentials from request
		username, password, ok := r.BasicAuth()
		if !ok || username != s.username || password != s.password {
			// Send WWW-Authenticate header to prompt browser for credentials
			w.Header().Set("WWW-Authenticate", `Basic realm="Service Manager"`)
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}

		// Authentication successful, proceed with handler
		next.ServeHTTP(w, r)
	})
}

// Start starts the web server
func (s *Server) Start() error {
	mux := http.NewServeMux()

	// API routes with pattern matching
	mux.HandleFunc("GET /api/services", s.listServices)
	mux.HandleFunc("POST /api/services", s.createService)
	mux.HandleFunc("GET /api/services/{name}", s.getService)
	mux.HandleFunc("PUT /api/services/{name}", s.updateService)
	mux.HandleFunc("DELETE /api/services/{name}", s.deleteService)
	mux.HandleFunc("GET /api/services/{name}/dotenv", s.getDotenv)
	mux.HandleFunc("POST /api/services/{name}/start", s.startService)
	mux.HandleFunc("POST /api/services/{name}/stop", s.stopService)
	mux.HandleFunc("POST /api/services/{name}/restart", s.restartService)
	mux.HandleFunc("POST /api/services/{name}/enable", s.enableService)
	mux.HandleFunc("POST /api/services/{name}/disable", s.disableService)
	mux.HandleFunc("POST /api/services/{name}/run-now", s.runNowService)
	mux.HandleFunc("GET /api/services/{name}/logs/{stream}", s.streamLogs)

	// Static files (catch-all)
	mux.HandleFunc("GET /{path...}", s.handleStatic)

	// Wrap entire mux with auth middleware
	handler := s.basicAuthMiddleware(mux)

	addr := fmt.Sprintf("%s:%d", s.host, s.port)
	fmt.Printf("Starting web server on http://%s\n", addr)
	return http.ListenAndServe(addr, handler)
}

// listServices returns all services with their status
func (s *Server) listServices(w http.ResponseWriter, r *http.Request) {
	services := s.serviceManager.GetAllServices()

	statuses := make([]interface{}, len(services))
	for i, svc := range services {
		status := svc.GetStatus()
		item := map[string]interface{}{
			"name":         status.Name,
			"running":      status.Running,
			"pid":          status.PID,
			"uptime":       status.Uptime.Seconds(),
			"restarts":     status.Restarts,
			"enabled":      svc.Config.IsEnabled(),
			"schedule":     svc.Config.Schedule,
			"lastRunTime":  status.LastRunTime,
			"lastExitCode": status.LastExitCode,
			"lastDuration": status.LastDuration.Seconds(),
		}

		// Add next run time for scheduled services
		if svc.Config.IsScheduled() {
			if nextRun, ok := s.serviceManager.GetNextRunTime(status.Name); ok {
				item["nextRunTime"] = nextRun
			}
		}

		statuses[i] = item
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(statuses)
}

// getService returns a specific service with config and status
func (s *Server) getService(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	svc, err := s.serviceManager.GetService(name)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}

	status := svc.GetStatus()
	response := map[string]any{
		"name":         svc.Config.Name,
		"command":      svc.Config.Command,
		"workdir":      svc.Config.Workdir,
		"env":          svc.Config.Env,
		"running":      status.Running,
		"pid":          status.PID,
		"uptime":       status.Uptime.Seconds(),
		"restarts":     status.Restarts,
		"enabled":      svc.Config.IsEnabled(),
		"schedule":     svc.Config.Schedule,
		"lastRunTime":  status.LastRunTime,
		"lastExitCode": status.LastExitCode,
		"lastDuration": status.LastDuration.Seconds(),
	}

	// Add next run time for scheduled services
	if svc.Config.IsScheduled() {
		if nextRun, ok := s.serviceManager.GetNextRunTime(svc.Config.Name); ok {
			response["nextRunTime"] = nextRun
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(response)
}

// createService creates a new service
func (s *Server) createService(w http.ResponseWriter, r *http.Request) {
	var req ServiceRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	if req.Name == "" || req.Command == "" {
		http.Error(w, "Name and command are required", http.StatusBadRequest)
		return
	}

	// Parse environment variables
	var envMap map[string]string
	if req.EnvRaw != "" {
		// Parse using godotenv for uniform handling
		parsed, err := godotenv.Unmarshal(req.EnvRaw)
		if err != nil {
			http.Error(w, fmt.Sprintf("Invalid environment variable format: %v", err), http.StatusBadRequest)
			return
		}
		envMap = parsed
	} else if req.Env != nil {
		// Backwards compatibility: use provided map
		envMap = req.Env
	}

	cfg := ServiceConfig{
		Name:     req.Name,
		Command:  req.Command,
		Workdir:  req.Workdir,
		Env:      envMap,
		Enabled:  req.Enabled,
		Schedule: req.Schedule,
	}

	if err := s.configManager.AddService(cfg); err != nil {
		if strings.Contains(err.Error(), "already exists") {
			http.Error(w, err.Error(), http.StatusConflict)
		} else {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
		return
	}

	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(map[string]string{"status": "created"})
}

// updateService updates an existing service
func (s *Server) updateService(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	var req ServiceRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	if req.Command == "" {
		http.Error(w, "Command is required", http.StatusBadRequest)
		return
	}

	// Parse environment variables
	var envMap map[string]string
	if req.EnvRaw != "" {
		// Parse using godotenv for uniform handling
		parsed, err := godotenv.Unmarshal(req.EnvRaw)
		if err != nil {
			http.Error(w, fmt.Sprintf("Invalid environment variable format: %v", err), http.StatusBadRequest)
			return
		}
		envMap = parsed
	} else if req.Env != nil {
		// Backwards compatibility: use provided map
		envMap = req.Env
	}

	cfg := ServiceConfig{
		Name:     name, // Use name from URL
		Command:  req.Command,
		Workdir:  req.Workdir,
		Env:      envMap,
		Enabled:  req.Enabled,
		Schedule: req.Schedule,
	}

	if err := s.configManager.UpdateService(name, cfg); err != nil {
		if strings.Contains(err.Error(), "not found") {
			http.Error(w, err.Error(), http.StatusNotFound)
		} else {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "updated"})
}

// deleteService deletes a service
func (s *Server) deleteService(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if err := s.configManager.DeleteService(name); err != nil {
		if strings.Contains(err.Error(), "not found") {
			http.Error(w, err.Error(), http.StatusNotFound)
		} else {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "deleted"})
}

// getDotenv checks for .env file in service's working directory
func (s *Server) getDotenv(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")

	// Get service from service manager
	svc, err := s.serviceManager.GetService(name)
	if err != nil {
		http.Error(w, "Service not found", http.StatusNotFound)
		return
	}

	// Check for workdir parameter (for preview when editing)
	workdir := r.URL.Query().Get("workdir")
	if workdir == "" {
		workdir = svc.Config.Workdir
	}

	response := map[string]interface{}{
		"exists": false,
	}

	// Only check if workdir is set
	if workdir != "" {
		dotenvPath := filepath.Join(workdir, ".env")
		if _, err := os.Stat(dotenvPath); err == nil {
			// .env file exists, try to parse it
			dotenvVars, err := godotenv.Read(dotenvPath)
			if err != nil {
				response["error"] = fmt.Sprintf("Failed to parse .env file: %v", err)
			} else {
				response["exists"] = true
				response["variables"] = dotenvVars

				// Also provide raw content for display
				content, _ := os.ReadFile(dotenvPath)
				response["raw"] = string(content)
			}
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(response)
}

// startService starts a service
func (s *Server) startService(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if err := s.serviceManager.StartService(name); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "started"})
}

// stopService stops a service
func (s *Server) stopService(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if err := s.serviceManager.StopService(name); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "stopped"})
}

// restartService restarts a service
func (s *Server) restartService(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if err := s.serviceManager.RestartService(name); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "restarted"})
}

// enableService enables a service
func (s *Server) enableService(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if err := s.configManager.SetServiceEnabled(name, true); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "enabled"})
}

// disableService disables a service
func (s *Server) disableService(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if err := s.configManager.SetServiceEnabled(name, false); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "disabled"})
}

// runNowService runs a scheduled service immediately
func (s *Server) runNowService(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	svc, err := s.serviceManager.GetService(name)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}

	// Check if it's a scheduled service
	if !svc.Config.IsScheduled() {
		http.Error(w, "Service is not a scheduled service", http.StatusBadRequest)
		return
	}

	// Check if already running (overlap prevention)
	if svc.IsRunning() {
		http.Error(w, "Service is already running", http.StatusConflict)
		return
	}

	// Start the service
	if err := svc.Start(); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "started"})
}

// streamLogs streams logs via WebSocket
func (s *Server) streamLogs(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	stream := r.PathValue("stream")

	if stream != "stdout" && stream != "stderr" {
		http.Error(w, "Stream must be stdout or stderr", http.StatusBadRequest)
		return
	}

	svc, err := s.serviceManager.GetService(name)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}

	// Upgrade to WebSocket
	conn, err := s.upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close()

	// Send historical logs
	var historyBytes []byte
	switch stream {
	case "stdout":
		historyBytes = svc.GetStdoutBuffer()
	case "stderr":
		historyBytes = svc.GetStderrBuffer()
	default:
		http.Error(w, "Stream must be stdout or stderr", http.StatusNotFound)
		return
	}

	if len(historyBytes) > 0 {
		if err := conn.WriteMessage(websocket.TextMessage, historyBytes); err != nil {
			return
		}
	}

	// Subscribe to live updates
	var ch chan string
	if stream == "stdout" {
		ch = svc.SubscribeStdout()
		defer svc.UnsubscribeStdout(ch)
	} else {
		ch = svc.SubscribeStderr()
		defer svc.UnsubscribeStderr(ch)
	}

	// Stream live logs
	for msg := range ch {
		if err := conn.WriteMessage(websocket.TextMessage, []byte(msg)); err != nil {
			return
		}
	}
}

// handleStatic serves static files from embedded filesystem
func (s *Server) handleStatic(w http.ResponseWriter, r *http.Request) {
	path := r.PathValue("path")
	if path == "" {
		path = "index.html"
	}

	// Get the embedded filesystem rooted at "static"
	staticFS, err := fs.Sub(staticFiles, "static")
	if err != nil {
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	// Open and serve the file directly
	file, err := staticFS.Open(path)
	if err != nil {
		http.Error(w, "File not found", http.StatusNotFound)
		return
	}
	defer file.Close()

	// Get file info for content type detection
	stat, err := file.Stat()
	if err != nil {
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	// Serve the file
	http.ServeContent(w, r, path, stat.ModTime(), file.(io.ReadSeeker))
}
