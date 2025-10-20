let selectedService = null;
let currentStream = 'stdout';
let logWebSocket = null;
let refreshInterval = null;
let lastServicesSnapshot = null;

// Initialize
document.addEventListener('DOMContentLoaded', () => {
    loadServices();
    setupEventListeners();

    // Refresh service list every 2 seconds
    refreshInterval = setInterval(loadServices, 2000);
});

// Setup event listeners
function setupEventListeners() {
    document.getElementById('createServiceBtn').addEventListener('click', showCreateView);
    document.getElementById('cancelCreateBtn').addEventListener('click', hideCreateView);
    document.getElementById('createServiceForm').addEventListener('submit', handleCreateService);

    document.getElementById('editBtn').addEventListener('click', showEditForm);
    document.getElementById('cancelEditBtn').addEventListener('click', hideEditForm);
    document.getElementById('serviceForm').addEventListener('submit', handleUpdateService);

    document.getElementById('startBtn').addEventListener('click', () => controlService('start'));
    document.getElementById('stopBtn').addEventListener('click', () => controlService('stop'));
    document.getElementById('restartBtn').addEventListener('click', () => controlService('restart'));
    document.getElementById('runNowBtn').addEventListener('click', handleRunNow);
    document.getElementById('deleteBtn').addEventListener('click', handleDeleteService);

    // Log tabs
    document.querySelectorAll('.log-tab').forEach(tab => {
        tab.addEventListener('click', (e) => {
            currentStream = e.target.dataset.stream;
            document.querySelectorAll('.log-tab').forEach(t => t.classList.remove('active'));
            e.target.classList.add('active');
            if (selectedService) {
                connectLogStream(selectedService, currentStream);
            }
        });
    });

    // Working directory change listeners for .env detection
    let editWorkdirDebounce;
    document.getElementById('editWorkdir').addEventListener('input', (e) => {
        clearTimeout(editWorkdirDebounce);
        editWorkdirDebounce = setTimeout(() => {
            checkDotenv(selectedService, e.target.value, 'edit');
        }, 500); // Debounce to avoid too many API calls
    });

    let createWorkdirDebounce;
    document.getElementById('createWorkdir').addEventListener('input', (e) => {
        clearTimeout(createWorkdirDebounce);
        createWorkdirDebounce = setTimeout(() => {
            // For create form, we don't have a service name yet, so use placeholder
            checkDotenv('_new', e.target.value, 'create');
        }, 500); // Debounce to avoid too many API calls
    });
}

// Check for .env file in working directory
async function checkDotenv(serviceName, workdir, formType) {
    const indicatorId = formType === 'edit' ? 'editDotenvIndicator' : 'createDotenvIndicator';
    const tooltipId = formType === 'edit' ? 'editDotenvTooltip' : 'createDotenvTooltip';

    const indicator = document.getElementById(indicatorId);
    const tooltip = document.getElementById(tooltipId);

    if (!workdir) {
        // Hide indicator if no working directory
        indicator.style.display = 'none';
        return;
    }

    try {
        // For new services, we need to use a dummy service name
        const checkServiceName = serviceName === '_new' ?
            (Object.keys(lastServicesSnapshot || {}).length > 0 ?
                Object.keys(lastServicesSnapshot)[0] : 'dummy') :
            serviceName;

        const response = await fetch(`/api/services/${checkServiceName}/dotenv?workdir=${encodeURIComponent(workdir)}`);

        if (!response.ok) {
            // If service doesn't exist yet (creating new), hide the indicator
            indicator.style.display = 'none';
            return;
        }

        const data = await response.json();

        if (data.exists) {
            // Show indicator
            indicator.style.display = 'inline-block';

            // Format .env contents for tooltip
            if (data.raw) {
                tooltip.textContent = data.raw;
            } else if (data.variables) {
                // Format variables as KEY=VALUE
                const lines = [];
                for (const [key, value] of Object.entries(data.variables)) {
                    lines.push(`${key}=${value}`);
                }
                tooltip.textContent = lines.join('\n');
            } else {
                tooltip.textContent = '.env file found (empty or unreadable)';
            }

            if (data.error) {
                tooltip.textContent = `Error: ${data.error}`;
            }
        } else {
            // Hide indicator
            indicator.style.display = 'none';
        }
    } catch (error) {
        console.error('Failed to check for .env file:', error);
        // Hide indicator on error
        indicator.style.display = 'none';
    }
}

