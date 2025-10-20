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

### Event-Based Architecture

The system uses an **event-based architecture** where ConfigManager is the **single source of truth** for detecting changes and notifying listeners. This ensures consistent behavior whether changes come from external file edits or API calls.

#### Key Principles:
1. **ConfigManager** watches `services.yaml` and is the only component that detects changes
2. **ServiceManager** implements the `ConfigListener` interface and reacts to configuration events
3. **Both paths converge**: API changes save to disk → trigger reload → emit event
4. **Global config immutable**: Loaded once at startup, not watched for runtime changes (requires restart)

### Event Flow

**External File Edit:**
```
User edits services.yaml → Watcher detects (modtime + checksum) → reloadAndNotify()
→ OnServicesUpdated(services, toKill) → ServiceManager handles event
```

**API Call:**
```
configMgr.AddService() → Saves to disk → Triggers reload → reloadAndNotify()
→ OnServicesUpdated(services, toKill) → ServiceManager handles event
```

### Components

#### 1. Configuration Manager (`config.go`)
- **Single source of truth** for configuration changes
- Loads global config and services from `services.yaml` (RootConfig structure)
- `LoadGlobalConfig()`: Loads global config once at startup (immutable at runtime)
- `NewConfigManager()`: Creates manager without listener (listener passed to StartWatching)
- `StartWatching()`: Loads initial config, emits initial state, starts background watcher
- **Only the watcher emits events** via `OnServicesUpdated()` callback
- **Critical: `cm.services` is NEVER mutated by API methods** - it represents the OLD state for comparison
- API methods use **copy-modify-save pattern**:
  1. Create a copy of services: `modifiedServices := cm.copyServices()`
  2. Modify the copy (NOT cm.services)
  3. Save the copy to disk
  4. Trigger reload via channel → watcher handles notification
- Uses polling (5s interval) + checksum verification to detect external changes
- Uses cooldown (2s) to avoid excessive reloads
- Preserves global config fields when saving service changes
- `RootConfig` struct handles combined format (global fields + services array)
- Uses `yaml.CommentMap` for automatic comment preservation (no manual AST manipulation)

#### 2. Service Manager (`manager.go`)
- Implements `ConfigListener` interface
- **Reacts to configuration events**, doesn't initiate config loading
- Maintains map of service name → service instance
- Maintains service order as defined in YAML (preserves insertion order)
- `OnServicesUpdated(services, toKill)`: Handles configuration change events
  - Stops services in `toKill` list
  - Removes services no longer in config
  - Creates/updates services based on new config
