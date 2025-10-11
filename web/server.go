package web

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/gorilla/websocket"
	"github.com/mrexodia/service-manager/config"
	"github.com/mrexodia/service-manager/manager"
)

// Server represents the web server
type Server struct {
	manager  *manager.Manager
	host     string
	port     int
	upgrader websocket.Upgrader
	username string // BasicAuth username (empty = no username required)
	password string // BasicAuth password (empty = no auth)
}

// New creates a new web server
func New(mgr *manager.Manager) *Server {
	// Parse authorization config once
	var username, password string
	cfg := mgr.GetGlobalConfig()
	if cfg.Authorization != "" {
		if idx := strings.Index(cfg.Authorization, ":"); idx > 0 {
			username = cfg.Authorization[:idx]
			password = cfg.Authorization[idx+1:]
		} else {
			password = cfg.Authorization
		}
	}

	return &Server{
		manager:  mgr,
		host:     cfg.Host,
		port:     cfg.Port,
		upgrader: websocket.Upgrader{},
		username: username,
		password: password,
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
	services := s.manager.GetAllServices()

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
			if nextRun, ok := s.manager.GetNextRunTime(status.Name); ok {
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
	svc, err := s.manager.GetService(name)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}

	status := svc.GetStatus()
	response := map[string]interface{}{
		"name":         svc.Config.Name,
		"command":      svc.Config.Command,
		"args":         svc.Config.Args,
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
		if nextRun, ok := s.manager.GetNextRunTime(svc.Config.Name); ok {
			response["nextRunTime"] = nextRun
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(response)
}

// createService creates a new service
func (s *Server) createService(w http.ResponseWriter, r *http.Request) {
	var cfg config.ServiceConfig
	if err := json.NewDecoder(r.Body).Decode(&cfg); err != nil {
		http.Error(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	if cfg.Name == "" || cfg.Command == "" {
		http.Error(w, "Name and command are required", http.StatusBadRequest)
		return
	}

	if err := s.manager.CreateService(cfg); err != nil {
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
	var cfg config.ServiceConfig
	if err := json.NewDecoder(r.Body).Decode(&cfg); err != nil {
		http.Error(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	if cfg.Command == "" {
		http.Error(w, "Command is required", http.StatusBadRequest)
		return
	}

	// Ensure name matches URL
	cfg.Name = name

	if err := s.manager.UpdateService(name, cfg); err != nil {
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
	if err := s.manager.DeleteService(name); err != nil {
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

// startService starts a service
func (s *Server) startService(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if err := s.manager.StartService(name); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "started"})
}

// stopService stops a service
func (s *Server) stopService(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if err := s.manager.StopService(name); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "stopped"})
}

// restartService restarts a service
func (s *Server) restartService(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if err := s.manager.RestartService(name); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "restarted"})
}

// enableService enables a service
func (s *Server) enableService(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if err := s.manager.EnableService(name); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "enabled"})
}

// disableService disables a service
func (s *Server) disableService(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if err := s.manager.DisableService(name); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "disabled"})
}

// runNowService runs a scheduled service immediately
func (s *Server) runNowService(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	svc, err := s.manager.GetService(name)
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

	svc, err := s.manager.GetService(name)
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

// handleStatic serves static files
func (s *Server) handleStatic(w http.ResponseWriter, r *http.Request) {
	path := r.PathValue("path")
	if path == "" {
		http.ServeFile(w, r, "web/static/index.html")
		return
	}

	http.ServeFile(w, r, "web/static/"+path)
}