// Load all services
async function loadServices() {
    try {
        const response = await fetch('/api/services');
        const services = await response.json();

        // Only rebuild the list if services have changed
        const currentSnapshot = JSON.stringify(services.map(s => ({
            name: s.name,
            running: s.running,
            enabled: s.enabled,
            schedule: s.schedule
        })));

        if (currentSnapshot !== lastServicesSnapshot) {
            renderServiceList(services);
            lastServicesSnapshot = currentSnapshot;
        }

        // Update current service status if one is selected
        if (selectedService) {
            const current = services.find(s => s.name === selectedService);
            if (current) {
                updateServiceStatus(current);
            }
        }
    } catch (error) {
        console.error('Failed to load services:', error);
    }
}

// Render service list (simple rebuild)
function renderServiceList(services) {
    const list = document.getElementById('serviceList');
    list.innerHTML = '';

    services.forEach(service => {
        const item = document.createElement('div');
        item.className = 'service-item';
        if (service.name === selectedService) {
            item.classList.add('active');
        }
        if (!service.enabled) {
            item.classList.add('disabled');
        }

        // Add scheduled/continuous class for styling
        if (isScheduledService(service)) {
            item.classList.add('scheduled');
        } else {
            item.classList.add('continuous');
        }

        // Show clock icon for scheduled services, status dot for continuous
        if (isScheduledService(service)) {
            const clock = document.createElement('div');
            clock.className = 'clock-icon';
            clock.textContent = '⏰';
            item.appendChild(clock);
        } else {
            const dot = document.createElement('div');
            // Determine dot class based on enabled + running state
            if (service.enabled === false && !service.running) {
                // Disabled + Stopped → grey
                dot.className = 'status-dot disabled';
            } else if (service.enabled === false && service.running) {
                // Disabled + Running → green with grey tint
                dot.className = 'status-dot disabled-running';
            } else {
                // Enabled → green/red based on running state
                dot.className = `status-dot ${service.running ? 'running' : 'stopped'}`;
            }
            item.appendChild(dot);
        }

        const name = document.createElement('div');
        name.className = 'service-item-name';
        name.textContent = service.name;

        item.appendChild(name);

        item.addEventListener('click', () => selectService(service.name));

        list.appendChild(item);
    });
}

// Select a service
async function selectService(name) {
    selectedService = name;

    // Update UI immediately to show selection
    document.querySelectorAll('.service-item').forEach(item => {
        item.classList.remove('active');
        if (item.querySelector('.service-item-name').textContent === name) {
            item.classList.add('active');
        }
    });

    try {
        const response = await fetch(`/api/services/${name}`);
        const service = await response.json();

        showServiceView(service);
        connectLogStream(name, currentStream);
    } catch (error) {
        console.error('Failed to load service:', error);
    }
}

// Show service view
function showServiceView(service) {
    document.getElementById('welcomeView').style.display = 'none';
    document.getElementById('createView').style.display = 'none';
    document.getElementById('serviceView').style.display = 'flex';

    document.getElementById('serviceName').textContent = service.name;
    updateServiceStatus(service);

    // Update buttons and checkbox based on service state
    const enabledCheckbox = document.getElementById('enabledCheckbox');
    const startBtn = document.getElementById('startBtn');
    const stopBtn = document.getElementById('stopBtn');
    const restartBtn = document.getElementById('restartBtn');
    const runNowBtn = document.getElementById('runNowBtn');

    // Set checkbox state
    enabledCheckbox.checked = service.enabled !== false;

    if (isScheduledService(service)) {
        // Scheduled service: hide start/stop/restart, show Run Now
        startBtn.style.display = 'none';
        stopBtn.style.display = 'none';
        restartBtn.style.display = 'none';
        runNowBtn.style.display = 'inline-block';

        // Disable Run Now if already running or service is disabled
        runNowBtn.disabled = service.running || service.enabled === false;
    } else {
        // Continuous service: show Start/Stop/Restart buttons
        startBtn.style.display = 'inline-block';
        stopBtn.style.display = 'inline-block';
        restartBtn.style.display = 'inline-block';
        runNowBtn.style.display = 'none';

        // Enable/disable buttons based on running state only
        // (Allow runtime control regardless of enabled flag)
        if (service.running) {
            startBtn.disabled = true;
            stopBtn.disabled = false;
            restartBtn.disabled = false;
        } else {
            startBtn.disabled = false;
            stopBtn.disabled = true;
            restartBtn.disabled = true;
        }
    }

    // Hide edit form if visible
    hideEditForm();
}

