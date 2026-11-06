// Package runner owns the lifetime and console output of a Vanilla server process.
package runner

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	shutdownGrace = 20 * time.Second
	consoleLines  = 160
	lineBytes     = 2048
)

// ErrStopTimeout means the server did not exit within the graceful shutdown period.
var ErrStopTimeout = errors.New("server exceeded the graceful shutdown time limit")

var errForcedStop = errors.New("server was forcibly stopped")

// Status describes the managed process. Running means Java has been launched.
type Status struct {
	State     string   `json:"state"`
	Ready     bool     `json:"ready"`
	PID       int      `json:"pid,omitempty"`
	Port      int      `json:"port,omitempty"`
	LastError string   `json:"lastError,omitempty"`
	Lines     []string `json:"lines"`
}

// Runner manages at most one server. Its zero value is ready to use.
type Runner struct {
	mu        sync.Mutex
	active    *server
	console   *console
	lastError string
}

type server struct {
	cmd      *exec.Cmd
	stdin    *os.File
	job      processJob
	port     int
	stopping bool
	forced   bool
	done     chan struct{}
	probeCtx context.Context
	endProbe context.CancelFunc
	stopErr  error
	exitErr  error
}

// Start launches Java in the provisioned directory. The context bounds startup.
func (r *Runner) Start(ctx context.Context, dir, javaPath string, memoryMiB, port int) error {
	if !filepath.IsAbs(dir) || !filepath.IsAbs(javaPath) {
		return errors.New("server directory and Java executable must be absolute paths")
	}
	if memoryMiB <= 0 || port < 1 || port > 65535 {
		return errors.New("invalid server memory or port")
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if r.active != nil {
		return errors.New("server process is already active")
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	input, stdin, err := os.Pipe()
	if err != nil {
		return fmt.Errorf("open server console input: %w", err)
	}
	defer input.Close()

	log := &console{}
	cmd := exec.Command(javaPath, fmt.Sprintf("-Xmx%dm", memoryMiB), "-jar", "server.jar", "nogui")
	cmd.Dir = dir
	cmd.Env = JavaEnvironment(cmd.Environ())
	cmd.Stdin = input
	cmd.Stdout, cmd.Stderr = log, log
	cmd.WaitDelay = 2 * time.Second
	r.console = log
	r.lastError = ""

	job, err := startProcess(ctx, cmd)
	if err != nil {
		stdin.Close()
		r.lastError = err.Error()
		return err
	}
	probeCtx, endProbe := context.WithCancel(context.Background())
	s := &server{
		cmd: cmd, stdin: stdin, job: job, port: port, done: make(chan struct{}),
		probeCtx: probeCtx, endProbe: endProbe,
	}
	r.active = s
	go r.wait(s)
	return nil
}

// JavaEnvironment removes inherited variables that can inject Java launch flags.
func JavaEnvironment(environment []string) []string {
	filtered := make([]string, 0, len(environment))
	for _, entry := range environment {
		key, _, _ := strings.Cut(entry, "=")
		switch strings.ToUpper(key) {
		case "JAVA_TOOL_OPTIONS", "JDK_JAVA_OPTIONS", "_JAVA_OPTIONS":
			continue
		}
		filtered = append(filtered, entry)
	}
	return filtered
}

func (r *Runner) wait(s *server) {
	err := s.cmd.Wait()
	s.endProbe()
	r.mu.Lock()
	defer r.mu.Unlock()
	s.stdin.Close()
	s.job.close()
	switch {
	case s.forced:
		s.exitErr = errForcedStop
	case err != nil:
		s.exitErr = fmt.Errorf("server process exited: %w", err)
		r.lastError = s.exitErr.Error()
	case !s.stopping:
		s.exitErr = errors.New("сервер завершился без команды остановки. Подробности в журнале")
		r.lastError = s.exitErr.Error()
	}
	r.active = nil
	close(s.done)
}

// Stop asks Vanilla to save and exit. The process remains attached if the wait times out.
func (r *Runner) Stop(ctx context.Context) error {
	r.mu.Lock()
	s := r.active
	if s == nil {
		r.mu.Unlock()
		return nil
	}
	if err := ctx.Err(); err != nil {
		r.mu.Unlock()
		return err
	}
	if !s.stopping {
		s.stopping = true
		s.endProbe()
		_, err := s.stdin.WriteString("stop\n")
		s.stdin.Close()
		if err != nil {
			s.stopErr = fmt.Errorf("send server stop command: %w", err)
			r.lastError = s.stopErr.Error()
		}
	}
	stopErr := s.stopErr
	r.mu.Unlock()
	if err := waitForShutdown(ctx, s.done); err != nil {
		return errors.Join(err, stopErr)
	}
	return s.exitErr
}

// ForceStop terminates the Windows job and waits for process cleanup.
func (r *Runner) ForceStop(ctx context.Context) error {
	r.mu.Lock()
	s := r.active
	if s == nil {
		r.mu.Unlock()
		return nil
	}
	if !s.forced {
		s.stopping = true
		s.forced = true
		s.endProbe()
		s.job.close()
		r.lastError = errForcedStop.Error()
	}
	r.mu.Unlock()
	if err := waitForShutdown(ctx, s.done); err != nil {
		return fmt.Errorf("wait for forced server shutdown: %w", err)
	}
	return nil
}

func waitForShutdown(ctx context.Context, done <-chan struct{}) error {
	timer := time.NewTimer(shutdownGrace)
	defer timer.Stop()
	var err error
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		err = ctx.Err()
	case <-timer.C:
		err = ErrStopTimeout
	}
	select {
	case <-done:
		return nil
	default:
		return err
	}
}

// LaunchProbeContext is canceled as soon as shutdown starts or the process exits.
func (r *Runner) LaunchProbeContext() (context.Context, int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.active == nil {
		return nil, 0
	}
	return r.active.probeCtx, r.active.cmd.Process.Pid
}

// Status returns a snapshot that callers may modify independently.
func (r *Runner) Status() Status {
	r.mu.Lock()
	defer r.mu.Unlock()
	status := Status{State: "stopped", LastError: r.lastError, Lines: []string{}}
	if r.console != nil {
		status.Lines, status.Ready = r.console.snapshot()
	}
	if s := r.active; s != nil {
		status.State = "running"
		if s.stopping {
			status.State = "stopping"
		}
		status.PID, status.Port = s.cmd.Process.Pid, s.port
	}
	return status
}

type console struct {
	mu      sync.Mutex
	ring    [consoleLines]string
	count   int
	next    int
	pending []byte
	clipped bool
	ready   bool
}

func (c *console) Write(data []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	written := len(data)
	for len(data) > 0 {
		segment, rest, complete := bytes.Cut(data, []byte{'\n'})
		keep := min(len(segment), lineBytes-len(c.pending))
		c.pending = append(c.pending, segment[:keep]...)
		c.clipped = c.clipped || keep < len(segment)
		if complete {
			line := c.currentLine()
			c.ready = c.ready || (strings.Contains(line, "[Server thread/INFO]: Done (") &&
				strings.HasSuffix(line, `! For help, type "help"`))
			c.ring[c.next] = line
			c.next = (c.next + 1) % len(c.ring)
			c.count = min(c.count+1, len(c.ring))
			c.pending, c.clipped = c.pending[:0], false
		}
		data = rest
	}
	return written, nil
}

func (c *console) currentLine() string {
	line := strings.TrimSuffix(string(c.pending), "\r")
	if c.clipped {
		line += "…"
	}
	return line
}

func (c *console) snapshot() ([]string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	result := make([]string, 0, c.count+1)
	start := (c.next + len(c.ring) - c.count) % len(c.ring)
	for i := 0; i < c.count; i++ {
		result = append(result, c.ring[(start+i)%len(c.ring)])
	}
	if len(c.pending) > 0 {
		result = append(result, c.currentLine())
	}
	return result, c.ready
}
