// Package process owns server processes independently of UI request lifetimes.
package process

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"gamedock/internal/model"
)

const (
	maxLogEntries  = 1000
	maxLogBytes    = 1024 * 1024
	maxLineBytes   = 16 * 1024
	commandTimeout = 2 * time.Second
)

type command struct {
	text   string
	result chan error
}
type run struct {
	cmd           *exec.Cmd
	stdin         io.WriteCloser
	tree          *processTree
	ready         chan struct{}
	done          chan struct{}
	commands      chan command
	stopOnce      sync.Once
	stopRequested bool
	stopCommand   string
}
type server struct {
	snapshot model.RuntimeSnapshot
	run      *run
	logs     []model.LogEntry
	logBytes int
	sequence uint64
}

// Manager serializes lifecycle changes; slow event consumers cannot block children.
// Events are best-effort hints. Snapshot and Logs are authoritative recovery APIs.
type Manager struct {
	mu           sync.Mutex
	servers      map[string]*server
	nextRun      uint64
	closed       bool
	events       chan model.Event
	dispatchDone chan struct{}
	dispatchOnce sync.Once
}

func New(publish func(model.Event)) *Manager {
	m := &Manager{servers: make(map[string]*server), events: make(chan model.Event, 256), dispatchDone: make(chan struct{})}
	if publish != nil {
		go func() {
			for {
				select {
				case <-m.dispatchDone:
					return
				case event := <-m.events:
					// An event integration must not take down process supervision.
					func() { defer func() { _ = recover() }(); publish(event) }()
				}
			}
		}()
	}
	return m
}

func (m *Manager) get(id string) *server {
	s := m.servers[id]
	if s == nil {
		s = &server{snapshot: model.RuntimeSnapshot{ServerID: id, State: model.Stopped}}
		m.servers[id] = s
	}
	return s
}

func cloneSnapshot(s model.RuntimeSnapshot) model.RuntimeSnapshot {
	if s.StartedAt != nil {
		v := *s.StartedAt
		s.StartedAt = &v
	}
	if s.ExitedAt != nil {
		v := *s.ExitedAt
		s.ExitedAt = &v
	}
	if s.ExitCode != nil {
		v := *s.ExitCode
		s.ExitCode = &v
	}
	return s
}

func (m *Manager) stateEvent(s *server) {
	v := cloneSnapshot(s.snapshot)
	m.emit(model.Event{Type: "state", ServerID: v.ServerID, Snapshot: &v})
}
func (m *Manager) emit(event model.Event) {
	select {
	case m.events <- event:
	default:
	}
}

