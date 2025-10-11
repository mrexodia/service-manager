# Service Manager - Design Document

## Overview
A simple service manager written in Go that manages multiple services defined in a YAML configuration file, captures their output to log files, and provides a web UI for management and live log viewing.

## Core Requirements
- Define services in YAML (command, arguments, working directory, environment variables, enabled flag)
- **Two service types**:
  - **Continuous services**: Long-running processes with auto-restart on crash
  - **Scheduled services**: Cron-based task scheduling (optional `schedule` field)
- Capture stdout/stderr to separate log files per service
- Auto-start enabled services when manager starts
- Auto-restart services if they crash (continuous services only, respects enabled flag)
- Cron scheduling with standard 5-field syntax (minute, hour, day, month, weekday)
- Overlap prevention for scheduled services (skip run if previous still executing)
- Track last run time, exit code, and duration for scheduled services
- Persistent enable/disable state via YAML `enabled` field
- Automatic config reload when `services.yaml` changes (5-second polling)
- Preserve service order as defined in YAML
- Configurable web UI host and port (default 127.0.0.1:4321)
- HTTP Basic Authorization support for API endpoints
- Webhook notifications for repeated service failures (configurable threshold)
- Live log streaming (last ~10KB + real-time updates)
- Start/stop services via web UI (persists to YAML)
- Edit existing services and create new services via web UI
- Update YAML file while preserving comments and formatting

## Architecture

All components are implemented in the `main` package in the root directory for simplicity.

### Components

#### 1. Configuration Loader (`config.go`)
- Reads `services.yaml` from the current directory
- Parses global config and service definitions with comment preservation (using yaml.v3 Node API)
- Validates configuration
- Updates YAML file when services are created/edited while preserving comments
- Provides `SetServiceEnabled()` to update the enabled flag in YAML
- `IsEnabled()` helper returns true if enabled field is nil or true (backwards compatibility)
- Supports global configuration: host, port, failure_webhook_url, failure_retries, authorization

#### 2. Service Manager (`manager.go`)
- Core orchestration component
- Maintains map of service name → service instance
- Maintains service order as defined in YAML (preserves insertion order)
- Starts enabled services on initialization (continuous or schedules cron jobs)
- Handles service lifecycle (start, stop, restart)
- Monitors continuous service processes and auto-restarts on crash
- Manages cron scheduler for scheduled services
- Watches config file for changes (5-second polling interval)
- Automatically reloads config when `services.yaml` is modified
- Tracks file modification time to avoid unnecessary reloads
- Integrates webhook notifier for service failure notifications
- `StartService()` sets `enabled=true` in YAML and starts the process (or registers cron)
- `StopService()` sets `enabled=false` in YAML and stops the process (or unregisters cron)
- `RestartService()` restarts without changing enabled state (continuous only)
- `RunNow()` immediately runs a scheduled service (409 Conflict if already running)

#### 3. Service Instance (`service.go`)
- Represents a single managed service
- **For continuous services**:
  - Manages the process (cmd.Cmd)
  - Captures stdout/stderr to log files
  - Provides status information (running, stopped, pid, uptime)
  - Auto-restart on crash (if enabled)
  - Tracks consecutive failure count for webhook notifications
- **For scheduled services**:
  - Registers cron job with scheduler
  - Tracks next run time, last run time, last exit code, last duration
  - Prevents overlapping runs
  - Runs to completion (no auto-restart)
  - Logs start/exit events to stderr
- Circular buffer for recent logs (~10KB) for both types

#### 4. Cron Scheduler (integrated in `manager.go`)
- Built using `github.com/robfig/cron/v3` library
- Parses standard cron expressions (5 fields: minute, hour, day, month, weekday)
- Registers scheduled services as cron jobs
- Handles cron job execution by calling service run method
- Provides next run time calculation
- Supports job removal on service stop/delete
- Thread-safe job registration/unregistration

#### 5. Log Manager (integrated in `service.go`)
- Writes stdout/stderr to `logs/{service-name}-stdout.log` and `logs/{service-name}-stderr.log`
- Maintains in-memory circular buffer of recent logs for quick retrieval
- Supports real-time log streaming via channels

#### 6. Web Server (`server.go`)
- HTTP server on configurable host:port (default 127.0.0.1:4321)
- REST API for service control using `http.ServeMux` with middleware
- HTTP Authorization support (configurable via services.yaml)
- WebSocket for live log streaming
- Static HTML/JS/CSS for UI

#### 7. Webhook Notifier (`webhook.go`)
- Sends HTTP POST notifications when services fail repeatedly
- Configurable webhook URL and failure threshold (default: 3 consecutive failures)
- JSON payload includes service name, timestamp, failure count, exit code
- 10-second timeout for webhook requests
- Only triggers for continuous services that crash repeatedly

### Technology Stack
- **Language**: Go 1.21+
- **YAML parsing**: `gopkg.in/yaml.v3`
- **Cron scheduling**: `github.com/robfig/cron/v3`
- **Web framework**: Standard library `net/http`
- **WebSockets**: `gorilla/websocket`
- **Frontend**: Vanilla JavaScript, HTML, CSS