// Update service status display
function updateServiceStatus(service) {
    const badge = document.getElementById('statusBadge');
    const stats = document.getElementById('serviceStats');

    if (isScheduledService(service)) {
        // Scheduled service status
        if (service.running) {
            badge.textContent = 'Running';
            badge.className = 'status-badge running';
        } else {
            badge.textContent = service.enabled !== false ? 'Scheduled' : 'Disabled';
            badge.className = `status-badge ${service.enabled !== false ? 'scheduled' : 'stopped'}`;
        }

        // Show next run and last run info
        const nextRun = service.nextRunTime ? formatNextRun(service.nextRunTime) : 'N/A';
        const lastRun = service.lastRunTime ? formatLastRun(service.lastRunTime) : 'Never';
        const lastExitCode = service.lastExitCode !== undefined ? service.lastExitCode : 'N/A';
        const lastDuration = service.lastDuration ? formatDuration(service.lastDuration) : 'N/A';

        stats.innerHTML = `
            <div class="stat-item">
                <div class="stat-label">Schedule</div>
                <div class="stat-value">${service.schedule || 'N/A'}</div>
            </div>
            <div class="stat-item">
                <div class="stat-label">Next Run</div>
                <div class="stat-value">${nextRun}</div>
            </div>
            <div class="stat-item">
                <div class="stat-label">Last Run</div>
                <div class="stat-value">${lastRun}</div>
            </div>
            <div class="stat-item">
                <div class="stat-label">Last Exit Code</div>
                <div class="stat-value">${lastExitCode}</div>
            </div>
            <div class="stat-item">
                <div class="stat-label">Last Duration</div>
                <div class="stat-value">${lastDuration}</div>
            </div>
        `;
    } else {
        // Continuous service status
        badge.textContent = service.running ? 'Running' : 'Stopped';
        badge.className = `status-badge ${service.running ? 'running' : 'stopped'}`;

        const uptime = service.running ? formatUptime(service.uptime) : 'N/A';
        const pid = service.running ? service.pid : 'N/A';
        const enabled = service.enabled !== false ? 'Yes' : 'No';

        stats.innerHTML = `
            <div class="stat-item">
                <div class="stat-label">PID</div>
                <div class="stat-value">${pid}</div>
            </div>
            <div class="stat-item">
                <div class="stat-label">Uptime</div>
                <div class="stat-value">${uptime}</div>
            </div>
            <div class="stat-item">
                <div class="stat-label">Restarts</div>
                <div class="stat-value">${service.restarts}</div>
            </div>
            <div class="stat-item">
                <div class="stat-label">Auto-start</div>
                <div class="stat-value">${enabled}</div>
            </div>
        `;
    }
}

// Format uptime in seconds to human readable
function formatUptime(seconds) {
    if (seconds < 60) {
        return `${Math.floor(seconds)}s`;
    } else if (seconds < 3600) {
        return `${Math.floor(seconds / 60)}m ${Math.floor(seconds % 60)}s`;
    } else {
        const hours = Math.floor(seconds / 3600);
        const minutes = Math.floor((seconds % 3600) / 60);
        return `${hours}h ${minutes}m`;
    }
}

// Connect to log stream via WebSocket
function connectLogStream(serviceName, stream) {
    // Close existing connection
    if (logWebSocket) {
        logWebSocket.close();
    }

    const logContent = document.getElementById('logContent');
    const logViewer = logContent.parentElement;
    logContent.textContent = '';

    // Reset scroll to bottom when first connecting
    logViewer.scrollTop = logViewer.scrollHeight;

    const protocol = window.location.protocol === 'https:' ? 'wss:' : 'ws:';
    const url = `${protocol}//${window.location.host}/api/services/${serviceName}/logs/${stream}`;

    logWebSocket = new WebSocket(url);

    logWebSocket.onmessage = (event) => {
        const wasAtBottom = logViewer.scrollHeight - logViewer.scrollTop - logViewer.clientHeight < 10;

        logContent.textContent += event.data;

        // Only auto-scroll if already at bottom
        if (wasAtBottom) {
            logViewer.scrollTop = logViewer.scrollHeight;
        }
    };

    logWebSocket.onerror = (error) => {
        console.error('WebSocket error:', error);
    };
}