func (m *Manager) Start(id string, spec model.LaunchSpec) (model.RuntimeSnapshot, error) {
	if strings.TrimSpace(id) == "" || strings.TrimSpace(spec.Executable) == "" {
		return m.Snapshot(id), errors.New("server ID and executable are required")
	}
	if strings.ContainsAny(spec.StopCommand, "\r\n") {
		return m.Snapshot(id), errors.New("stop command must be one line")
	}
	m.mu.Lock()
	s := m.get(id)
	if m.closed {
		snapshot := cloneSnapshot(s.snapshot)
		m.mu.Unlock()
		return snapshot, errors.New("process manager is shutting down")
	}
	if s.run != nil {
		snapshot := cloneSnapshot(s.snapshot)
		m.mu.Unlock()
		return snapshot, errors.New("server is already active")
	}
	m.nextRun++
	r := &run{ready: make(chan struct{}), done: make(chan struct{}), commands: make(chan command, 64), stopCommand: spec.StopCommand}
	s.run = r
	s.snapshot = model.RuntimeSnapshot{ServerID: id, RunID: fmt.Sprintf("%d-%d", time.Now().UnixNano(), m.nextRun), State: model.Starting}
	m.stateEvent(s)
	m.mu.Unlock()

	cmd := exec.Command(spec.Executable, spec.Args...)
	cmd.Env = spec.Env
	cmd.Dir = spec.WorkingDir
	tree, err := prepareTree(cmd)
	if err != nil {
		return m.startFailed(id, r, err)
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		tree.close()
		return m.startFailed(id, r, err)
	}
	outR, outW, err := os.Pipe()
	if err != nil {
		stdin.Close()
		tree.close()
		return m.startFailed(id, r, err)
	}
	errR, errW, err := os.Pipe()
	if err != nil {
		stdin.Close()
		outR.Close()
		outW.Close()
		tree.close()
		return m.startFailed(id, r, err)
	}
	cmd.Stdout, cmd.Stderr = outW, errW
	err = cmd.Start()
	outW.Close()
	errW.Close()
	if err != nil {
		stdin.Close()
		outR.Close()
		errR.Close()
		tree.close()
		return m.startFailed(id, r, err)
	}
	if err = tree.attach(cmd.Process); err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait() // The only Wait for a process whose containment setup failed.
		stdin.Close()
		outR.Close()
		errR.Close()
		tree.close()
		return m.startFailed(id, r, fmt.Errorf("attach process containment: %w", err))
	}
	m.mu.Lock()
	r.cmd, r.stdin, r.tree = cmd, stdin, &tree
	now := time.Now()
	s.snapshot.StartedAt, s.snapshot.PID = &now, cmd.Process.Pid
	if r.stopRequested {
		s.snapshot.State = model.Stopping
	} else {
		s.snapshot.State = model.Running
	}
	m.stateEvent(s)
	close(r.ready)
	snapshot := cloneSnapshot(s.snapshot)
	m.mu.Unlock()

	var drains sync.WaitGroup
	drains.Add(2)
	go func() { defer drains.Done(); m.readOutput(id, r, "stdout", outR) }()
	go func() { defer drains.Done(); m.readOutput(id, r, "stderr", errR) }()
	go m.writeCommands(r)
	go func() {
		waitErr := cmd.Wait() // Exactly one owner for successful launches.
		stdin.Close()
		tree.close() // Kill lingering descendants, releasing inherited output handles.
		drained := make(chan struct{})
		go func() { drains.Wait(); close(drained) }()
		select {
		case <-drained:
		case <-time.After(time.Second):
			outR.Close()
			errR.Close()
			<-drained
		}
		m.mu.Lock()
		defer m.mu.Unlock()
		now, exitCode := time.Now(), cmd.ProcessState.ExitCode()
		s.snapshot.PID, s.snapshot.ExitedAt, s.snapshot.ExitCode = 0, &now, &exitCode
		s.snapshot.State = model.Stopped
		if waitErr != nil && !r.stopRequested {
			s.snapshot.State, s.snapshot.Error = model.Failed, waitErr.Error()
		}
		s.run = nil
		m.stateEvent(s)
		close(r.done)
	}()
	if snapshot.State == model.Stopping {
		m.requestStop(r)
	}
	return snapshot, nil
}

func (m *Manager) startFailed(id string, r *run, err error) (model.RuntimeSnapshot, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s := m.get(id)
	now := time.Now()
	s.snapshot.State, s.snapshot.Error, s.snapshot.ExitedAt = model.Failed, err.Error(), &now
	s.run = nil
	close(r.ready)
	close(r.done)
	m.stateEvent(s)
	return cloneSnapshot(s.snapshot), err
}

func (m *Manager) writeCommands(r *run) {
	for {
		select {
		case <-r.done:
			return
		case c := <-r.commands:
			_, err := io.WriteString(r.stdin, c.text+"\n")
			c.result <- err
		}
	}
}

func (m *Manager) requestStop(r *run) {
	r.stopOnce.Do(func() {
		if r.stopCommand == "" {
			return
		}
		// Delivery is asynchronous so Stop always respects its caller's deadline.
		go func() {
			select {
			case r.commands <- command{text: r.stopCommand, result: make(chan error, 1)}:
			case <-r.done:
			}
		}()
	})
}

