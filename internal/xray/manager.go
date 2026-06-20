// Package xray manages the xray-core subprocess and generates its config.
package xray

import (
	"bufio"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"

	"xray-manager/internal/models"
)

const maxLogLines = 500

// Status is a snapshot of the manager state for the API.
type Status struct {
	Running       bool   `json:"running"`
	ActiveProxyID string `json:"activeProxyId"`
	PID           int    `json:"pid"`
	UptimeSeconds int64  `json:"uptime"`
	BinaryOK      bool   `json:"binaryOk"`
	Warning       string `json:"warning,omitempty"`
}

// Manager owns the xray process lifecycle, config generation, and log fan-out.
type Manager struct {
	binary     string
	configPath string
	socksPort  int
	httpPort   int

	mu         sync.Mutex
	cmd        *exec.Cmd
	activeID   string
	startTime  time.Time
	running    bool
	dnsServers []string

	logMu sync.Mutex
	logs  []string
	subs  map[chan string]struct{}
}

// NewManager constructs a Manager. It does not start the process.
func NewManager(binary, configPath string, socksPort, httpPort int) *Manager {
	return &Manager{
		binary:     binary,
		configPath: configPath,
		socksPort:  socksPort,
		httpPort:   httpPort,
		subs:       make(map[chan string]struct{}),
	}
}

// SocksPort and HTTPPort expose the configured inbound ports.
func (m *Manager) SocksPort() int { return m.socksPort }
func (m *Manager) HTTPPort() int  { return m.httpPort }

// SetDNS replaces the DNS servers baked into generated configs.
func (m *Manager) SetDNS(servers []string) {
	m.mu.Lock()
	m.dnsServers = append([]string(nil), servers...)
	m.mu.Unlock()
}

// DNS returns the configured DNS servers, falling back to sane defaults when
// none have been set.
func (m *Manager) DNS() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.dnsServers) == 0 {
		return []string{"1.1.1.1", "1.0.0.1", "8.8.8.8"}
	}
	return append([]string(nil), m.dnsServers...)
}

// BinaryAvailable reports whether the xray binary can be found/executed.
func (m *Manager) BinaryAvailable() bool {
	if _, err := os.Stat(m.binary); err == nil {
		return true
	}
	if _, err := exec.LookPath(m.binary); err == nil {
		return true
	}
	return false
}

// WriteConfig generates and writes the xray config for p.
func (m *Manager) WriteConfig(p *models.Proxy) error {
	data, err := m.GenerateConfig(p)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(m.configPath), 0o755); err != nil {
		return err
	}
	return os.WriteFile(m.configPath, data, 0o644)
}

// Activate writes p's config and records it as active. If xray is already
// running it restarts to apply the change; if stopped, it stays stopped (the
// user starts it explicitly from the Status tab).
func (m *Manager) Activate(p *models.Proxy) error {
	if err := m.WriteConfig(p); err != nil {
		return err
	}
	m.SetActiveID(p.ID)
	if m.IsRunning() {
		return m.Restart()
	}
	return nil
}

// SetActiveID records the active proxy id without touching the process.
func (m *Manager) SetActiveID(id string) {
	m.mu.Lock()
	m.activeID = id
	m.mu.Unlock()
}

// Start launches the xray process using the current config file.
func (m *Manager) Start() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.running {
		return nil
	}
	if !m.binaryExists() {
		return errors.New("xray binary not found at " + m.binary)
	}
	if _, err := os.Stat(m.configPath); err != nil {
		return errors.New("no xray config yet — activate a proxy first")
	}

	cmd := exec.Command(m.binary, "run", "-c", m.configPath)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	m.cmd = cmd
	m.running = true
	m.startTime = time.Now()

	go m.scan(stdout)
	go m.scan(stderr)
	go m.wait(cmd)

	m.appendLog("[manager] xray started (pid " + itoa(cmd.Process.Pid) + ")")
	return nil
}

// Stop terminates the xray process if running.
func (m *Manager) Stop() error {
	m.mu.Lock()
	cmd := m.cmd
	running := m.running
	m.mu.Unlock()
	if !running || cmd == nil || cmd.Process == nil {
		return nil
	}
	m.appendLog("[manager] stopping xray")
	_ = cmd.Process.Signal(os.Interrupt)
	// Give it a moment, then force-kill.
	done := make(chan struct{})
	go func() { _ = cmd.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		_ = cmd.Process.Kill()
	}
	m.mu.Lock()
	m.running = false
	m.cmd = nil
	m.mu.Unlock()
	return nil
}

// Restart stops then starts the process.
func (m *Manager) Restart() error {
	if err := m.Stop(); err != nil {
		return err
	}
	return m.Start()
}

// IsRunning reports whether xray is currently running.
func (m *Manager) IsRunning() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.running
}

// Status returns a snapshot for the API.
func (m *Manager) Status() Status {
	m.mu.Lock()
	defer m.mu.Unlock()
	st := Status{
		Running:       m.running,
		ActiveProxyID: m.activeID,
		BinaryOK:      m.binaryExists(),
	}
	if m.running && m.cmd != nil && m.cmd.Process != nil {
		st.PID = m.cmd.Process.Pid
		st.UptimeSeconds = int64(time.Since(m.startTime).Seconds())
	}
	if !st.BinaryOK {
		st.Warning = "xray binary not found at " + m.binary
	}
	return st
}

func (m *Manager) binaryExists() bool {
	if _, err := os.Stat(m.binary); err == nil {
		return true
	}
	_, err := exec.LookPath(m.binary)
	return err == nil
}

func (m *Manager) wait(cmd *exec.Cmd) {
	err := cmd.Wait()
	m.mu.Lock()
	// Only react if this is still the active command (not superseded by restart).
	if m.cmd == cmd {
		m.running = false
		m.cmd = nil
	}
	m.mu.Unlock()
	if err != nil {
		m.appendLog("[manager] xray exited: " + err.Error())
	} else {
		m.appendLog("[manager] xray exited cleanly")
	}
}

func (m *Manager) scan(r io.Reader) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		m.appendLog(sc.Text())
	}
}

// ---------- log fan-out ----------

func (m *Manager) appendLog(line string) {
	m.logMu.Lock()
	m.logs = append(m.logs, line)
	if len(m.logs) > maxLogLines {
		m.logs = m.logs[len(m.logs)-maxLogLines:]
	}
	subs := make([]chan string, 0, len(m.subs))
	for c := range m.subs {
		subs = append(subs, c)
	}
	m.logMu.Unlock()

	for _, c := range subs {
		select {
		case c <- line:
		default: // drop if subscriber is slow
		}
	}
}

// RecentLogs returns a copy of the buffered log lines.
func (m *Manager) RecentLogs() []string {
	m.logMu.Lock()
	defer m.logMu.Unlock()
	out := make([]string, len(m.logs))
	copy(out, m.logs)
	return out
}

// ClearLogs empties the log buffer.
func (m *Manager) ClearLogs() {
	m.logMu.Lock()
	m.logs = nil
	m.logMu.Unlock()
}

// Subscribe returns a channel of new log lines and a cancel func to unsubscribe.
func (m *Manager) Subscribe() (<-chan string, func()) {
	ch := make(chan string, 64)
	m.logMu.Lock()
	m.subs[ch] = struct{}{}
	m.logMu.Unlock()
	cancel := func() {
		m.logMu.Lock()
		if _, ok := m.subs[ch]; ok {
			delete(m.subs, ch)
			close(ch)
		}
		m.logMu.Unlock()
	}
	return ch, cancel
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}
