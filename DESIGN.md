# Service Manager - Design Document

## Overview
A simple service manager written in Go that manages multiple services defined in a YAML configuration file, captures their output to log files, and provides a web UI for management and live log viewing.

## Core Requirements
- Define services in YAML (command, arguments, working directory, environment variables, enabled flag)
- Capture stdout/stderr to separate log files per service
- Auto-start enabled services when manager starts
- Auto-restart services if they crash (respects enabled flag)
- Persistent enable/disable state via YAML `enabled` field
- Automatic config reload when `services.yaml` changes (5-second polling)
- Preserve service order as defined in YAML
- Web UI on port 4321 for service management
- Live log streaming (last ~10KB + real-time updates)
- Start/stop services via web UI (persists to YAML)
- Edit existing services and create new services via web UI
- Update YAML file while preserving comments and formatting

## Architecture

### Components

#### 1. Configuration Loader
- Reads `services.yaml` from the current directory
- Parses service definitions into structs with comment preservation (using yaml.v3 Node API)
- Validates configuration
- Updates YAML file when services are created/edited while preserving comments
- Provides `SetServiceEnabled()` to update the enabled flag in YAML
- `IsEnabled()` helper returns true if enabled field is nil or true (backwards compatibility)

#### 2. Service Manager
- Core orchestration component
- Maintains map of service name → service instance
- Maintains service order as defined in YAML (preserves insertion order)
- Starts enabled services on initialization
- Handles service lifecycle (start, stop, restart)
- Monitors service processes and auto-restarts on crash
- Watches config file for changes (5-second polling interval)
- Automatically reloads config when `services.yaml` is modified
- Tracks file modification time to avoid unnecessary reloads
- `StartService()` sets `enabled=true` in YAML and starts the process
- `StopService()` sets `enabled=false` in YAML and stops the process
- `RestartService()` restarts without changing enabled state

#### 3. Service Instance
- Represents a single managed service
- Manages the process (cmd.Cmd)
- Captures stdout/stderr to log files
- Provides status information (running, stopped, pid, uptime)
- Circular buffer for recent logs (~10KB)

#### 4. Log Manager
- Writes stdout/stderr to `logs/{service-name}-stdout.log` and `logs/{service-name}-stderr.log`
- Maintains in-memory circular buffer of recent logs for quick retrieval
- Supports real-time log streaming via channels

#### 5. Web Server
- HTTP server on port 4321
- REST API for service control
- WebSocket for live log streaming
- Static HTML/JS/CSS for UI

### Technology Stack
- **Language**: Go 1.21+
- **YAML parsing**: `gopkg.in/yaml.v3`
- **Web framework**: Standard library `net/http`
- **WebSockets**: `gorilla/websocket`
- **Frontend**: Vanilla JavaScript, HTML, CSS

## YAML Configuration Format

```yaml
services:
  - name: example-service
    command: /path/to/executable
    args:
      - --arg1
      - value1
    workdir: /path/to/working/directory
    env:
      KEY1: value1
      KEY2: value2
    enabled: true  # Optional: defaults to true

  - name: another-service
    command: python
    args:
      - script.py
    workdir: /app
    env:
      PYTHONUNBUFFERED: "1"
    enabled: false  # This service won't auto-start
```

### Configuration Fields
- `name` (required): Unique service identifier
- `command` (required): Executable path or command
- `args` (optional): List of command-line arguments
- `workdir` (optional): Working directory for the process
- `env` (optional): Environment variables as key-value pairs
- `enabled` (optional): Auto-start flag, defaults to `true` if omitted

## File Structure

```
service-manager/
├── main.go                 # Entry point
├── config/
│   └── config.go          # YAML loading and parsing
├── manager/
│   └── manager.go         # Service manager logic
├── service/
│   └── service.go         # Individual service instance
├── web/
│   ├── server.go          # HTTP server and API handlers
│   └── static/
│       ├── index.html     # Web UI
│       ├── style.css
│       └── app.js
├── services.yaml          # Service definitions
└── logs/                  # Created at runtime
    ├── service1-stdout.log
    ├── service1-stderr.log
    ├── service2-stdout.log
    └── service2-stderr.log
```

## API Endpoints

### REST API
- `GET /api/services` - List all services with status
- `GET /api/services/{name}` - Get service details (config + status)
- `POST /api/services` - Create a new service
- `PUT /api/services/{name}` - Update an existing service
- `DELETE /api/services/{name}` - Delete a service
- `POST /api/services/{name}/start` - Start a service
- `POST /api/services/{name}/stop` - Stop a service
- `POST /api/services/{name}/restart` - Restart a service

### WebSocket
- `WS /api/services/{name}/logs/{stream}` - Stream logs (stream = stdout or stderr)
  - Sends last ~10KB of logs on connect
  - Streams new logs in real-time

## Service Status Model

```go
type ServiceStatus struct {
    Name      string
    Running   bool
    PID       int
    Uptime    time.Duration
    Restarts  int
}
```

API responses also include the `enabled` field from the service configuration.

## Web UI Features

### Layout
- **Left Sidebar** (30% width):
  - List of all services in YAML order
  - Each service shows:
    - Service name
    - Status indicator (green dot = running, red dot = stopped)
    - Disabled services appear grayed out with italic text
    - Click to select and view details
  - "Create New Service" button at the top
  - Auto-refreshes every 2 seconds

