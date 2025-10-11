package service

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"

	"service-manager/config"
)

const (
	logBufferSize = 10 * 1024 // 10KB circular buffer
	restartDelay  = 5 * time.Second
)

// FailureCallback is called when a service fails
type FailureCallback func(serviceName string, consecutiveFailures int, exitCode int, err error)

// Service represents a managed service instance
type Service struct {
	Config    config.ServiceConfig
	cmd       *exec.Cmd
	running   bool
	pid       int
	startTime time.Time
	restarts  int

	// Scheduled service tracking
	lastRunTime  time.Time
	lastExitCode int
	lastDuration time.Duration

	// Failure tracking
	consecutiveFailures int
	lastError           error
	failureCallback     FailureCallback

	stdoutBuf *CircularBuffer
	stderrBuf *CircularBuffer

	stdoutFile *os.File
	stderrFile *os.File

	stdoutBroadcast *Broadcaster
	stderrBroadcast *Broadcaster

	mu       sync.RWMutex
	stopChan chan struct{}
	stopOnce sync.Once // Ensures stopChan is only closed once
}

// CircularBuffer is a fixed-size ring buffer for log lines
type CircularBuffer struct {
	data []byte
	size int
	mu   sync.RWMutex
}

// NewCircularBuffer creates a new circular buffer
func NewCircularBuffer(size int) *CircularBuffer {
	return &CircularBuffer{
		data: make([]byte, 0, size),
		size: size,
	}
}

// Write implements io.Writer
func (cb *CircularBuffer) Write(p []byte) (n int, err error) {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	// If adding this would exceed capacity, remove from the front
	if len(cb.data)+len(p) > cb.size {
		excess := len(cb.data) + len(p) - cb.size
		if excess >= len(cb.data) {
			// New data is larger than buffer, just keep the tail
			cb.data = append([]byte{}, p[len(p)-cb.size:]...)
		} else {
			cb.data = append(cb.data[excess:], p...)
		}
	} else {
		cb.data = append(cb.data, p...)
	}

	return len(p), nil
}

// Read returns the current buffer contents
func (cb *CircularBuffer) Read() []byte {
	cb.mu.RLock()
	defer cb.mu.RUnlock()

	result := make([]byte, len(cb.data))
	copy(result, cb.data)
	return result
}

// Broadcaster broadcasts messages to multiple channels
type Broadcaster struct {
	clients map[chan string]bool
	closed  map[chan string]bool // Track closed channels
	mu      sync.RWMutex
}

// NewBroadcaster creates a new broadcaster
func NewBroadcaster() *Broadcaster {
	return &Broadcaster{
		clients: make(map[chan string]bool),
		closed:  make(map[chan string]bool),
	}
}

// Subscribe adds a new client channel
func (b *Broadcaster) Subscribe() chan string {
	b.mu.Lock()
	defer b.mu.Unlock()

	ch := make(chan string, 100)
	b.clients[ch] = true
	return ch
}

// Unsubscribe removes a client channel
func (b *Broadcaster) Unsubscribe(ch chan string) {
	b.mu.Lock()
	defer b.mu.Unlock()

	// Check if already closed
	if b.closed[ch] {
		return
	}

	delete(b.clients, ch)
	close(ch)
	b.closed[ch] = true
}

// Broadcast sends a message to all subscribers
func (b *Broadcaster) Broadcast(msg string) {
	b.mu.RLock()
	defer b.mu.RUnlock()

	for ch := range b.clients {
		select {
		case ch <- msg:
		default:
			// Skip if channel is full
		}
	}
}

// New creates a new service instance
func New(cfg config.ServiceConfig) *Service {
	return &Service{
		Config:          cfg,
		stdoutBuf:       NewCircularBuffer(logBufferSize),
		stderrBuf:       NewCircularBuffer(logBufferSize),
		stdoutBroadcast: NewBroadcaster(),
		stderrBroadcast: NewBroadcaster(),
		stopChan:        make(chan struct{}),
	}
}

// SetFailureCallback sets the callback to be called when the service fails
func (s *Service) SetFailureCallback(callback FailureCallback) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.failureCallback = callback
}

