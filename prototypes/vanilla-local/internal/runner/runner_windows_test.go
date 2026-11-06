package runner

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func init() {
	if len(os.Args) < 2 || !strings.HasPrefix(os.Args[1], "-Xmx") {
		return
	}
	if os.Getenv("JAVA_TOOL_OPTIONS") != "" {
		os.Exit(4)
	}
	fmt.Println("fake server ready")
	if os.Getenv("GAMEDOCK_RUNNER_UNREQUESTED_EXIT") != "" {
		os.Exit(0)
	}
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil || strings.TrimSpace(line) != "stop" {
		os.Exit(5)
	}
	if os.Getenv("GAMEDOCK_RUNNER_IGNORE_STOP") != "" {
		time.Sleep(time.Minute)
	}
	if path := os.Getenv("GAMEDOCK_RUNNER_RELEASE_FILE"); path != "" {
		for {
			if _, err := os.Stat(path); err == nil {
				break
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
	if os.Getenv("GAMEDOCK_RUNNER_FAIL_STOP") != "" {
		os.Exit(6)
	}
	fmt.Println("fake server saved")
	os.Exit(0)
}

func TestServerProcessLifecycle(t *testing.T) {
	t.Setenv("JAVA_TOOL_OPTIONS", "-bad-argument")
	java, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var r Runner
	if err := r.Start(ctx, t.TempDir(), java, 512, 25565); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { r.ForceStop(context.Background()) })
	status := r.Status()
	if status.State != "running" || status.PID == 0 || status.Port != 25565 {
		t.Fatalf("unexpected start status: %+v", status)
	}
	if err := r.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	status = r.Status()
	if status.State != "stopped" || status.LastError != "" ||
		!slices.Equal(status.Lines, []string{"fake server ready", "fake server saved"}) {
		t.Fatalf("unexpected stop status: %+v", status)
	}
}

func TestServerProcessStopTimeoutKeepsJobUntilForced(t *testing.T) {
	t.Setenv("GAMEDOCK_RUNNER_IGNORE_STOP", "1")
	java, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var r Runner
	if err := r.Start(ctx, t.TempDir(), java, 512, 25565); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { r.ForceStop(context.Background()) })
	pid := r.Status().PID
	for range 2 {
		short, cancelShort := context.WithTimeout(ctx, 100*time.Millisecond)
		err := r.Stop(short)
		cancelShort()
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("expected shutdown timeout, got %v", err)
		}
		status := r.Status()
		if status.State != "stopping" || status.PID != pid || status.LastError != "" {
			t.Fatalf("server was not left running after timeout: %+v", status)
		}
	}
	if err := r.ForceStop(ctx); err != nil {
		t.Fatal(err)
	}
	status := r.Status()
	if status.State != "stopped" || status.PID != 0 || status.LastError != errForcedStop.Error() {
		t.Fatalf("unexpected forced stop status: %+v", status)
	}
}

func TestServerProcessStopReportsFailedExit(t *testing.T) {
	t.Setenv("GAMEDOCK_RUNNER_FAIL_STOP", "1")
	java, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var r Runner
	if err := r.Start(ctx, t.TempDir(), java, 512, 25565); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { r.ForceStop(context.Background()) })
	var exitErr *exec.ExitError
	if err := r.Stop(ctx); !errors.As(err, &exitErr) || exitErr.ExitCode() != 6 {
		t.Fatalf("expected exit code 6, got %v", err)
	}
	if status := r.Status(); status.State != "stopped" || status.LastError == "" {
		t.Fatalf("unexpected failed stop status: %+v", status)
	}
}

func TestServerProcessCanFinishSavingAfterWaitTimeout(t *testing.T) {
	dir := t.TempDir()
	release := filepath.Join(dir, "finish-save")
	t.Setenv("GAMEDOCK_RUNNER_RELEASE_FILE", release)
	java, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var r Runner
	if err := r.Start(ctx, dir, java, 512, 25565); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { r.ForceStop(context.Background()) })
	probeCtx, _ := r.LaunchProbeContext()
	short, cancelShort := context.WithTimeout(ctx, 100*time.Millisecond)
	err = r.Stop(short)
	cancelShort()
	if !errors.Is(err, context.DeadlineExceeded) || !errors.Is(probeCtx.Err(), context.Canceled) {
		t.Fatalf("wait should time out and cancel startup probe, got stop=%v probe=%v", err, probeCtx.Err())
	}
	if err := os.WriteFile(release, nil, 0600); err != nil {
		t.Fatal(err)
	}
	if err := r.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	status := r.Status()
	if status.State != "stopped" || !slices.Contains(status.Lines, "fake server saved") {
		t.Fatalf("expected late clean shutdown: %+v", status)
	}
}

func TestServerProcessUnexpectedZeroExit(t *testing.T) {
	t.Setenv("GAMEDOCK_RUNNER_UNREQUESTED_EXIT", "1")
	java, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var r Runner
	if err := r.Start(ctx, t.TempDir(), java, 512, 25565); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { r.ForceStop(context.Background()) })
	for ctx.Err() == nil {
		status := r.Status()
		if status.State == "stopped" {
			if status.LastError == "" {
				t.Fatal("unexpected clean process exit was reported as a normal stop")
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("fake process did not exit")
}
