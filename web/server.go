package web

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/gorilla/websocket"
	"service-manager/config"
	"service-manager/manager"
)

// Server represents the web server
type Server struct {
	manager  *manager.Manager
	host     string
	port     int
	upgrader websocket.Upgrader
}

// New creates a new web server
func New(mgr *manager.Manager, host string, port int) *Server {
	s := &Server{
		manager: mgr,
		host:    host,
		port:    port,
	}

	// Configure WebSocket upgrader with proper CORS checking
	s.upgrader = websocket.Upgrader{
		CheckOrigin: func(r *http.Request) bool {
			// In production, you should check against allowed origins
			// For now, allow same-origin and localhost for development
			origin := r.Header.Get("Origin")
			if origin == "" {
				return true // Allow no-origin (same-origin) requests
			}

			// Allow localhost and same host connections with configured port
			host := r.Host
			portStr := fmt.Sprintf("%d", port)
			return origin == "http://"+host ||
			       origin == "https://"+host ||
			       origin == "http://localhost" ||
			       origin == "http://localhost:"+portStr ||
			       origin == "http://127.0.0.1:"+portStr
		},
	}

	return s
}

// Start starts the web server
func (s *Server) Start() error {
	http.HandleFunc("/api/services", s.handleServices)
	http.HandleFunc("/api/services/", s.handleServiceActions)
	http.HandleFunc("/", s.handleStatic)

	addr := fmt.Sprintf("%s:%d", s.host, s.port)
	fmt.Printf("Starting web server on http://%s\n", addr)
	return http.ListenAndServe(addr, nil)
}

// handleServices handles GET /api/services (list all) and POST /api/services (create)
func (s *Server) handleServices(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		s.listServices(w, r)
	case http.MethodPost:
		s.createService(w, r)
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

// handleServiceActions handles service-specific actions
func (s *Server) handleServiceActions(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/api/services/")
	parts := strings.Split(path, "/")

	if len(parts) == 0 || parts[0] == "" {
		http.Error(w, "Service name required", http.StatusBadRequest)
		return
	}

	serviceName := parts[0]

	// Handle different endpoints
	if len(parts) == 1 {
		// /api/services/{name}
		switch r.Method {
		case http.MethodGet:
			s.getService(w, r, serviceName)
		case http.MethodPut:
			s.updateService(w, r, serviceName)
		case http.MethodDelete:
			s.deleteService(w, r, serviceName)
		default:
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		}
		return
	}

	if len(parts) == 2 {
		action := parts[1]
		switch action {
		case "start":
			s.startService(w, r, serviceName)
		case "stop":
			s.stopService(w, r, serviceName)
		case "restart":
			s.restartService(w, r, serviceName)
		case "enable":
			s.enableService(w, r, serviceName)
		case "disable":
			s.disableService(w, r, serviceName)
		case "run-now":
			s.runNowService(w, r, serviceName)
		default:
			http.Error(w, "Unknown action", http.StatusNotFound)
		}
		return
	}

	if len(parts) == 3 && parts[1] == "logs" {
		// /api/services/{name}/logs/{stream}
		stream := parts[2]
		s.streamLogs(w, r, serviceName, stream)
		return
	}

	http.Error(w, "Not found", http.StatusNotFound)
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
func (s *Server) getService(w http.ResponseWriter, r *http.Request, name string) {
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
func (s *Server) updateService(w http.ResponseWriter, r *http.Request, name string) {
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
func (s *Server) deleteService(w http.ResponseWriter, r *http.Request, name string) {
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
func (s *Server) startService(w http.ResponseWriter, r *http.Request, name string) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if err := s.manager.StartService(name); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "started"})
}

// stopService stops a service
func (s *Server) stopService(w http.ResponseWriter, r *http.Request, name string) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if err := s.manager.StopService(name); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "stopped"})
}

// restartService restarts a service
func (s *Server) restartService(w http.ResponseWriter, r *http.Request, name string) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if err := s.manager.RestartService(name); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "restarted"})
}

// enableService enables a service
func (s *Server) enableService(w http.ResponseWriter, r *http.Request, name string) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if err := s.manager.EnableService(name); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "enabled"})
}

// disableService disables a service
func (s *Server) disableService(w http.ResponseWriter, r *http.Request, name string) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if err := s.manager.DisableService(name); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "disabled"})
}

// runNowService runs a scheduled service immediately
func (s *Server) runNowService(w http.ResponseWriter, r *http.Request, name string) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

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
func (s *Server) streamLogs(w http.ResponseWriter, r *http.Request, serviceName, stream string) {
	if stream != "stdout" && stream != "stderr" {
		http.Error(w, "Stream must be stdout or stderr", http.StatusBadRequest)
		return
	}

	svc, err := s.manager.GetService(serviceName)
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
	if stream == "stdout" {
		historyBytes = svc.GetStdoutBuffer()
	} else {
		historyBytes = svc.GetStderrBuffer()
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
	if r.URL.Path == "/" {
		http.ServeFile(w, r, "web/static/index.html")
		return
	}

	http.ServeFile(w, r, "web/static"+r.URL.Path)
}