// Control service (start/stop/restart)
async function controlService(action) {
    if (!selectedService) return;

    try {
        const response = await fetch(`/api/services/${selectedService}/${action}`, {
            method: 'POST'
        });

        if (response.ok) {
            // Refresh immediately
            setTimeout(() => selectService(selectedService), 200);
        } else {
            alert(`Failed to ${action} service`);
        }
    } catch (error) {
        console.error(`Failed to ${action} service:`, error);
        alert(`Failed to ${action} service`);
    }
}

// Show create view
function showCreateView() {
    document.getElementById('welcomeView').style.display = 'none';
    document.getElementById('serviceView').style.display = 'none';
    document.getElementById('createView').style.display = 'block';

    // Clear form
    document.getElementById('createServiceForm').reset();

    // Hide .env indicator
    document.getElementById('createDotenvIndicator').style.display = 'none';
}

// Hide create view
function hideCreateView() {
    document.getElementById('createView').style.display = 'none';
    if (selectedService) {
        selectService(selectedService);
    } else {
        document.getElementById('welcomeView').style.display = 'block';
    }
}

// Handle create service
async function handleCreateService(e) {
    e.preventDefault();

    const name = document.getElementById('createName').value;
    const command = document.getElementById('createCommand').value;
    const workdir = document.getElementById('createWorkdir').value;
    const envText = document.getElementById('createEnv').value;
    const schedule = document.getElementById('createSchedule').value;

    const service = {
        name,
        command,
        workdir: workdir || undefined,
        env_raw: envText.trim() || undefined,  // Send raw text for godotenv parsing
        schedule: schedule || undefined
    };

    try {
        const response = await fetch('/api/services', {
            method: 'POST',
            headers: {
                'Content-Type': 'application/json'
            },
            body: JSON.stringify(service)
        });

        if (response.ok) {
            hideCreateView();
            await loadServices();
            selectService(name);
        } else {
            const error = await response.text();
            alert(`Failed to create service: ${error}`);
        }
    } catch (error) {
        console.error('Failed to create service:', error);
        alert('Failed to create service');
    }
}

// Show edit form
async function showEditForm() {
    if (!selectedService) return;

    try {
        const response = await fetch(`/api/services/${selectedService}`);
        const service = await response.json();

        document.getElementById('editName').value = service.name;
        document.getElementById('editCommand').value = service.command;
        document.getElementById('editWorkdir').value = service.workdir || '';

        const envLines = [];
        if (service.env) {
            for (const [key, value] of Object.entries(service.env)) {
                envLines.push(`${key}=${value}`);
            }
        }
        document.getElementById('editEnv').value = envLines.join('\n');

        // Set the schedule field
        document.getElementById('editSchedule').value = service.schedule || '';

        // Store enabled state to preserve it during update
        document.getElementById('editForm').dataset.enabled = JSON.stringify(service.enabled);

        document.getElementById('editForm').style.display = 'block';

        // Check for .env file in the working directory
        if (service.workdir) {
            checkDotenv(service.name, service.workdir, 'edit');
        } else {
            // Hide indicator if no working directory
            document.getElementById('editDotenvIndicator').style.display = 'none';
        }
    } catch (error) {
        console.error('Failed to load service for editing:', error);
    }
}

// Hide edit form
function hideEditForm() {
    document.getElementById('editForm').style.display = 'none';
}

// Handle update service
async function handleUpdateService(e) {
    e.preventDefault();

    const name = document.getElementById('editName').value;
    const command = document.getElementById('editCommand').value;
    const workdir = document.getElementById('editWorkdir').value;
    const envText = document.getElementById('editEnv').value;
    const schedule = document.getElementById('editSchedule').value;

    // Retrieve preserved enabled value
    const editForm = document.getElementById('editForm');
    const enabledValue = JSON.parse(editForm.dataset.enabled);

    const service = {
        name,
        command,
        workdir: workdir || undefined,
        env_raw: envText.trim() || undefined,  // Send raw text for godotenv parsing
        enabled: enabledValue,
        schedule: schedule || undefined
    };

    try {
        const response = await fetch(`/api/services/${name}`, {
            method: 'PUT',
            headers: {
                'Content-Type': 'application/json'
            },
            body: JSON.stringify(service)
        });

        if (response.ok) {
            hideEditForm();
            await loadServices();
            setTimeout(() => selectService(name), 200);
        } else {
            const error = await response.text();
            alert(`Failed to update service: ${error}`);
        }
    } catch (error) {
        console.error('Failed to update service:', error);
        alert('Failed to update service');
    }
}

