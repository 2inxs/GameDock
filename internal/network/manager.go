// Package network runs the pinned EasyTier engine without installing an adapter,
// changing DNS, adding OS routes, or configuring a router.
package network

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"gamedock/internal/model"
	"gamedock/internal/process"
	"gamedock/internal/rooms"
)

type Options struct {
	CorePath   string
	CLIPath    string
	RuntimeDir string
	Publish    func(RoomStatus)
	// Private hooks keep subprocess tests independent of an installed engine.
	argsPrefix   []string
	listeners    []string
	pollInterval time.Duration
}
type PeerInfo struct {
	ID         string  `json:"id"`
	Hostname   string  `json:"hostname"`
	VirtualIP  string  `json:"virtualIp"`
	Connection string  `json:"connection"`
	LatencyMS  float64 `json:"latencyMs"`
}
type RoomStatus struct {
	RoomID     string     `json:"roomId"`
	State      string     `json:"state"`
	Address    string     `json:"address"`
	VirtualIP  string     `json:"virtualIp"`
	Error      string     `json:"error"`
	AllowRelay bool       `json:"allowRelay"`
	Peers      []PeerInfo `json:"peers"`
	Logs       []string   `json:"logs"`
}
type session struct {
	config            rooms.Config
	pm                *process.Manager
	dir, rpc          string
	startupDone, done chan struct{}
	cancelStartup     context.CancelFunc
	cancelMonitor     context.CancelFunc
	cleanup           sync.Once
	closing           bool
}
type Manager struct {
	mu           sync.Mutex
	opts         Options
	active       *session
	status       RoomStatus
	closed       bool
	events       chan RoomStatus
	dispatchDone chan struct{}
	dispatchOnce sync.Once
}

func New(opts Options) *Manager {
	if opts.pollInterval <= 0 {
		opts.pollInterval = time.Second
	}
	m := &Manager{opts: opts, status: RoomStatus{State: "disconnected", Peers: []PeerInfo{}, Logs: []string{}}, events: make(chan RoomStatus, 16), dispatchDone: make(chan struct{})}
	if opts.Publish != nil {
		go func() {
			for {
				select {
				case <-m.dispatchDone:
					return
				case event := <-m.events:
					func() { defer func() { _ = recover() }(); opts.Publish(event) }()
				}
			}
		}()
	}
	return m
}
func copyStatus(s RoomStatus) RoomStatus {
	s.Peers = append([]PeerInfo{}, s.Peers...)
	s.Logs = append([]string{}, s.Logs...)
	return s
}
func (m *Manager) Status() RoomStatus { m.mu.Lock(); defer m.mu.Unlock(); return copyStatus(m.status) }
func (m *Manager) emitLocked() {
	select {
	case m.events <- copyStatus(m.status):
	default:
	}
}
func (m *Manager) logLocked(message string) {
	m.status.Logs = append(m.status.Logs, time.Now().Format("15:04:05")+" "+message)
	if len(m.status.Logs) > 80 {
		m.status.Logs = append([]string{}, m.status.Logs[len(m.status.Logs)-80:]...)
	}
}
func (m *Manager) setState(s *session, state, message string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.active != s || s.closing {
		return
	}
	if m.status.State != state || m.status.Error != message {
		m.logLocked(stateMessage(state, message))
	}
	m.status.State, m.status.Error = state, message
	m.emitLocked()
}
func stateMessage(state, message string) string {
	if message != "" {
		return message
	}
	switch state {
	case "connecting":
		return "Запускается защищённое подключение комнаты"
	case "waiting":
		return "Ожидание участников комнаты"
	case "connected":
		return "Сетевой путь к участнику комнаты установлен"
	case "no_direct_path":
		return "Прямой путь пока не установлен; передача через ретранслятор запрещена"
	case "disconnected":
		return "Комната отключена"
	}
	return state
}