## YAML Configuration Format

```yaml
# Global configuration (all fields optional)
config:
  host: "127.0.0.1"                      # Web UI bind address (default: 127.0.0.1)
  port: 4321                             # Web UI port (default: 4321)
  failure_webhook_url: ""                # Webhook URL for failure notifications (empty = disabled)
  failure_retries: 3                     # Consecutive failures before webhook triggers (default: 3)
  authorization: "username:password"     # HTTP Basic Auth for API (empty = disabled)

services:
  # Continuous service (long-running)
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

  # Scheduled service (cron-based)
  - name: cleanup-job
    command: /path/to/cleanup
    args:
      - --deep-clean
    schedule: "0 2 * * *"  # Daily at 2:00 AM
    enabled: true

  - name: another-service
    command: python
    args:
      - script.py
    workdir: /app
    env:
      PYTHONUNBUFFERED: "1"
    enabled: false  # This service won't auto-start
```

### Global Configuration Fields
- `host` (optional): Web UI bind address, defaults to `127.0.0.1`
- `port` (optional): Web UI port, defaults to `4321`
- `failure_webhook_url` (optional): Webhook URL for failure notifications, empty/omitted disables webhooks
- `failure_retries` (optional): Number of consecutive failures before webhook triggers, defaults to `3`
- `authorization` (optional): HTTP Basic Auth credentials in `username:password` format, empty/omitted disables auth

### Service Configuration Fields
- `name` (required): Unique service identifier
- `command` (required): Executable path or command
- `args` (optional): List of command-line arguments
- `workdir` (optional): Working directory for the process
- `env` (optional): Environment variables as key-value pairs
- `enabled` (optional): Auto-start flag, defaults to `true` if omitted
- `schedule` (optional): Cron expression (5 fields: minute, hour, day, month, weekday). Presence of this field makes it a scheduled service instead of continuous.

## File Structure

```
service-manager/
├── main.go                # Entry point, initialization, shutdown
├── config.go              # YAML loading, parsing, and config updates
├── manager.go             # Service manager, lifecycle orchestration
├── service.go             # Individual service instances, process management
├── server.go              # HTTP server, REST API, WebSocket handlers
├── webhook.go             # Webhook notifications for service failures
├── web/
│   └── static/
│       ├── index.html     # Web UI
│       ├── style.css      # UI styles
│       ├── app.js         # UI logic
│       └── favicon.ico    # Icon for web UI and Windows executable
├── services.yaml          # Service definitions and global config
├── rsrc.syso              # Windows resource file (generated, contains embedded icon)
└── logs/                  # Created at runtime
    ├── service1-stdout.log
    ├── service1-stderr.log
    ├── service2-stdout.log
    └── service2-stderr.log
```

## Building

### Standard Build

```bash
go build
```

### Windows Executable Icon

The Windows executable icon is embedded from `web/static/favicon.ico` using the `rsrc.syso` resource file. This file is already generated and included in the repository.

If you need to change the icon:

```bash
# Install rsrc tool (one-time setup)
go install github.com/akavel/rsrc@latest

# Generate resource file from favicon.ico
rsrc -ico web/static/favicon.ico -o rsrc.syso

# Build normally (rsrc.syso is automatically included)
go build
```

The `rsrc.syso` file is automatically detected and linked by the Go compiler during Windows builds.

## API Endpoints

