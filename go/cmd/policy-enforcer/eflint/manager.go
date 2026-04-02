// Package eflint provides functionality to manage and communicate with
// eFLINT server instances for policy enforcement in the DYNAMOS system.
package eflint

import (
	"bufio"
	"encoding/json"
	"fmt"
	"math/rand"
	"net"
	"os/exec"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"
)

// -----------------------------------------------------------------------------
// Configuration
// -----------------------------------------------------------------------------

// ManagerConfig holds configuration for the eFLINT instance Manager.
// It defines the parameters for starting and connecting to eFLINT server processes.
type ManagerConfig struct {
	EflintServerPath  string        // Path to the eflint-server executable
	MinPort           int           // Minimum port number for random port selection
	MaxPort           int           // Maximum port number for random port selection
	StartupDelay      time.Duration // Time to wait after starting a process
	ConnectionTimeout time.Duration // Timeout for TCP connections and commands
}

// DefaultManagerConfig returns sensible default configuration values.
func DefaultManagerConfig() *ManagerConfig {
	return &ManagerConfig{
		EflintServerPath:  "eflint-server",
		MinPort:           1025,
		MaxPort:           65535,
		StartupDelay:      3 * time.Second,
		ConnectionTimeout: 60 * time.Second,
	}
}

// -----------------------------------------------------------------------------
// Status Types
// -----------------------------------------------------------------------------

// InstanceStatus represents the current status of an eFLINT server instance.
type InstanceStatus struct {
	Running       bool   `json:"running"`                  // Whether the instance is running
	Port          int    `json:"port,omitempty"`           // The TCP port the instance is listening on
	ModelLocation string `json:"model_location,omitempty"` // Path to the loaded eFLINT model
}

// -----------------------------------------------------------------------------
// Manager
// -----------------------------------------------------------------------------

// Manager manages an eFLINT server instance lifecycle and communication.
// It handles starting, stopping, and sending commands to the eFLINT server process.
type Manager struct {
	instance *Instance
	mu       sync.RWMutex
	config   *ManagerConfig
	logger   *zap.Logger
	rnd      *rand.Rand
}

// NewManager creates a new eFLINT instance Manager with the given configuration.
func NewManager(config *ManagerConfig, logger *zap.Logger) *Manager {
	if config == nil {
		config = DefaultManagerConfig()
	}

	return &Manager{
		config: config,
		logger: logger,
		rnd:    rand.New(rand.NewSource(time.Now().UnixNano())),
	}
}

// Start starts the eFLINT server instance with the given model.
func (m *Manager) Start(modelLocation string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Kill existing instance if running
	if m.instance != nil && m.instance.IsAlive() {
		if err := m.instance.Kill(); err != nil {
			m.logger.Warn("failed to kill existing instance", zap.Error(err))
		}
	}

	// Generate random port
	port := m.generateRandomPort()

	// Start the eFLINT server process
	process, err := m.startProcess(modelLocation, port)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrProcessStartFailed, err)
	}

	m.instance = NewInstance(port, process, modelLocation)

	m.logger.Info("started eFLINT server instance",
		zap.Int("port", port),
		zap.String("model", modelLocation),
	)

	return nil
}

// Stop stops the running eFLINT server instance.
func (m *Manager) Stop() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.instance == nil {
		return ErrInstanceNotFound
	}

	if err := m.instance.Kill(); err != nil {
		return err
	}

	m.logger.Info("stopped eFLINT server instance")
	m.instance = nil

	return nil
}

// Restart restarts the eFLINT server instance with the same model.
func (m *Manager) Restart() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.instance == nil {
		return ErrInstanceNotFound
	}

	return m.restartInternalWithModel(m.instance.GetModelLocation())
}

// restartWithModel restarts the eFLINT server instance with a specific model.
// This is used internally when recovering from load-export failures.
// NOTE: This method does NOT acquire the mutex - caller must handle locking.
func (m *Manager) restartWithModel(modelLocation string) error {
	// Note: We don't acquire the mutex here because this is called from StateManager
	// which may already hold a mutex. The caller is responsible for thread safety.
	return m.restartInternalWithModel(modelLocation)
}

// restartInternalWithModel is the internal implementation of restart.
// It does NOT acquire the mutex - caller must handle locking appropriately.
func (m *Manager) restartInternalWithModel(modelLocation string) error {
	// Kill existing instance if running
	if m.instance != nil && m.instance.IsAlive() {
		if err := m.instance.Kill(); err != nil {
			m.logger.Warn("failed to kill instance during restart", zap.Error(err))
		}
	}

	// Generate new port
	port := m.generateRandomPort()

	// Start new process
	process, err := m.startProcess(modelLocation, port)
	if err != nil {
		m.instance = nil
		return fmt.Errorf("%w: %v", ErrProcessStartFailed, err)
	}

	m.instance = NewInstance(port, process, modelLocation)

	m.logger.Info("restarted eFLINT server instance",
		zap.Int("port", port),
		zap.String("model", modelLocation),
	)

	return nil
}

// UpdateModel updates the model and restarts the instance.
func (m *Manager) UpdateModel(modelLocation string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Kill existing instance if running
	if m.instance != nil && m.instance.IsAlive() {
		if err := m.instance.Kill(); err != nil {
			m.logger.Warn("failed to kill instance during model update", zap.Error(err))
		}
	}

	// Generate new port
	port := m.generateRandomPort()

	// Start new process with new model
	process, err := m.startProcess(modelLocation, port)
	if err != nil {
		m.instance = nil
		return fmt.Errorf("%w: %v", ErrProcessStartFailed, err)
	}

	m.instance = NewInstance(port, process, modelLocation)

	m.logger.Info("updated eFLINT server model",
		zap.Int("port", port),
		zap.String("model", modelLocation),
	)

	return nil
}