// Start starts the service
func (s *Service) Start() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.running {
		return fmt.Errorf("service %s is already running", s.Config.Name)
	}

	// Open log files
	if err := s.openLogFiles(); err != nil {
		return err
	}

	// Create command
	s.cmd = exec.Command(s.Config.Command, s.Config.Args...)

	// Set working directory
	if s.Config.Workdir != "" {
		s.cmd.Dir = s.Config.Workdir
	}

	// Set environment variables
	s.cmd.Env = os.Environ()
	for k, v := range s.Config.Env {
		s.cmd.Env = append(s.cmd.Env, fmt.Sprintf("%s=%s", k, v))
	}

	// Create pipes for stdout/stderr
	stdout, err := s.cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("failed to create stdout pipe: %w", err)
	}

	stderr, err := s.cmd.StderrPipe()
	if err != nil {
		return fmt.Errorf("failed to create stderr pipe: %w", err)
	}

	// Start the process
	if err := s.cmd.Start(); err != nil {
		s.closeLogFiles()
		return fmt.Errorf("failed to start service %s: %w", s.Config.Name, err)
	}

	s.running = true
	s.pid = s.cmd.Process.Pid
	s.startTime = time.Now()

	// Log start time for scheduled services
	if s.Config.IsScheduled() && s.stderrFile != nil {
		logMsg := fmt.Sprintf("[%s] Starting scheduled task: %s\n",
			s.startTime.Format("2006-01-02 15:04:05"), s.Config.Name)
		s.stderrFile.WriteString(logMsg)
		s.stderrBuf.Write([]byte(logMsg))
		s.stderrBroadcast.Broadcast(logMsg)
	}

	// Start log readers
	go s.readLogs(stdout, s.stdoutFile, s.stdoutBuf, s.stdoutBroadcast)
	go s.readLogs(stderr, s.stderrFile, s.stderrBuf, s.stderrBroadcast)

	// Monitor process
	go s.monitor()

	return nil
}

// Stop stops the service
func (s *Service) Stop() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if !s.running {
		return fmt.Errorf("service %s is not running", s.Config.Name)
	}

	// Signal to stop auto-restart (only close once)
	s.stopOnce.Do(func() {
		close(s.stopChan)
	})

	// Kill the process
	if s.cmd != nil && s.cmd.Process != nil {
		if err := s.cmd.Process.Kill(); err != nil {
			return fmt.Errorf("failed to kill process: %w", err)
		}
	}

	s.running = false
	s.pid = 0

	return nil
}

// Restart restarts the service
func (s *Service) Restart() error {
	if err := s.Stop(); err != nil && s.IsRunning() {
		return err
	}

	time.Sleep(100 * time.Millisecond)

	// Reset stop channel and once flag
	s.mu.Lock()
	s.stopChan = make(chan struct{})
	s.stopOnce = sync.Once{}
	s.mu.Unlock()

	return s.Start()
}

// IsRunning returns whether the service is running
func (s *Service) IsRunning() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.running
}

// GetStatus returns the current service status
func (s *Service) GetStatus() Status {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var uptime time.Duration
	if s.running {
		uptime = time.Since(s.startTime)
	}

	// Only include lastRunTime if it's been set (not zero value)
	var lastRunTime *time.Time
	if !s.lastRunTime.IsZero() {
		lastRunTime = &s.lastRunTime
	}

	return Status{
		Name:                s.Config.Name,
		Running:             s.running,
		PID:                 s.pid,
		Uptime:              uptime,
		Restarts:            s.restarts,
		LastRunTime:         lastRunTime,
		LastExitCode:        s.lastExitCode,
		LastDuration:        s.lastDuration,
		ConsecutiveFailures: s.consecutiveFailures,
	}
}

// Status represents service status information
type Status struct {
	Name                string        `json:"name"`
	Running             bool          `json:"running"`
	PID                 int           `json:"pid"`
	Uptime              time.Duration `json:"uptime"`
	Restarts            int           `json:"restarts"`
	LastRunTime         *time.Time    `json:"lastRunTime,omitempty"`
	LastExitCode        int           `json:"lastExitCode"`
	LastDuration        time.Duration `json:"lastDuration"`
	ConsecutiveFailures int           `json:"consecutiveFailures"`
}

// GetStdoutBuffer returns the stdout buffer contents
func (s *Service) GetStdoutBuffer() []byte {
	return s.stdoutBuf.Read()
}

// GetStderrBuffer returns the stderr buffer contents
func (s *Service) GetStderrBuffer() []byte {
	return s.stderrBuf.Read()
}

// SubscribeStdout subscribes to stdout updates
func (s *Service) SubscribeStdout() chan string {
	return s.stdoutBroadcast.Subscribe()
}

// SubscribeStderr subscribes to stderr updates
func (s *Service) SubscribeStderr() chan string {
	return s.stderrBroadcast.Subscribe()
}

// UnsubscribeStdout unsubscribes from stdout updates
func (s *Service) UnsubscribeStdout(ch chan string) {
	s.stdoutBroadcast.Unsubscribe(ch)
}

// UnsubscribeStderr unsubscribes from stderr updates
func (s *Service) UnsubscribeStderr(ch chan string) {
	s.stderrBroadcast.Unsubscribe(ch)
}

// WriteStderrLog writes a message to the stderr log (for cron overlap messages)
func (s *Service) WriteStderrLog(msg string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.stderrFile != nil {
		s.stderrFile.WriteString(msg)
	}
	s.stderrBuf.Write([]byte(msg))
	s.stderrBroadcast.Broadcast(msg)
}

