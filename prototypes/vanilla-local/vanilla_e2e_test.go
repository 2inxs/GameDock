//go:build windows

package main

import (
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"gamedock/internal/vanilla"
)

const vanillaE2EAddress = "127.0.0.1:25565"

func TestVanillaWorldRoundTrip(t *testing.T) {
	if os.Getenv("GAMEDOCK_E2E_ACCEPT_EULA") != "1" {
		t.Skip("accept the Minecraft EULA for the temporary test servers with GAMEDOCK_E2E_ACCEPT_EULA=1")
	}
	jar := os.Getenv("GAMEDOCK_TEST_SERVER_JAR")
	java := os.Getenv("GAMEDOCK_TEST_JAVA")
	if jar == "" || java == "" {
		t.Fatal("set GAMEDOCK_TEST_SERVER_JAR to the official Vanilla 1.21.11 JAR and GAMEDOCK_TEST_JAVA to Java 21 x64 java.exe")
	}
	jar, err := filepath.Abs(jar)
	if err != nil {
		t.Fatal(err)
	}
	java, err = filepath.Abs(java)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("GAMEDOCK_SERVER_JAR", jar)
	checkVanillaE2EPort(t)

	source := newVanillaE2EApp(t)
	if err := source.Setup(true); err != nil {
		t.Fatalf("prepare fresh server: %v", err)
	}
	if status := source.Status(); !status.Ready || status.Version != "1.21.11" {
		t.Fatalf("fresh server preparation: %+v", status)
	}
	t.Log("launching a fresh Vanilla 1.21.11 world")
	launchVanillaE2E(t, source, java)
	stopVanillaE2E(t, source)

	sourceWorld := filepath.Join(source.store.ActiveDir(), "world")
	if err := vanilla.ValidateWorld(sourceWorld); err != nil {
		t.Fatalf("validate saved fresh world: %v", err)
	}
	const markerName = "gamedock-e2e-marker.txt"
	const original = "original world data"
	const modified = "modified world data"
	sourceMarker := filepath.Join(sourceWorld, markerName)
	if err := os.WriteFile(sourceMarker, []byte(original), 0600); err != nil {
		t.Fatal(err)
	}

	t.Log("importing a copy into a separate GameDock installation")
	imported := newVanillaE2EApp(t)
	if err := imported.Import(source.store.ActiveDir(), true); err != nil {
		t.Fatalf("import stopped server: %v", err)
	}
	importedMarker := filepath.Join(imported.store.ActiveDir(), "world", markerName)
	checkVanillaE2EMarker(t, importedMarker, original)

	before, err := imported.Backup()
	if err != nil {
		t.Fatalf("back up original import: %v", err)
	}
	if err := os.WriteFile(importedMarker, []byte(modified), 0600); err != nil {
		t.Fatal(err)
	}
	checkVanillaE2EMarker(t, sourceMarker, original)
	after, err := imported.Backup()
	if err != nil {
		t.Fatalf("back up modified import: %v", err)
	}
	if ids := imported.Status().Backups; before == after || !slices.Contains(ids, before) || !slices.Contains(ids, after) {
		t.Fatalf("expected two listed backups %q and %q, got %v", before, after, ids)
	}

	t.Log("restoring the original snapshot and launching the restored world")
	if err := imported.Restore(before); err != nil {
		t.Fatalf("restore original import: %v", err)
	}
	checkVanillaE2EMarker(t, importedMarker, original)
	launchVanillaE2E(t, imported, java)
	stopVanillaE2E(t, imported)
	checkVanillaE2EMarker(t, importedMarker, original)
	checkVanillaE2EMarker(t, sourceMarker, original)
}

func newVanillaE2EApp(t *testing.T) *App {
	t.Helper()
	app, err := newApp(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := app.Stop(); err != nil {
			if forceErr := app.ForceStop(); forceErr != nil {
				t.Errorf("clean up server: stop: %v, force stop: %v", err, forceErr)
			}
		}
	})
	return app
}

func checkVanillaE2EPort(t *testing.T) {
	t.Helper()
	listener, err := net.Listen("tcp4", vanillaE2EAddress)
	if err != nil {
		t.Fatalf("the Vanilla test needs a free loopback port %s: %v", vanillaE2EAddress, err)
	}
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
}

func launchVanillaE2E(t *testing.T, app *App, java string) {
	t.Helper()
	if err := app.Start(java, 1024); err != nil {
		t.Fatalf("start Vanilla: %v", err)
	}
	deadline := time.Now().Add(3 * time.Minute)
	for {
		status := app.Status()
		switch status.State {
		case "running":
			return
		case "starting":
			if time.Now().Before(deadline) {
				time.Sleep(200 * time.Millisecond)
				continue
			}
		}
		t.Fatalf("Vanilla readiness: state=%s notice=%q\n%s", status.State, status.Notice, strings.Join(status.Lines, "\n"))
	}
}

func stopVanillaE2E(t *testing.T, app *App) {
	t.Helper()
	if err := app.Stop(); err != nil {
		t.Fatalf("stop Vanilla: %v\n%s", err, strings.Join(app.Status().Lines, "\n"))
	}
	status := app.Status()
	if status.State != "stopped" || !slices.ContainsFunc(status.Lines, func(line string) bool {
		return strings.Contains(line, "All dimensions are saved")
	}) {
		t.Fatalf("Vanilla graceful shutdown: state=%s notice=%q\n%s", status.State, status.Notice, strings.Join(status.Lines, "\n"))
	}
	checkVanillaE2EPort(t)
}

func checkVanillaE2EMarker(t *testing.T, path, expected string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != expected {
		t.Fatalf("world marker %s: got %q, want %q", path, data, expected)
	}
}