// Status returns the current status of the instance.
func (m *Manager) Status() InstanceStatus {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if m.instance == nil {
		return InstanceStatus{Running: false}
	}

	return InstanceStatus{
		Running:       m.instance.IsAlive(),
		Port:          m.instance.GetPort(),
		ModelLocation: m.instance.GetModelLocation(),
	}
}

// IsRunning checks if the instance is running.
func (m *Manager) IsRunning() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()

	return m.instance != nil && m.instance.IsAlive()
}

// SendCommand sends a command to the eFLINT server instance.
func (m *Manager) SendCommand(command string) (string, error) {
	m.mu.RLock()
	instance := m.instance
	m.mu.RUnlock()

	if instance == nil {
		return "", ErrInstanceNotFound
	}

	if !instance.IsAlive() {
		return "", ErrInstanceNotRunning
	}

	// Connect to the instance (use 127.0.0.1 to force IPv4)
	addr := fmt.Sprintf("127.0.0.1:%d", instance.GetPort())
	conn, err := net.DialTimeout("tcp", addr, m.config.ConnectionTimeout)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrConnectionFailed, err)
	}
	defer conn.Close()

	// Set deadline for the operation
	if err := conn.SetDeadline(time.Now().Add(m.config.ConnectionTimeout)); err != nil {
		return "", fmt.Errorf("failed to set deadline: %v", err)
	}

	// Send command with newline
	if _, err := conn.Write([]byte(command + "\n")); err != nil {
		return "", fmt.Errorf("%w: %v", ErrCommandFailed, err)
	}

	// Read response until newline
	reader := bufio.NewReader(conn)
	response, err := reader.ReadString('\n')
	if err != nil {
		return "", fmt.Errorf("failed to read response: %v", err)
	}

	m.logger.Debug("sent command to eFLINT instance")

	return strings.TrimSpace(response), nil
}

// SendPhrases sends a full eFLINT specification to the server using the "phrases" command.
// Unlike the singular "phrase" command (which sends one line at a time), "phrases" sends
// the entire eFLINT file content in one go. The response is checked for errors.
func (m *Manager) SendPhrases(text string) (*PhrasesResponse, error) {
	// Build the command as a struct to get proper JSON escaping of the text
	type phrasesCmd struct {
		Command string `json:"command"`
		Text    string `json:"text"`
	}

	cmd := phrasesCmd{
		Command: "phrases",
		Text:    text,
	}

	cmdJSON, err := json.Marshal(cmd)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal phrases command: %w", err)
	}

	// Ensure the command is on a single line (the eFLINT server uses line-based protocol)
	cmdStr := strings.ReplaceAll(string(cmdJSON), "\n", " ")
	cmdStr = strings.ReplaceAll(cmdStr, "\r", " ")

	m.logger.Debug("sending phrases command",
		zap.Int("text_length", len(text)),
		zap.Int("command_size", len(cmdStr)),
	)

	response, err := m.SendCommand(cmdStr)
	if err != nil {
		return nil, fmt.Errorf("failed to send phrases command: %w", err)
	}

	// Parse the response
	var phrasesResp PhrasesResponse
	if err := json.Unmarshal([]byte(response), &phrasesResp); err != nil {
		return nil, fmt.Errorf("failed to parse phrases response: %w", err)
	}

	// Check for errors in the response
	if len(phrasesResp.Errors) > 0 {
		var errMsgs []string
		for _, e := range phrasesResp.Errors {
			errMsgs = append(errMsgs, e.Message)
		}
		return &phrasesResp, fmt.Errorf("eFLINT phrases command had errors: %s", strings.Join(errMsgs, "; "))
	}

	m.logger.Debug("phrases command completed successfully",
		zap.Int("query_results", len(phrasesResp.QueryResults)),
	)

	return &phrasesResp, nil
}

// PhrasesResponse represents the response from an eFLINT "phrases" command.
type PhrasesResponse struct {
	Response     string   `json:"response"`
	QueryResults []string `json:"query-results"`
	Errors       []struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	} `json:"errors"`
	Violations []struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	} `json:"violations"`
}

// GetState retrieves the state by sending an export command.
func (m *Manager) GetState() (string, error) {
	return m.SendCommand(`{"command": "create-export"}`)
}

// GetEflintStatus retrieves the status from the eFLINT server.
// This returns detailed information about the current state of the server.
func (m *Manager) GetEflintStatus() (string, error) {
	return m.SendCommand(`{"command": "status"}`)
}

// startProcess starts a new eFLINT server process.
func (m *Manager) startProcess(modelLocation string, port int) (*exec.Cmd, error) {
	cmd := exec.Command(m.config.EflintServerPath, modelLocation, fmt.Sprintf("%d", port))

	// Capture stderr for debugging
	cmd.Stderr = nil
	cmd.Stdout = nil

	m.logger.Info("starting eflint-server",
		zap.String("path", m.config.EflintServerPath),
		zap.String("model", modelLocation),
		zap.Int("port", port),
	)

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("failed to start eflint-server: %w", err)
	}

	// Wait for the server to start
	time.Sleep(m.config.StartupDelay)

	// Check if the process is still running
	if cmd.ProcessState != nil {
		return nil, fmt.Errorf("eflint-server process exited immediately")
	}

	m.logger.Info("eflint-server started successfully",
		zap.Int("pid", cmd.Process.Pid),
		zap.Int("port", port),
	)

	return cmd, nil
}

// generateRandomPort generates a random port number within the configured range.
func (m *Manager) generateRandomPort() int {
	if m.rnd == nil {
		m.rnd = rand.New(rand.NewSource(time.Now().UnixNano()))
	}

	return m.rnd.Intn(m.config.MaxPort-m.config.MinPort) + m.config.MinPort
}