- **Right Panel** (70% width):
  - When no service selected: Welcome message or instructions
  - When service selected:
    - **Service Info Section**:
      - Service name, status badge (running/stopped)
      - Stats: PID, uptime, restart count, auto-start (Yes/No)
      - Action buttons: Start, Stop, Restart, Delete
      - Edit button to toggle edit mode
    - **Edit Mode** (when Edit clicked):
      - Form fields for: command, args (one per line), workdir, env vars (key=value, one per line)
      - Save and Cancel buttons
    - **Log Viewer Section**:
      - Tabs for stdout/stderr
      - Auto-scrolling log display (~10KB history + live updates)
      - Live WebSocket streaming

### Create/Edit Service Flow
1. Click "Create New Service" or "Edit" button
2. Form appears with fields:
   - Service Name (required, disabled when editing)
   - Command (required)
   - Arguments (textarea, one arg per line)
   - Working Directory (optional)
   - Environment Variables (textarea, KEY=VALUE format, one per line)
3. Click "Save" → Updates YAML file and reloads config
4. New/updated service auto-starts
5. Form closes, service is selected in the list

## Process Flow

### Startup
1. Load `services.yaml` (creates empty config if not exists)
2. Create `logs/` directory if not exists
3. Initialize service manager
4. Load config and record file modification time
5. Start enabled services only
6. Start config file watcher (5-second polling)
7. Start web server on port 4321

### Service Start
1. Create log files for stdout/stderr
2. Set up working directory and environment
3. Start process with `exec.Command`
4. Attach stdout/stderr to log writers
5. Start goroutine to monitor process
6. Update service status

### Service Crash Detection
1. Monitor goroutine detects process exit
2. Log the crash
3. Wait 1 second (backoff)
4. Restart the service (only if enabled)
5. Increment restart counter

### Config File Watching
1. Background goroutine checks file every 5 seconds
2. Compare current modification time with last known time
3. If file is newer:
   - Reload configuration
   - Stop services removed from config
   - Restart services with changed config (if enabled)
   - Start new services (if enabled)
   - Preserve service order from YAML
   - Update modification time
4. UI automatically picks up changes (2-second polling + immediate updates on actions)

### Log Streaming
1. Client connects via WebSocket
2. Server sends last ~10KB from circular buffer
3. Server adds client to broadcast list
4. New logs broadcast to all connected clients
5. Client disconnects → remove from broadcast list

### Service Creation
1. Receive POST request with service config
2. Validate service config (name unique, command not empty)
3. Parse existing YAML preserving comments using yaml.v3 Node API
4. Add new service node to the services array
5. Write updated YAML back to file
6. Reload configuration
7. Start new service
8. Return success response

### Service Update
1. Receive PUT request with updated service config
2. Validate new config
3. Stop the existing service if running
4. Parse existing YAML preserving comments using yaml.v3 Node API
5. Find and update the service node in the services array
6. Write updated YAML back to file
7. Reload configuration (starts if enabled)
8. Return success response

### Service Start/Stop (Persistent)
- **Start**: Sets `enabled: true` in YAML, then starts the process
- **Stop**: Sets `enabled: false` in YAML, then stops the process
- **Restart**: Stops and starts without changing enabled flag
- Changes persist across service manager restarts

### Service Deletion
1. Receive DELETE request
2. Stop the service if running
3. Parse existing YAML preserving comments
4. Remove service node from services array
5. Write updated YAML back to file
6. Reload configuration
7. Return success response

## Error Handling
- Invalid YAML on startup: Log error and exit
- Service start failure: Log error, mark as stopped, retry after 1s
- Log file write failure: Log to stderr, continue running
- WebSocket disconnect: Clean up, remove from broadcast list
- Service creation/update with invalid config: Return 400 Bad Request with error details
- Duplicate service name on creation: Return 409 Conflict
- Update/delete non-existent service: Return 404 Not Found
- YAML write failure: Return 500 Internal Server Error, rollback not attempted (keep existing file)

## Implementation Notes
- Use `sync.RWMutex` for concurrent access to service map and order slice
- Use channels for graceful shutdown of config watcher
- Circular buffer: Fixed-size ring buffer (10KB)
- Process monitoring: Use `cmd.Wait()` to detect exit
- All services run in separate goroutines
- Config watcher runs in background goroutine with 5-second ticker
- Web server runs in main goroutine with graceful shutdown support
- **Service Order Preservation**: Maintain separate slice to preserve YAML order
  - Services map provides O(1) lookup by name
  - Order slice provides ordered iteration for UI display
- **YAML Comment Preservation**: Use `yaml.v3` Node API for parsing/encoding
  - Decode to `yaml.Node` instead of structs for modifications
  - Traverse/modify nodes directly
  - Encode back to preserve all formatting and comments
  - Helper functions: `AddService()`, `UpdateService()`, `DeleteService()`, `SetServiceEnabled()`
- **Enabled Flag Handling**:
  - Pointer to bool allows nil = enabled (backwards compatibility)
  - `IsEnabled()` helper treats nil and true as enabled
  - Start/Stop operations update YAML immediately
  - Auto-restart respects enabled flag