- Initialized with global config (passed to constructor, immutable)
- Handles service lifecycle (start, stop, restart)
- Monitors continuous service processes and auto-restarts on crash
- Manages cron scheduler for scheduled services
- Integrates webhook notifier for service failure notifications
- `StartService()`, `StopService()`, `RestartService()`: Runtime control only (don't modify YAML)
- Services are enabled/disabled via ConfigManager API, which triggers config reload

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
- **YAML parsing**: `github.com/goccy/go-yaml` (with automatic comment preservation via CommentMap)
- **Command parsing**: `github.com/google/shlex` (for parsing command strings with arguments)
- **Cron scheduling**: `github.com/robfig/cron/v3`
- **Web framework**: Standard library `net/http`
- **WebSockets**: `gorilla/websocket`
- **Frontend**: Vanilla JavaScript, HTML, CSS

## YAML Configuration Format

All configuration is in a single `services.yaml` file with global config at the top level and services in a `services:` array.

```yaml
# Global configuration (all fields optional, at top level)
host: "127.0.0.1"                      # Web UI bind address (default: 127.0.0.1)
port: 4321                             # Web UI port (default: 4321)
failure_webhook_url: ""                # Webhook URL for failure notifications (empty = disabled)
failure_retries: 3                     # Consecutive failures before webhook triggers (default: 3)
authorization: "username:password"     # HTTP Basic Auth for API (empty = disabled)

services:
  # Continuous service (long-running)
  - name: example-service
    command: "/path/to/executable --arg1 value1"
    workdir: /path/to/working/directory
    env:
      KEY1: value1
      KEY2: value2
    enabled: true  # Optional: defaults to true

  # Scheduled service (cron-based)
  - name: cleanup-job
    command: "/path/to/cleanup --deep-clean"
    schedule: "0 2 * * *"  # Daily at 2:00 AM
    enabled: true

  - name: another-service
    command: "python script.py"
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
- `command` (required): Full command with arguments (e.g. `"python -u server.py"` or `"/usr/bin/node app.js --port 3000"`)
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
   - Command (required, full command with arguments e.g. "python -u server.py")
   - Working Directory (optional)
   - Environment Variables (textarea, KEY=VALUE format, one per line)
3. Click "Save" → Updates YAML file and reloads config
4. New/updated service auto-starts
5. Form closes, service is selected in the list

## Process Flow

### Startup (Event-Based)
1. `LoadGlobalConfig("services.yaml")` - Load global config once (immutable)
2. `NewServiceManager(globalConfig)` - Create manager with global config
3. `NewConfigManager("services.yaml")` - Create config manager
4. `configMgr.StartWatching(ctx, mgr)` - Load services, emit initial state, start watcher
   - Loads services from disk (or creates empty if not exists)
   - Emits `OnServicesUpdated(services, [])` to manager
   - ServiceManager creates/starts all enabled services
   - Starts background watcher goroutine (5-second polling)
5. `NewServer(mgr, configMgr)` - Create web server with both managers
6. Start web server on configured host:port with optional authorization
7. Wait for shutdown signal (Ctrl+C)
8. Graceful shutdown: stop watcher, stop all services, stop cron scheduler

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

### Config File Watching (Event-Based)
1. Background watcher goroutine in ConfigManager runs continuously
2. Two triggers for checking changes:
   - Timer tick every 5 seconds (regular polling)
   - Reload channel signal (immediate after API changes)
3. Change detection (`needsReload()`):
   - Compare file modification time with last known time
   - If newer, calculate SHA256 checksum
   - If checksum differs from last known, file content actually changed
   - If only modtime changed, update modtime and skip reload
4. When change detected (`reloadAndNotify()`):
   - Load services from disk (parses RootConfig, extracts services)
   - Calculate `toKill` list: services deleted or modified
   - Update internal state (services, modtime, checksum)
   - **Emit event**: `listener.OnServicesUpdated(services, toKill)`
5. ServiceManager handles event (`OnServicesUpdated()`):
   - Stop and remove services in `toKill` list
   - Remove services no longer in config
   - Create new services (start if enabled)
   - Update config reference for existing services
   - Preserve service order from YAML
6. UI automatically picks up changes (2-second polling + immediate updates on actions)
7. **Cooldown**: 2-second cooldown prevents excessive reloads from rapid external changes

### Log Streaming
1. Client connects via WebSocket
2. Server sends last ~10KB from circular buffer
3. Server adds client to broadcast list
4. New logs broadcast to all connected clients
5. Client disconnects → remove from broadcast list

### Service Creation (Event-Based Flow)
1. Server receives POST request → `configMgr.AddService(config)`
2. ConfigManager validates (name unique, command not empty)
3. ConfigManager adds service to internal list
4. ConfigManager saves to disk (preserves global config)
5. ConfigManager triggers reload via channel
6. Watcher calls `reloadAndNotify()` → emits `OnServicesUpdated()`
7. ServiceManager receives event, creates and starts new service
8. Return success response to client

### Service Update (Event-Based Flow)
1. Server receives PUT request → `configMgr.UpdateService(name, config)`
2. ConfigManager validates new config
3. ConfigManager updates service in internal list
4. ConfigManager saves to disk (preserves global config)
5. ConfigManager triggers reload via channel
6. Watcher calls `reloadAndNotify()` → calculates `toKill` (includes modified service)
7. ServiceManager receives event:
   - Stops old instance (in `toKill` list)
   - Creates new instance with updated config
   - Starts if enabled
8. Return success response to client

### Service Enable/Disable (Event-Based Flow)
1. Server receives POST to enable/disable → `configMgr.SetServiceEnabled(name, enabled)`
2. ConfigManager updates enabled flag in internal service config
3. ConfigManager saves to disk (preserves global config)
4. ConfigManager triggers reload via channel
5. Watcher calls `reloadAndNotify()` → calculates `toKill` (includes service if disabled or config changed)
6. ServiceManager receives event and applies changes
7. Return success response to client

### Service Deletion (Event-Based Flow)
1. Server receives DELETE request → `configMgr.DeleteService(name)`
2. ConfigManager removes service from internal list
3. ConfigManager saves to disk (preserves global config)
4. ConfigManager triggers reload via channel
5. Watcher calls `reloadAndNotify()` → includes service in `toKill` list
6. ServiceManager receives event, stops and removes service
7. Return success response to client

### Runtime Control (Non-Persistent)
These operations don't modify YAML, only control runtime state:
- **StartService()**: Start service process (continuous) or run now (scheduled)
- **StopService()**: Stop service process temporarily
- **RestartService()**: Stop and start (continuous only)
- **RunNow()**: Immediately run scheduled service (409 if already running)
- Changes don't persist across service manager restarts

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

### Concurrency & Threading
- Use `sync.RWMutex` for concurrent access to service map and order slice (ServiceManager)
- Use `sync.RWMutex` for concurrent access to services list (ConfigManager)
- Use channels for graceful shutdown of config watcher and cron scheduler
- Config watcher runs in background goroutine with 5-second ticker + reload channel
- All continuous services run in separate goroutines
- Scheduled services execute synchronously in cron job handler
- Web server runs in main goroutine with graceful shutdown support

### Event-Based Architecture
- **ConfigListener Interface**: Single method `OnServicesUpdated(services, toKill)`
- **Listener passed to StartWatching()**: Decouples initialization from listener registration
- **Only watcher emits events**: API methods save to disk and trigger reload, watcher handles notification
- **Consistent event flow**: External edits and API changes both go through watcher
- **Cooldown mechanism**: 2-second cooldown prevents excessive reloads from rapid changes
- **Immediate reload channel**: API changes trigger immediate reload (bypasses cooldown)

### Configuration Management
- **Single file**: `services.yaml` contains both global config and services
- **RootConfig structure**: `GlobalConfig` embedded inline + `Services []ServiceConfig`
- **Global config immutable**: Loaded once at startup, requires restart to change
- **Service config mutable**: Watched for changes via polling + checksum
- **Change detection**: Modtime check first, then SHA256 checksum if newer
- **Atomic writes**: Write to temp file, then rename (atomic on POSIX systems)
- **Preserve global config**: When saving services, read existing file to preserve global fields
- **Comment preservation**: Automatic via `yaml.CommentMap` - no manual AST manipulation needed

### State Management in ConfigManager
- **cm.services is read-only for API methods** - represents the OLD state
- **API methods use copy-modify-save pattern**:
  1. Copy current services
  2. Modify the copy
  3. Save copy to disk
  4. Trigger reload
- **Only reloadAndNotify() updates cm.services** after comparing old vs new
- This allows proper change detection and `toKill` calculation
- The watcher compares old state (cm.services) with new state (from disk) to determine which services need to be stopped

### Service Management
- **Service Order Preservation**: Maintain separate slice to preserve YAML order
  - Services map provides O(1) lookup by name
  - Order slice provides ordered iteration for UI display
- **Enabled Flag Handling**:
  - Pointer to bool allows nil = enabled (backwards compatibility)
  - `IsEnabled()` helper treats nil and true as enabled
  - Enable/disable operations update YAML and trigger reload
  - Auto-restart respects enabled flag (continuous services only)
- **Circular buffer**: Fixed-size ring buffer (10KB) for recent logs
- **Process monitoring**: Use `cmd.Wait()` to detect exit

### Cron Scheduling
- Use `robfig/cron/v3` with 5-field parser
- Each scheduled service gets unique cron entry ID for removal
- Cron jobs call service run method which handles overlap prevention
- Next run time calculated from cron schedule
- Scheduler started on manager initialization, stopped on shutdown

### Webhook Notifications
- Only triggered for continuous services that crash repeatedly
- Consecutive failure counter tracked per service instance
- Counter resets on successful service start
- Webhook requests have 10-second timeout
- Failures logged but non-blocking to service management

### HTTP Authorization
- Basic Auth middleware applied to all API endpoints
- Authorization header format: `Authorization: Basic base64(username:password)`
- Static files (web UI) served without authentication
- Disabled when authorization field empty or omitted in config
