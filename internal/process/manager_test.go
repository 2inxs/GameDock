package process

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"gamedock/internal/model"
)

// TestHelperProcess runs only in explicitly launched child copies of this binary.
func TestHelperProcess(t *testing.T) {
	mode := ""
	for i, arg := range os.Args {
		if arg == "--gamedock-helper" && i+1 < len(os.Args) {
			mode = os.Args[i+1]
		}
	}
	if mode == "" {
		return
	}
	fmt.Println("ready")
	fmt.Fprintln(os.Stderr, "stderr-ready")
	switch mode {
	case "crash":
		os.Exit(17)
	case "quick":
		os.Exit(0)
	case "logs":
		fmt.Println(strings.Repeat("x", 2*maxLogBytes))
		for i := 0; i < 2000; i++ {
			fmt.Printf("line-%d\n", i)
		}
	case "ignore":
		time.Sleep(60 * time.Second)
		os.Exit(0)
	}
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		line := scanner.Text()
		if line == "spawn" {
			child := exec.Command(os.Args[0], "-test.run=^TestHelperProcess$", "--", "--gamedock-helper", "ignore")
			child.Stdout, child.Stderr = os.Stdout, os.Stderr
			if err := child.Start(); err != nil {
				fmt.Printf("spawn-error:%v\n", err)
			} else {
				fmt.Printf("child:%d\n", child.Process.Pid)
			}
			continue
		}
		if line == "stop" {
			fmt.Println("goodbye")
			os.Exit(0)
		}
		fmt.Println("echo:" + line)
	}
	os.Exit(0)
}

func helper(mode string) model.LaunchSpec {
	return model.LaunchSpec{Executable: os.Args[0], Args: []string{"-test.run=^TestHelperProcess$", "--", "--gamedock-helper", mode}, StopCommand: "stop"}
}
func newTestManager(t *testing.T) *Manager {
	t.Helper()
	m := New(nil)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		defer cancel()
		if err := m.Shutdown(ctx); err != nil {
			t.Errorf("cleanup: %v", err)
		}
	})
	return m
}
func eventually(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("condition not met before timeout")
}
func hasLog(m *Manager, id, message string) bool {
	for _, l := range m.Logs(id, 0) {
		if l.Message == message {
			return true
		}
	}
	return false
}

func TestOutputCommandsStopRestart(t *testing.T) {
	m := newTestManager(t)
	first, err := m.Start("one", helper("normal"))
	if err != nil {
		t.Fatal(err)
	}
	if first.State != model.Running || first.PID == 0 {
		t.Fatalf("unexpected snapshot: %+v", first)
	}
	eventually(t, func() bool { return hasLog(m, "one", "ready") && hasLog(m, "one", "stderr-ready") })
	if err := m.SendCommand("one", "say hello"); err != nil {
		t.Fatal(err)
	}
	eventually(t, func() bool { return hasLog(m, "one", "echo:say hello") })
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := m.Stop(ctx, "one"); err != nil {
		t.Fatal(err)
	}
	stopped := m.Snapshot("one")
	if stopped.State != model.Stopped || stopped.PID != 0 || stopped.ExitCode == nil || *stopped.ExitCode != 0 {
		t.Fatalf("unexpected stop: %+v", stopped)
	}
	if !hasLog(m, "one", "goodbye") {
		t.Fatal("lost final stdout")
	}
	logs := m.Logs("one", 0)
	last := logs[len(logs)-1].Sequence
	second, err := m.Start("one", helper("normal"))
	if err != nil {
		t.Fatal(err)
	}
	if first.RunID == second.RunID {
		t.Fatal("run ID reused")
	}
	eventually(t, func() bool { return len(m.Logs("one", last)) >= 2 })
	for _, entry := range m.Logs("one", last) {
		if entry.Sequence <= last || entry.RunID != second.RunID {
			t.Fatalf("bad restarted log: %+v", entry)
		}
	}
}

func TestCrashAndQuickExit(t *testing.T) {
	m := newTestManager(t)
	for _, mode := range []string{"crash", "quick"} {
		for i := 0; i < 10; i++ {
			if _, err := m.Start(mode, helper(mode)); err != nil {
				t.Fatal(err)
			}
			eventually(t, func() bool { state := m.Snapshot(mode).State; return state == model.Stopped || state == model.Failed })
			snapshot := m.Snapshot(mode)
			wantCode, wantState := 0, model.Stopped
			if mode == "crash" {
				wantCode, wantState = 17, model.Failed
			}
			if snapshot.State != wantState || snapshot.ExitCode == nil || *snapshot.ExitCode != wantCode {
				t.Fatalf("bad final snapshot: %+v", snapshot)
			}
			if !hasLog(m, mode, "stderr-ready") {
				t.Fatal("lost crash output")
			}
		}
	}
}

func TestDuplicateStartsAndParallelCommands(t *testing.T) {
	m := newTestManager(t)
	var wg sync.WaitGroup
	results := make(chan error, 12)
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _, err := m.Start("one", helper("normal")); results <- err }()
	}
	wg.Wait()
	close(results)
	starts := 0
	for err := range results {
		if err == nil {
			starts++
		}
	}
	if starts != 1 {
		t.Fatalf("started %d processes", starts)
	}
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if err := m.SendCommand("one", fmt.Sprintf("command-%d", i)); err != nil {
				t.Error(err)
			}
		}(i)
	}
	wg.Wait()
	eventually(t, func() bool {
		count := 0
		for _, l := range m.Logs("one", 0) {
			if strings.HasPrefix(l.Message, "echo:command-") {
				count++
			}
		}
		return count == 20
	})
}