func (m *Manager) Stop(ctx context.Context, id string) error {
	m.mu.Lock()
	s := m.get(id)
	r := s.run
	if r == nil {
		m.mu.Unlock()
		return nil
	}
	r.stopRequested = true
	s.snapshot.State = model.Stopping
	m.stateEvent(s)
	m.mu.Unlock()
	select {
	case <-r.done:
		return nil
	case <-r.ready:
	case <-ctx.Done():
		return ctx.Err()
	}
	m.requestStop(r)
	select {
	case <-r.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (m *Manager) Kill(id string) error {
	m.mu.Lock()
	s := m.get(id)
	r := s.run
	if r == nil {
		m.mu.Unlock()
		return nil
	}
	r.stopRequested = true
	s.snapshot.State = model.Stopping
	m.stateEvent(s)
	m.mu.Unlock()
	// A start may be in flight. Retain that exact run so a later restart is safe.
	select {
	case <-r.ready:
		return killRun(r)
	default:
		go func() { <-r.ready; _ = killRun(r) }()
		return nil
	}
}

func killRun(r *run) error {
	if r.cmd == nil {
		return nil
	}
	select {
	case <-r.done:
		return nil
	default:
	}
	treeErr := r.tree.kill()
	if treeErr != nil {
		_ = r.cmd.Process.Kill()
		return fmt.Errorf("terminate process tree: %w", treeErr)
	}
	return nil
}

func (m *Manager) SendCommand(id, text string) error {
	if strings.ContainsAny(text, "\r\n") || strings.TrimSpace(text) == "" || len(text) > maxLineBytes {
		return errors.New("command must be a nonempty single line of at most 16384 bytes")
	}
	m.mu.Lock()
	s := m.get(id)
	r := s.run
	state := s.snapshot.State
	m.mu.Unlock()
	if r == nil || state != model.Running {
		return errors.New("server is not running")
	}
	c := command{text: text, result: make(chan error, 1)}
	select {
	case r.commands <- c:
	case <-r.done:
		return errors.New("server has exited")
	default:
		return errors.New("command queue is full")
	}
	timer := time.NewTimer(commandTimeout)
	defer timer.Stop()
	select {
	case err := <-c.result:
		return err
	case <-r.done:
		return errors.New("server has exited")
	case <-timer.C:
		return errors.New("command write timed out; queued command may still be delivered")
	}
}

func (m *Manager) Snapshot(id string) model.RuntimeSnapshot {
	m.mu.Lock()
	defer m.mu.Unlock()
	return cloneSnapshot(m.get(id).snapshot)
}
func (m *Manager) Logs(id string, after uint64) []model.LogEntry {
	m.mu.Lock()
	defer m.mu.Unlock()
	s := m.get(id)
	result := make([]model.LogEntry, 0, len(s.logs))
	for _, entry := range s.logs {
		if entry.Sequence > after {
			result = append(result, entry)
		}
	}
	return result
}

func (m *Manager) readOutput(id string, r *run, stream string, pipe *os.File) {
	defer pipe.Close()
	reader := bufio.NewReaderSize(pipe, maxLineBytes)
	for {
		// ReadSlice bounds memory even for malicious or newline-free output.
		line, err := reader.ReadSlice('\n')
		if len(line) > 0 {
			m.appendLog(id, r, stream, strings.TrimSuffix(strings.TrimSuffix(string(line), "\n"), "\r"))
		}
		if err != nil && err != bufio.ErrBufferFull {
			return
		}
	}
}

func (m *Manager) appendLog(id string, r *run, stream, message string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s := m.get(id)
	if s.run != r {
		return
	}
	s.sequence++
	entry := model.LogEntry{ServerID: id, RunID: s.snapshot.RunID, Sequence: s.sequence, Time: time.Now(), Stream: stream, Message: message}
	s.logs = append(s.logs, entry)
	s.logBytes += len(message)
	for len(s.logs) > maxLogEntries || s.logBytes > maxLogBytes {
		s.logBytes -= len(s.logs[0].Message)
		s.logs[0] = model.LogEntry{}
		s.logs = s.logs[1:]
	}
	m.emit(model.Event{Type: "log", ServerID: id, Log: &entry})
}

// Shutdown first waits for graceful stops until ctx expires, then gives forced
// tree termination a separate bounded cleanup window.
func (m *Manager) Shutdown(ctx context.Context) error {
	m.mu.Lock()
	m.closed = true
	ids := make([]string, 0, len(m.servers))
	for id, s := range m.servers {
		if s.run != nil {
			ids = append(ids, id)
		}
	}
	m.mu.Unlock()
	var wg sync.WaitGroup
	for _, id := range ids {
		wg.Add(1)
		go func(id string) { defer wg.Done(); _ = m.Stop(ctx, id) }(id)
	}
	wg.Wait()
	for _, id := range ids {
		_ = m.Kill(id)
	}
	cleanup, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	var result error
	for _, id := range ids {
		m.mu.Lock()
		r := m.get(id).run
		m.mu.Unlock()
		if r != nil {
			select {
			case <-r.done:
			case <-cleanup.Done():
				result = errors.New("process cleanup timed out")
			}
		}
	}
	m.dispatchOnce.Do(func() { close(m.dispatchDone) })
	return result
}