### REST API
- `GET /api/services` - List all services with status
- `GET /api/services/{name}` - Get service details (config + status)
- `POST /api/services` - Create a new service
- `PUT /api/services/{name}` - Update an existing service
- `DELETE /api/services/{name}` - Delete a service
- `POST /api/services/{name}/start` - Start a service (or register cron for scheduled)
- `POST /api/services/{name}/stop` - Stop a service (or unregister cron for scheduled)
- `POST /api/services/{name}/restart` - Restart a service (continuous only)
- `POST /api/services/{name}/run-now` - Immediately run a scheduled service (409 if already running)

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
    Uptime    time.Duration  // For continuous services
    Restarts  int            // For continuous services

    // For scheduled services
    Schedule      string
    NextRunTime   *time.Time
    LastRunTime   *time.Time
    LastExitCode  *int
    LastDuration  *time.Duration  // In milliseconds
}
```

API responses also include the `enabled` field from the service configuration.

## Web UI Features

### Layout
- **Left Sidebar** (30% width):
  - List of all services in YAML order
  - Each service shows:
    - Service name
    - **Continuous services**: Status dot (green = running, red = stopped)
    - **Scheduled services**: Clock icon (⏰) with orange left border
    - Disabled services appear grayed out with italic text
    - Click to select and view details
  - "Create New Service" button at the top
  - Auto-refreshes every 2 seconds

- **Right Panel** (70% width):
  - When no service selected: Welcome message or instructions
  - When service selected:
    - **Service Info Section**:
      - Service name, status badge
        - **Continuous**: Running (green) / Stopped (red)
        - **Scheduled**: Scheduled (orange) / Running (green) / Disabled (red)
      - **Stats for continuous services**: PID, uptime, restart count, auto-start (Yes/No)
      - **Stats for scheduled services**: Schedule, Next Run, Last Run, Last Exit Code, Last Duration
      - **Action buttons**:
        - **Continuous**: Start, Stop, Restart, Edit, Delete
        - **Scheduled**: Run Now, Enable/Disable toggle, Edit, Delete
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
2. Parse global config (host, port, webhook URL, failure retries, authorization)
3. Create `logs/` directory if not exists
4. Initialize service manager with cron scheduler and webhook notifier
5. Load service definitions and record file modification time
6. Start enabled services (continuous processes or register cron jobs)
7. Start config file watcher (5-second polling)
8. Start web server on configured host:port with optional authorization

### Service Start
1. Create log files for stdout/stderr
2. Set up working directory and environment
3. Start process with `exec.Command`
4. Attach stdout/stderr to log writers
5. Start goroutine to monitor process
6. Update service status

### Service Crash Detection (Continuous Services Only)
1. Monitor goroutine detects process exit
2. Log the crash and capture exit code
3. Increment consecutive failure counter
4. If consecutive failures >= configured threshold, send webhook notification
5. Wait 1 second (backoff)
6. Restart the service (only if enabled)
7. Increment restart counter
8. Reset consecutive failure counter on successful start

### Scheduled Service Execution
1. Cron scheduler triggers at scheduled time
2. Check if service is already running (overlap prevention)
3. If running, skip this execution and log warning to stderr
4. If not running:
   - Record start time
   - Log "Starting scheduled run" to stderr
   - Start process with configured command/args/env/workdir
   - Wait for process to complete
   - Record end time, exit code, duration
   - Log "Exited with code X" to stderr
   - Update last run statistics (time, exit code, duration)
5. Calculate and update next run time

### Config File Watching
1. Background goroutine checks file every 5 seconds
2. Compare current modification time with last known time
3. If file is newer:
   - Reload configuration
   - Stop/unregister services removed from config
   - Restart/reschedule services with changed config (if enabled)
   - Start/register new services (if enabled)
   - For scheduled services: unregister old cron job, register new one if schedule changed
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
- **Start**: Sets `enabled: true` in YAML, then starts the process (or registers cron job)
- **Stop**: Sets `enabled: false` in YAML, then stops the process (or unregisters cron job)
- **Restart**: Stops and starts without changing enabled flag (continuous services only)
- **Run Now**: Immediately runs a scheduled service (returns 409 if already running)
- Changes persist across service manager restarts

### Service Deletion
1. Receive DELETE request
2. Stop the service if running
3. Parse existing YAML preserving comments
4. Remove service node from services array
5. Write updated YAML back to file
6. Reload configuration
7. Return success response

### Webhook Notification
1. Service crashes and consecutive failure count reaches threshold
2. Build JSON payload with service name, timestamp, failure count, exit code
3. Send HTTP POST request to configured webhook URL (10-second timeout)
4. Log success or failure of webhook delivery
5. Webhook failures are logged but don't affect service management
6. Subsequent successful starts reset the failure counter

## Error Handling
- Invalid YAML on startup: Log error and exit
- Invalid cron expression: Log error and mark service as disabled
- Service start failure: Log error, mark as stopped, retry after 1s (continuous only)
- Scheduled service overlap: Skip run, log warning to stderr
- Run-now on already running scheduled service: Return 409 Conflict
- Restart on scheduled service: Return 400 Bad Request (not supported)
- Log file write failure: Log to stderr, continue running
- WebSocket disconnect: Clean up, remove from broadcast list
- Service creation/update with invalid config: Return 400 Bad Request with error details
- Duplicate service name on creation: Return 409 Conflict
- Update/delete non-existent service: Return 404 Not Found
- YAML write failure: Return 500 Internal Server Error, rollback not attempted (keep existing file)
- Webhook delivery failure: Log error, continue service management (non-blocking)
- Invalid authorization credentials: Return 401 Unauthorized for API requests
- Missing authorization header when auth enabled: Return 401 Unauthorized

## Implementation Notes
- Use `sync.RWMutex` for concurrent access to service map and order slice
- Use channels for graceful shutdown of config watcher and cron scheduler
- Circular buffer: Fixed-size ring buffer (10KB)
- Process monitoring: Use `cmd.Wait()` to detect exit
- All continuous services run in separate goroutines
- Scheduled services execute synchronously in cron job handler
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
  - Auto-restart respects enabled flag (continuous services only)
- **Cron Scheduling**:
  - Use `robfig/cron/v3` with 5-field parser
  - Each scheduled service gets unique cron entry ID for removal
  - Cron jobs call service run method which handles overlap prevention
  - Next run time calculated from cron schedule
  - Scheduler started on manager initialization, stopped on shutdown
- **Webhook Notifications**:
  - Only triggered for continuous services that crash repeatedly
  - Consecutive failure counter tracked per service instance
  - Counter resets on successful service start
  - Webhook requests have 10-second timeout
  - Failures logged but non-blocking to service management
- **HTTP Authorization**:
  - Basic Auth middleware applied to all API endpoints
  - Authorization header format: `Authorization: Basic base64(username:password)`
  - Static files (web UI) served without authentication
  - Disabled when authorization field empty or omitted in config