// Handle delete service
async function handleDeleteService() {
    if (!selectedService) return;

    if (!confirm(`Are you sure you want to delete service "${selectedService}"?`)) {
        return;
    }

    try {
        const response = await fetch(`/api/services/${selectedService}`, {
            method: 'DELETE'
        });

        if (response.ok) {
            selectedService = null;
            document.getElementById('serviceView').style.display = 'none';
            document.getElementById('welcomeView').style.display = 'block';

            if (logWebSocket) {
                logWebSocket.close();
                logWebSocket = null;
            }

            await loadServices();
        } else {
            const error = await response.text();
            alert(`Failed to delete service: ${error}`);
        }
    } catch (error) {
        console.error('Failed to delete service:', error);
        alert('Failed to delete service');
    }
}

// Helper: Check if service is scheduled
function isScheduledService(service) {
    return service.schedule !== undefined && service.schedule !== null && service.schedule !== '';
}

// Format next run time with relative duration
function formatNextRun(nextRunTime) {
    const next = new Date(nextRunTime);
    const now = new Date();
    const diff = next - now;

    if (diff < 0) {
        return 'Overdue';
    }

    const seconds = Math.floor(diff / 1000);
    const minutes = Math.floor(seconds / 60);
    const hours = Math.floor(minutes / 60);
    const days = Math.floor(hours / 24);

    let relative = '';
    if (days > 0) {
        relative = `in ${days}d ${hours % 24}h`;
    } else if (hours > 0) {
        relative = `in ${hours}h ${minutes % 60}m`;
    } else if (minutes > 0) {
        relative = `in ${minutes}m`;
    } else {
        relative = `in ${seconds}s`;
    }

    const timeStr = next.toLocaleTimeString();
    return `${timeStr} (${relative})`;
}

// Format last run timestamp
function formatLastRun(lastRunTime) {
    const last = new Date(lastRunTime);
    const now = new Date();
    const diff = now - last;

    const seconds = Math.floor(diff / 1000);
    const minutes = Math.floor(seconds / 60);
    const hours = Math.floor(minutes / 60);
    const days = Math.floor(hours / 24);

    let relative = '';
    if (days > 0) {
        relative = `${days}d ago`;
    } else if (hours > 0) {
        relative = `${hours}h ago`;
    } else if (minutes > 0) {
        relative = `${minutes}m ago`;
    } else {
        relative = `${seconds}s ago`;
    }

    const timeStr = last.toLocaleString();
    return `${timeStr} (${relative})`;
}

// Format duration in milliseconds
function formatDuration(ms) {
    if (ms < 1000) {
        return `${ms}ms`;
    }

    const seconds = Math.floor(ms / 1000);
    if (seconds < 60) {
        return `${seconds}s`;
    }

    const minutes = Math.floor(seconds / 60);
    const remainingSeconds = seconds % 60;
    if (minutes < 60) {
        return `${minutes}m ${remainingSeconds}s`;
    }

    const hours = Math.floor(minutes / 60);
    const remainingMinutes = minutes % 60;
    return `${hours}h ${remainingMinutes}m`;
}

// Handle Run Now button
async function handleRunNow() {
    if (!selectedService) return;

    try {
        const response = await fetch(`/api/services/${selectedService}/run-now`, {
            method: 'POST'
        });

        if (response.ok) {
            // Refresh immediately
            setTimeout(() => selectService(selectedService), 200);
        } else if (response.status === 409) {
            alert('Service is already running');
        } else {
            alert('Failed to run service');
        }
    } catch (error) {
        console.error('Failed to run service:', error);
        alert('Failed to run service');
    }
}

// Handle enabled checkbox change
async function handleEnabledChange() {
    if (!selectedService) return;

    const checkbox = document.getElementById('enabledCheckbox');
    const isEnabled = checkbox.checked;
    const action = isEnabled ? 'enable' : 'disable';

    try {
        const response = await fetch(`/api/services/${selectedService}/${action}`, {
            method: 'POST'
        });

        if (response.ok) {
            // Refresh service list and re-select current service to update button states
            setTimeout(() => {
                loadServices();
                selectService(selectedService);
            }, 200);
        } else {
            alert(`Failed to ${action} service`);
            // Revert checkbox state on error
            checkbox.checked = !isEnabled;
        }
    } catch (error) {
        console.error('Failed to toggle service:', error);
        alert('Failed to toggle service');
        // Revert checkbox state on error
        checkbox.checked = !isEnabled;
    }
}
