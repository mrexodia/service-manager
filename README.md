# Service Manager

A simple service manager written in Go with a web UI for managing, monitoring, and logging multiple services.

## Features

- Define services in YAML (command, args, working directory, environment variables)
- Auto-start enabled services on manager startup
- Auto-restart services on crash
- Persistent enable/disable state via YAML
- Automatic config reload when `services.yaml` changes (checked every 5 seconds)
- Web UI on port 4321
- Live log streaming (stdout/stderr)
- Create, edit, and delete services via web UI
- YAML comment preservation when editing config
- Service control (start/stop/restart)

## Installation

Build the service manager:

```bash
go build -o service-manager.exe
```

## Configuration

Services are defined in `services.yaml`. Example:

```yaml
services:
  - name: my-service
    command: /path/to/executable
    args:
      - --arg1
      - value1
    workdir: /working/directory
    env:
      KEY1: value1
      KEY2: value2
    enabled: true  # Optional: Set to false to disable auto-start
```

### Configuration Fields

- `name` (required): Unique service identifier
- `command` (required): Executable path or command
- `args` (optional): List of command-line arguments
- `workdir` (optional): Working directory for the service
- `env` (optional): Environment variables as key-value pairs
- `enabled` (optional): If `false`, service won't auto-start (default: `true`)

### Auto-Reload

The service manager monitors `services.yaml` and automatically reloads when changes are detected (checked every 5 seconds). The web UI will reflect changes without requiring a restart.

## Usage

1. Start the service manager:
   ```bash
   ./service-manager.exe
   ```

2. Open your browser to `http://localhost:4321`

3. Use the web UI to:
   - View all services in the left sidebar
   - Click a service to view logs and details
   - Start/Stop/Restart services
   - Edit service configuration
   - Create new services
   - Delete services

## Logs

Service logs are written to:
- `logs/{service-name}-stdout.log`
- `logs/{service-name}-stderr.log`

The web UI shows the last ~10KB of logs plus live streaming.

## Stopping

Press `Ctrl+C` to stop the service manager and all managed services.