// openLogFiles opens the log files for writing
func (s *Service) openLogFiles() error {
	if err := os.MkdirAll("logs", 0755); err != nil {
		return fmt.Errorf("failed to create logs directory: %w", err)
	}

	stdoutPath := filepath.Join("logs", fmt.Sprintf("%s-stdout.log", s.Config.Name))
	stderrPath := filepath.Join("logs", fmt.Sprintf("%s-stderr.log", s.Config.Name))

	var err error
	s.stdoutFile, err = os.OpenFile(stdoutPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return fmt.Errorf("failed to open stdout log file: %w", err)
	}

	s.stderrFile, err = os.OpenFile(stderrPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		s.stdoutFile.Close()
		return fmt.Errorf("failed to open stderr log file: %w", err)
	}

	return nil
}

// closeLogFiles closes the log files
func (s *Service) closeLogFiles() {
	if s.stdoutFile != nil {
		s.stdoutFile.Close()
		s.stdoutFile = nil
	}
	if s.stderrFile != nil {
		s.stderrFile.Close()
		s.stderrFile = nil
	}
}

// readLogs reads from a pipe and writes to file, buffer, and broadcast
func (s *Service) readLogs(pipe io.Reader, file *os.File, buf *CircularBuffer, broadcast *Broadcaster) {
	scanner := bufio.NewScanner(pipe)
	for scanner.Scan() {
		line := scanner.Text() + "\n"

		// Write to file
		if file != nil {
			file.WriteString(line)
		}

		// Write to circular buffer
		buf.Write([]byte(line))

		// Broadcast to subscribers
		broadcast.Broadcast(line)
	}
}

// monitor watches the process and handles restarts
func (s *Service) monitor() {
	startTime := time.Now()
	err := s.cmd.Wait()
	duration := time.Since(startTime)

	// Get exit code
	exitCode := 0
	if err != nil {
		if exitError, ok := err.(*exec.ExitError); ok {
			exitCode = exitError.ExitCode()
		} else {
			exitCode = -1 // Unknown error
		}
	}

	s.mu.Lock()
	s.running = false
	s.pid = 0
	s.lastRunTime = startTime
	s.lastExitCode = exitCode
	s.lastDuration = duration
	s.lastError = err

	// Log to stderr for scheduled services BEFORE closing files
	if s.Config.IsScheduled() && s.stderrFile != nil {
		endTime := time.Now()
		logMsg := fmt.Sprintf("[%s] Task ended with exit code: %d (duration: %v)\n",
			endTime.Format("2006-01-02 15:04:05"), exitCode, duration.Round(time.Second))
		s.stderrFile.WriteString(logMsg)
		s.stderrBuf.Write([]byte(logMsg))
		s.stderrBroadcast.Broadcast(logMsg)
	}

	s.closeLogFiles()

	// Track failures (exit code 0 = success, anything else = failure)
	if exitCode == 0 {
		// Success - reset consecutive failures
		s.consecutiveFailures = 0
	} else {
		// Failure - increment counter
		s.consecutiveFailures++
	}

	// Call failure callback (both on failure and success, so manager can reset state)
	callback := s.failureCallback
	consecutiveFailures := s.consecutiveFailures
	s.mu.Unlock()

	if callback != nil {
		callback(s.Config.Name, consecutiveFailures, exitCode, err)
	}

	// Scheduled services don't auto-restart, let the scheduler handle it
	if s.Config.IsScheduled() {
		return
	}

	// Continuous services: check if we should restart
	// Don't restart if service exited successfully (exit code 0)
	if exitCode == 0 {
		fmt.Fprintf(os.Stderr, "Service %s exited successfully (exit code 0). Not restarting.\n",
			s.Config.Name)
		return
	}

	// Check if intentionally stopped
	select {
	case <-s.stopChan:
		// Stopped intentionally, don't restart
		return
	default:
		// Service crashed/failed, keep trying to restart
		for {
			if err != nil {
				fmt.Fprintf(os.Stderr, "Service %s exited with exit code %d: %v. Restarting in %v...\n",
					s.Config.Name, exitCode, err, restartDelay)
			} else {
				fmt.Fprintf(os.Stderr, "Service %s exited with exit code %d. Restarting in %v...\n",
					s.Config.Name, exitCode, restartDelay)
			}

			// Check if stop was requested during the delay
			select {
			case <-time.After(restartDelay):
				// Continue with restart
			case <-s.stopChan:
				// Stop requested during delay, abort restart
				return
			}

			s.mu.Lock()
			s.restarts++
			s.mu.Unlock()

			if err := s.Start(); err != nil {
				fmt.Fprintf(os.Stderr, "Failed to restart service %s: %v\n", s.Config.Name, err)
				// Loop will retry after another delay
			} else {
				// Successfully restarted, monitor will be called by Start()
				return
			}
		}
	}
}