func TestStopTimeoutKillAndShutdown(t *testing.T) {
	m := newTestManager(t)
	if _, err := m.Start("one", helper("ignore")); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if err := m.Stop(ctx, "one"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("want timeout, got %v", err)
	}
	s := m.Snapshot("one")
	if s.State != model.Stopping || s.PID == 0 || s.ExitedAt != nil {
		t.Fatalf("untruthful live state: %+v", s)
	}
	if err := m.Kill("one"); err != nil {
		t.Fatal(err)
	}
	eventually(t, func() bool { return m.Snapshot("one").State == model.Stopped })
	for _, id := range []string{"two", "three"} {
		if _, err := m.Start(id, helper("ignore")); err != nil {
			t.Fatal(err)
		}
	}
	start := time.Now()
	if err := m.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	if time.Since(start) > 4*time.Second {
		t.Fatal("shutdown was not bounded")
	}
	for _, id := range []string{"two", "three"} {
		if m.Snapshot(id).PID != 0 {
			t.Fatal("process survived shutdown")
		}
	}
	if _, err := m.Start("four", helper("normal")); err == nil {
		t.Fatal("start after shutdown succeeded")
	}
}

func TestBoundedLogsAndSlowPublisher(t *testing.T) {
	block := make(chan struct{})
	m := New(func(model.Event) { <-block })
	defer close(block)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = m.Shutdown(ctx)
	})
	if _, err := m.Start("one", helper("logs")); err != nil {
		t.Fatal(err)
	}
	eventually(t, func() bool { return hasLog(m, "one", "line-1999") })
	logs := m.Logs("one", 0)
	if len(logs) > maxLogEntries {
		t.Fatal("too many log entries")
	}
	total := 0
	for i, l := range logs {
		total += len(l.Message)
		if len(l.Message) > maxLineBytes {
			t.Fatal("unbounded line")
		}
		if i > 0 && l.Sequence <= logs[i-1].Sequence {
			t.Fatal("non-monotonic sequence")
		}
	}
	if total > maxLogBytes {
		t.Fatal("unbounded log bytes")
	}
	if err := m.SendCommand("one", "responsive"); err != nil {
		t.Fatal(err)
	}
	eventually(t, func() bool { return hasLog(m, "one", "echo:responsive") })
}

func TestLaunchFailureAndSnapshotIsolation(t *testing.T) {
	m := newTestManager(t)
	if _, err := m.Start("one", model.LaunchSpec{Executable: filepath.Join(t.TempDir(), "missing-gamedock-executable.exe")}); err == nil {
		t.Fatal("missing executable launched")
	}
	s := m.Snapshot("one")
	if s.State != model.Failed || s.ExitedAt == nil {
		t.Fatalf("bad failure state: %+v", s)
	}
	*s.ExitedAt = time.Time{}
	if m.Snapshot("one").ExitedAt.IsZero() {
		t.Fatal("snapshot aliased internal timestamp")
	}
	if _, err := m.Start("one", helper("normal")); err != nil {
		t.Fatal(err)
	}
	if err := m.SendCommand("one", "one\ntwo"); err == nil {
		t.Fatal("accepted multiline command")
	}
}

func TestStopDuringStarting(t *testing.T) {
	m := newTestManager(t)
	observed := false
	for attempt := 0; attempt < 10 && !observed; attempt++ {
		result := make(chan model.RuntimeSnapshot, 1)
		failure := make(chan error, 1)
		go func() { snapshot, err := m.Start("one", helper("normal")); result <- snapshot; failure <- err }()
		deadline := time.Now().Add(5 * time.Second)
		for {
			state := m.Snapshot("one").State
			if state == model.Starting || state == model.Running {
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("launch did not begin")
			}
			runtime.Gosched()
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		err := m.Stop(ctx, "one")
		cancel()
		if err != nil {
			t.Fatal(err)
		}
		if err := <-failure; err != nil {
			t.Fatal(err)
		}
		if snapshot := <-result; snapshot.State == model.Stopping {
			observed = true
		}
		if m.Snapshot("one").State != model.Stopped {
			t.Fatal("in-flight start escaped stop")
		}
	}
	if !observed {
		t.Skip("OS launches completed before stop could observe the starting window")
	}
}

func TestBlockedStdinDoesNotBlockStop(t *testing.T) {
	m := newTestManager(t)
	if _, err := m.Start("one", helper("ignore")); err != nil {
		t.Fatal(err)
	}
	var writers sync.WaitGroup
	for i := 0; i < 70; i++ {
		writers.Add(1)
		go func() { defer writers.Done(); _ = m.SendCommand("one", strings.Repeat("x", maxLineBytes)) }()
	}
	time.Sleep(20 * time.Millisecond)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	started := time.Now()
	if err := m.Stop(ctx, "one"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("want stop timeout, got %v", err)
	}
	if time.Since(started) > time.Second {
		t.Fatal("stdin backpressure blocked stop deadline")
	}
	if err := m.Kill("one"); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { writers.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("command callers did not unblock on termination")
	}
	eventually(t, func() bool { return m.Snapshot("one").State == model.Stopped })
}
