package service

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gamedock/internal/model"
	"gamedock/internal/process"
	"gamedock/internal/storage"
)

const integrationJar = "gamedock-service-integration.jar"

// The copied test executable behaves like Java only for this exact fixture.
// Normal test invocations continue through the standard testing entry point.
func TestMain(m *testing.M) {
	if len(os.Args) == 6 && os.Args[1] == "-Xms512M" && os.Args[2] == "-Xmx4096M" &&
		os.Args[3] == "-jar" && filepath.Base(os.Args[4]) == integrationJar && os.Args[5] == "nogui" {
		cwd, err := os.Getwd()
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
		fmt.Println("WORKDIR:" + cwd)
		fmt.Println("JAR:" + os.Args[4])
		scanner := bufio.NewScanner(os.Stdin)
		for scanner.Scan() {
			command := scanner.Text()
			fmt.Println("COMMAND:" + command)
			if command == "stop" {
				fmt.Println("STOPPED")
				os.Exit(0)
			}
		}
		if scanner.Err() != nil {
			os.Exit(2)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func TestRealProcessAndSQLiteLifecycle(t *testing.T) {
	ctx := context.Background()
	repo, err := storage.Open(filepath.Join(t.TempDir(), "Локальная база", "gamedock.db"))
	if err != nil {
		t.Fatal(err)
	}
	manager := process.New(nil)
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if err := manager.Shutdown(cleanup); err != nil {
			t.Errorf("process cleanup: %v", err)
		}
		if err := repo.Close(); err != nil {
			t.Errorf("database cleanup: %v", err)
		}
	})
	svc := New(repo, manager)
	config := serverFiles(t)
	if config.FilesDir == config.WorkingDir {
		t.Fatal("fixture must have independent files and working directories")
	}
	copyTestExecutable(t, config.JavaPath)
	if err := os.Rename(filepath.Join(config.FilesDir, config.JarPath), filepath.Join(config.FilesDir, integrationJar)); err != nil {
		t.Fatal(err)
	}
	config.JarPath = integrationJar
	writeEULA(t, config.WorkingDir, "eula=true\n")
	server, err := svc.SaveServer(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	first, err := svc.StartServer(ctx, server.ID)
	if err != nil || first.State != model.Running || first.PID <= 0 {
		t.Fatalf("start = %+v, %v", first, err)
	}
	waitForLog(t, manager, server.ID, 0, "WORKDIR:"+server.WorkingDir)
	jarLog := waitForLog(t, manager, server.ID, 0, "JAR:"+filepath.Join(server.FilesDir, integrationJar))
	if err := svc.SendCommand(ctx, server.ID, "say Привет мир"); err != nil {
		t.Fatal(err)
	}
	commandLog := waitForLog(t, manager, server.ID, jarLog.Sequence, "COMMAND:say Привет мир")
	stopCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := svc.StopServer(stopCtx, server.ID); err != nil {
		t.Fatal(err)
	}
	stopped := manager.Snapshot(server.ID)
	if stopped.State != model.Stopped || stopped.PID != 0 || stopped.ExitCode == nil || *stopped.ExitCode != 0 {
		t.Fatalf("graceful stop = %+v", stopped)
	}
	stopLog := waitForLog(t, manager, server.ID, commandLog.Sequence, "STOPPED")
	second, err := svc.StartServer(ctx, server.ID)
	if err != nil || second.State != model.Running || second.RunID == first.RunID {
		t.Fatalf("restart = %+v, %v", second, err)
	}
	restartLog := waitForLog(t, manager, server.ID, stopLog.Sequence, "WORKDIR:"+server.WorkingDir)
	if restartLog.RunID != second.RunID {
		t.Fatalf("restart log belongs to wrong run: %+v", restartLog)
	}
	shutdownCtx, shutdownCancel := context.WithTimeout(ctx, 10*time.Second)
	defer shutdownCancel()
	if err := manager.Shutdown(shutdownCtx); err != nil {
		t.Fatal(err)
	}
	if state := manager.Snapshot(server.ID); state.State != model.Stopped || state.PID != 0 {
		t.Fatalf("shutdown = %+v", state)
	}
	if _, err := svc.StartServer(ctx, server.ID); err == nil {
		t.Fatal("manager accepted a new process after shutdown")
	}
	// The service uses stdout pipes and leaves both selected directories intact.
	if _, err := os.Stat(filepath.Join(server.FilesDir, integrationJar)); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(server.WorkingDir, "eula.txt")); err != nil {
		t.Fatal(err)
	}
}

func copyTestExecutable(t *testing.T, destination string) {
	t.Helper()
	path, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	src, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()
	dst, err := os.OpenFile(destination, os.O_WRONLY|os.O_TRUNC, 0700)
	if err != nil {
		t.Fatal(err)
	}
	_, copyErr := io.Copy(dst, src)
	closeErr := dst.Close()
	if copyErr != nil {
		t.Fatal(copyErr)
	}
	if closeErr != nil {
		t.Fatal(closeErr)
	}
}

func waitForLog(t *testing.T, manager *process.Manager, id string, after uint64, message string) model.LogEntry {
	t.Helper()
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	deadline := time.NewTimer(10 * time.Second)
	defer deadline.Stop()
	for {
		for _, entry := range manager.Logs(id, after) {
			if strings.EqualFold(entry.Message, message) {
				return entry
			}
		}
		select {
		case <-ticker.C:
		case <-deadline.C:
			t.Fatalf("timed out waiting for log %q; snapshot=%+v logs=%+v", message, manager.Snapshot(id), manager.Logs(id, after))
			return model.LogEntry{}
		}
	}
}