func (m *Manager) Connect(ctx context.Context, config rooms.Config) (result error) {
	if err := rooms.Validate(config); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	config.BootstrapPeers = append([]string{}, config.BootstrapPeers...)
	startupCtx, cancelStartup := context.WithTimeout(ctx, 12*time.Second)
	monitorCtx, cancelMonitor := context.WithCancel(context.Background())
	s := &session{config: config, pm: process.New(nil), startupDone: make(chan struct{}), done: make(chan struct{}), cancelStartup: cancelStartup, cancelMonitor: cancelMonitor}
	m.mu.Lock()
	if m.closed || m.active != nil {
		m.mu.Unlock()
		cancelStartup()
		cancelMonitor()
		return errors.New("сначала отключите текущую комнату")
	}
	m.active = s
	port := config.GamePort
	if config.Role == "guest" {
		port = config.LocalPort
	}
	m.status = RoomStatus{RoomID: config.ID, State: "connecting", Address: fmt.Sprintf("127.0.0.1:%d", port), AllowRelay: config.AllowRelay, Peers: []PeerInfo{}, Logs: []string{}}
	m.logLocked(stateMessage("connecting", ""))
	m.emitLocked()
	m.mu.Unlock()
	defer func() {
		cancelStartup()
		close(s.startupDone)
		if result != nil {
			m.requestCleanup(s, "error", safeError(result))
			select {
			case <-s.done:
			case <-time.After(4 * time.Second):
			}
		}
	}()
	if !filepath.IsAbs(m.opts.RuntimeDir) {
		return errors.New("папка временных файлов сети должна иметь абсолютный путь")
	}
	for _, path := range []string{m.opts.CorePath, m.opts.CLIPath} {
		if !filepath.IsAbs(path) {
			return errors.New("не задан абсолютный путь к EasyTier")
		}
		info, err := os.Stat(path)
		if err != nil || !info.Mode().IsRegular() {
			return errors.New("EasyTier не установлен или его файлы недоступны")
		}
	}
	// Never allow inherited ET_* options to override no-TUN/DNS/ACL policy.
	for _, binary := range []string{m.opts.CorePath, m.opts.CLIPath} {
		output, err := m.runCommand(startupCtx, binary, "--version")
		if err != nil {
			return fmt.Errorf("не удалось проверить версию %s: %w", filepath.Base(binary), err)
		}
		if !compatibleVersion(string(output)) {
			return errors.New("требуется EasyTier версии " + EngineVersion)
		}
	}
	if config.Role == "guest" {
		listener, err := net.Listen("tcp4", fmt.Sprintf("127.0.0.1:%d", config.LocalPort))
		if err != nil {
			return errors.New("локальный игровой порт уже занят; выберите другой порт")
		}
		listener.Close()
	}
	if err := os.MkdirAll(m.opts.RuntimeDir, 0700); err != nil {
		return errors.New("не удалось создать папку временных файлов сети")
	}
	dir, err := os.MkdirTemp(m.opts.RuntimeDir, "room-")
	if err != nil {
		return errors.New("не удалось создать временную папку комнаты")
	}
	s.dir = dir
	if err = privateDirectory(dir); err != nil {
		return errors.New("не удалось защитить временную папку с ключом комнаты")
	}
	listeners := m.opts.listeners
	if len(listeners) == 0 {
		listeners = []string{"tcp://0.0.0.0:0", "udp://0.0.0.0:0"}
	}
	configPath := filepath.Join(dir, "room.toml")
	if err = os.WriteFile(configPath, []byte(coreConfig(config, listeners)), 0600); err != nil {
		return errors.New("не удалось записать защищённую конфигурацию комнаты")
	}
	if _, err = m.command(startupCtx, m.opts.CorePath, "--disable-env-parsing", "--config-file", configPath, "--check-config"); err != nil {
		return errors.New("EasyTier отклонил конфигурацию комнаты")
	}
	reservation, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return errors.New("не удалось выделить локальный порт управления")
	}
	s.rpc = reservation.Addr().String()
	reservation.Close()
	if err = startupCtx.Err(); err != nil {
		return err
	}
	args := append([]string{}, m.opts.argsPrefix...)
	args = append(args, "--disable-env-parsing", "--config-file", configPath, "--rpc-portal", s.rpc, "--rpc-portal-whitelist", "127.0.0.1/32", "--console-log-level", "error", "--machine-id", config.ID)
	if _, err = s.pm.Start("easytier", model.LaunchSpec{Executable: m.opts.CorePath, Args: args, Env: cleanEnvironment(), WorkingDir: dir}); err != nil {
		return errors.New("не удалось запустить EasyTier")
	}
	for {
		if err = startupCtx.Err(); err != nil {
			return err
		}
		if !processActive(s.pm.Snapshot("easytier").State) {
			return errors.New("EasyTier завершился до готовности подключения")
		}
		if err = m.refresh(startupCtx, s); err == nil {
			go m.monitor(monitorCtx, s)
			return nil
		}
		select {
		case <-startupCtx.Done():
			return startupCtx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
}

func processActive(state model.ProcessState) bool {
	return state == model.Starting || state == model.Running || state == model.Stopping
}
func safeError(err error) string {
	if errors.Is(err, context.DeadlineExceeded) {
		return "EasyTier не ответил вовремя"
	}
	if errors.Is(err, context.Canceled) {
		return "Подключение отменено"
	}
	return err.Error() // Only our own fixed strings enter this path, never engine output.
}
func cleanEnvironment() []string {
	result := make([]string, 0, len(os.Environ()))
	for _, v := range os.Environ() {
		name, _, _ := strings.Cut(v, "=")
		if !strings.HasPrefix(strings.ToUpper(name), "ET_") {
			result = append(result, v)
		}
	}
	return result
}

func compatibleVersion(output string) bool {
	fields := strings.Fields(output)
	if len(fields) != 2 {
		return false
	}
	version, _, _ := strings.Cut(fields[1], "-")
	return version == EngineVersion
}

type boundedOutput struct {
	bytes.Buffer
	overflow bool
}

func (b *boundedOutput) Write(p []byte) (int, error) {
	n := len(p)
	remaining := 1024*1024 - b.Len()
	if len(p) > remaining {
		b.overflow = true
		p = p[:remaining]
	}
	_, _ = b.Buffer.Write(p)
	return n, nil
}
func (m *Manager) command(ctx context.Context, executable string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	return m.runCommand(ctx, executable, args...)
}

func (m *Manager) runCommand(ctx context.Context, executable string, args ...string) ([]byte, error) {
	argv := append(append([]string{}, m.opts.argsPrefix...), args...)
	cmd := exec.CommandContext(ctx, executable, argv...)
	cmd.Env = cleanEnvironment()
	hideWindow(cmd)
	var output boundedOutput
	cmd.Stdout = &output
	err := cmd.Run()
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if err != nil {
		return output.Bytes(), err
	}
	if output.overflow {
		return nil, errors.New("слишком большой ответ EasyTier")
	}
	return output.Bytes(), nil
}

type peerRow struct {
	ID       string `json:"id"`
	Hostname string `json:"hostname"`
	IPv4     string `json:"ipv4"`
	Cost     string `json:"cost"`
	Latency  string `json:"lat_ms"`
}

func (m *Manager) refresh(ctx context.Context, s *session) error {
	output, err := m.command(ctx, m.opts.CLIPath, "--rpc-portal", s.rpc, "--instance-name", "gamedock-"+s.config.ID, "--output", "json", "peer")
	if err != nil {
		return err
	}
	var rows []peerRow
	if err = json.Unmarshal(output, &rows); err != nil || rows == nil {
		return errors.New("неизвестный формат ответа EasyTier")
	}
	virtualIP := ""
	peers := make([]PeerInfo, 0)
	state := "waiting"
	foundLocal := false
	prefix := netip.MustParsePrefix(rooms.VirtualNetworkCIDR)
	for _, row := range rows {
		if row.Cost == "Local" {
			virtualIP = row.IPv4
			foundLocal = true
			continue
		}
		ip, err := netip.ParseAddr(row.IPv4)
		if err != nil || !prefix.Contains(ip) {
			continue
		} // Exclude public bootstrap nodes.
		connection := "unavailable"
		if row.Cost == "p2p" {
			connection = "direct"
		} else if strings.HasPrefix(row.Cost, "relay(") {
			connection = "relay"
		}
		latency, _ := strconv.ParseFloat(row.Latency, 64)
		hostname := strings.ReplaceAll(row.Hostname, s.config.Secret, "[скрыто]")
		if len(hostname) > 256 {
			hostname = hostname[:256]
		}
		if len(peers) < 256 {
			peers = append(peers, PeerInfo{ID: row.ID, Hostname: hostname, VirtualIP: row.IPv4, Connection: connection, LatencyMS: latency})
		}
		if s.config.Role == "host" || row.IPv4 == s.config.HostVirtualIP {
			if connection == "direct" || (connection == "relay" && s.config.AllowRelay) {
				state = "connected"
			} else if state != "connected" && connection == "relay" {
				state = "no_direct_path"
			}
		}
	}
	if !foundLocal {
		return errors.New("в ответе EasyTier нет локального узла")
	}
	if local, err := netip.ParseAddr(virtualIP); err != nil || !prefix.Contains(local) {
		state = "waiting"
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.active != s || s.closing {
		return nil
	}
	if m.status.State != state || m.status.Error != "" {
		m.logLocked(stateMessage(state, ""))
	}
	m.status.State, m.status.Error, m.status.VirtualIP, m.status.Peers = state, "", virtualIP, peers
	m.emitLocked()
	return nil
}
func (m *Manager) monitor(ctx context.Context, s *session) {
	ticker := time.NewTicker(m.opts.pollInterval)
	defer ticker.Stop()
	failures := 0
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if !processActive(s.pm.Snapshot("easytier").State) {
				m.requestCleanup(s, "error", "EasyTier неожиданно завершился")
				return
			}
			if err := m.refresh(ctx, s); err != nil {
				failures++
				if failures >= 3 {
					m.setState(s, "error", "Не удаётся получить состояние EasyTier; отключите комнату и повторите подключение")
				}
			} else {
				failures = 0
			}
		}
	}
}
func (m *Manager) requestCleanup(s *session, finalState, message string) {
	s.cleanup.Do(func() {
		m.mu.Lock()
		s.closing = true
		m.mu.Unlock()
		s.cancelStartup()
		s.cancelMonitor()
		go func() {
			<-s.startupDone
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			_ = s.pm.Kill("easytier")
			err := s.pm.Shutdown(ctx)
			stillActive := processActive(s.pm.Snapshot("easytier").State)
			if s.dir != "" && !stillActive {
				_ = os.RemoveAll(s.dir)
			} // Only the private MkdirTemp directory owned by this session.
			m.mu.Lock()
			if m.active == s {
				if err != nil {
					finalState = "error"
					message = "Не удалось завершить сетевой процесс"
				}
				m.status.State, m.status.Error = finalState, message
				m.status.VirtualIP = ""
				m.status.Peers = []PeerInfo{}
				m.logLocked(stateMessage(finalState, message))
				if !stillActive {
					m.active = nil
				}
				m.emitLocked()
			}
			m.mu.Unlock()
			close(s.done)
		}()
	})
}
func (m *Manager) Disconnect(ctx context.Context) error {
	m.mu.Lock()
	s := m.active
	m.mu.Unlock()
	if s == nil {
		return nil
	}
	m.requestCleanup(s, "disconnected", "")
	select {
	case <-s.done:
		if processActive(s.pm.Snapshot("easytier").State) {
			return errors.New("сетевой процесс ещё завершается")
		}
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (m *Manager) Shutdown(ctx context.Context) error {
	m.mu.Lock()
	m.closed = true
	m.mu.Unlock()
	err := m.Disconnect(ctx)
	m.dispatchOnce.Do(func() { close(m.dispatchDone) })
	return err
}
