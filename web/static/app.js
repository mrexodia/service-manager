let selectedService = null;
let currentStream = 'stdout';
let logWebSocket = null;
let refreshInterval = null;

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
}

// Load all services
async function loadServices() {
    try {
        const response = await fetch('/api/services');
        const services = await response.json();

        renderServiceList(services);

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

// Render service list
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

        const dot = document.createElement('div');
        dot.className = `status-dot ${service.running ? 'running' : 'stopped'}`;

        const name = document.createElement('div');
        name.className = 'service-item-name';
        name.textContent = service.name;

        item.appendChild(dot);
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

    // Update buttons
    const startBtn = document.getElementById('startBtn');
    const stopBtn = document.getElementById('stopBtn');
    const restartBtn = document.getElementById('restartBtn');

    if (service.running) {
        startBtn.disabled = true;
        stopBtn.disabled = false;
        restartBtn.disabled = false;
    } else {
        startBtn.disabled = false;
        stopBtn.disabled = true;
        restartBtn.disabled = true;
    }

    // Hide edit form if visible
    hideEditForm();
}

// Update service status display
function updateServiceStatus(service) {
    const badge = document.getElementById('statusBadge');
    badge.textContent = service.running ? 'Running' : 'Stopped';
    badge.className = `status-badge ${service.running ? 'running' : 'stopped'}`;

    const stats = document.getElementById('serviceStats');
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
    logContent.textContent = '';

    const protocol = window.location.protocol === 'https:' ? 'wss:' : 'ws:';
    const url = `${protocol}//${window.location.host}/api/services/${serviceName}/logs/${stream}`;

    logWebSocket = new WebSocket(url);

    logWebSocket.onmessage = (event) => {
        logContent.textContent += event.data;
        // Auto-scroll to bottom
        logContent.parentElement.scrollTop = logContent.parentElement.scrollHeight;
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
    const args = document.getElementById('createArgs').value
        .split('\n')
        .map(s => s.trim())
        .filter(s => s.length > 0);
    const workdir = document.getElementById('createWorkdir').value;
    const envText = document.getElementById('createEnv').value;

    const env = {};
    envText.split('\n').forEach(line => {
        const trimmed = line.trim();
        if (trimmed.length > 0) {
            const [key, ...valueParts] = trimmed.split('=');
            if (key && valueParts.length > 0) {
                env[key.trim()] = valueParts.join('=').trim();
            }
        }
    });

    const service = {
        name,
        command,
        args: args.length > 0 ? args : undefined,
        workdir: workdir || undefined,
        env: Object.keys(env).length > 0 ? env : undefined
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
        document.getElementById('editArgs').value = (service.args || []).join('\n');
        document.getElementById('editWorkdir').value = service.workdir || '';

        const envLines = [];
        if (service.env) {
            for (const [key, value] of Object.entries(service.env)) {
                envLines.push(`${key}=${value}`);
            }
        }
        document.getElementById('editEnv').value = envLines.join('\n');

        document.getElementById('editForm').style.display = 'block';
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
    const args = document.getElementById('editArgs').value
        .split('\n')
        .map(s => s.trim())
        .filter(s => s.length > 0);
    const workdir = document.getElementById('editWorkdir').value;
    const envText = document.getElementById('editEnv').value;

    const env = {};
    envText.split('\n').forEach(line => {
        const trimmed = line.trim();
        if (trimmed.length > 0) {
            const [key, ...valueParts] = trimmed.split('=');
            if (key && valueParts.length > 0) {
                env[key.trim()] = valueParts.join('=').trim();
            }
        }
    });

    const service = {
        name,
        command,
        args: args.length > 0 ? args : undefined,
        workdir: workdir || undefined,
        env: Object.keys(env).length > 0 ? env : undefined
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
