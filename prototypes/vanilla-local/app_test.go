//go:build windows

package main

import (
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestSetupRequiresConsent(t *testing.T) {
	app, err := newApp(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := app.Setup(false); err == nil {
		t.Fatal("setup without consent succeeded")
	}
	if err := app.Import(t.TempDir(), false); err == nil {
		t.Fatal("import without consent succeeded")
	}
	if _, err := os.Stat(filepath.Join(app.store.ActiveDir(), "eula.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("setup without consent modified EULA: %v", err)
	}
}

func TestRepairWithOfficialJar(t *testing.T) {
	jar := os.Getenv("GAMEDOCK_TEST_SERVER_JAR")
	if jar == "" {
		t.Skip("set GAMEDOCK_TEST_SERVER_JAR to the official Vanilla 1.21.11 JAR")
	}
	t.Setenv("GAMEDOCK_SERVER_JAR", jar)
	root := t.TempDir()
	app, err := newApp(root)
	if err != nil {
		t.Fatal(err)
	}
	// Simulate the UI consent flag only inside the test data directory.
	if err := app.Setup(true); err != nil {
		t.Fatal(err)
	}
	if !app.Status().Ready {
		t.Fatal("validated server did not become ready for launch")
	}
	if err := os.WriteFile(filepath.Join(app.store.ActiveDir(), "server.jar"), []byte("corrupted"), 0600); err != nil {
		t.Fatal(err)
	}
	app, err = newApp(root)
	if err != nil {
		t.Fatal(err)
	}
	if status := app.Status(); status.Ready || status.Notice == "" {
		t.Fatalf("corrupted JAR did not reopen setup: %+v", status)
	}
	if err := app.Setup(true); err != nil {
		t.Fatal(err)
	}
	if !app.Status().Ready {
		t.Fatal("server did not become ready after repair")
	}
}

func TestCheckLocalPort(t *testing.T) {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	port := listener.Addr().(*net.TCPAddr).Port
	if err := checkLocalPort(port); err == nil || !strings.Contains(err.Error(), "занят") {
		t.Fatalf("expected occupied port diagnosis, got %v", err)
	}
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	if err := checkLocalPort(port); err != nil {
		t.Fatalf("expected available port, got %v", err)
	}
}

func TestValidateLaunchWorld(t *testing.T) {
	t.Run("missing", func(t *testing.T) {
		if err := validateLaunchWorld(filepath.Join(t.TempDir(), "world")); err != nil {
			t.Fatal(err)
		}
	})
	for _, tc := range []struct {
		name  string
		files map[string]string
		fail  bool
	}{
		{name: "empty"},
		{name: "fresh lock", files: map[string]string{"session.lock": "☃"}},
		{name: "empty lock", files: map[string]string{"session.lock": ""}},
		{name: "empty vanilla folders", files: map[string]string{"region/": "", "DIM1/": "", "datapacks/": ""}},
		{name: "unexpected lock", files: map[string]string{"session.lock": "abc"}, fail: true},
		{name: "old metadata", files: map[string]string{"level.dat_old": "old"}, fail: true},
		{name: "damaged metadata", files: map[string]string{"level.dat": "broken"}, fail: true},
		{name: "existing chunk", files: map[string]string{"region/r.0.0.mca": "chunk"}, fail: true},
		{name: "unknown folder", files: map[string]string{"unknown/": ""}, fail: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			for name, data := range tc.files {
				path := filepath.Join(dir, name)
				if strings.HasSuffix(name, "/") {
					if err := os.MkdirAll(path, 0700); err != nil {
						t.Fatal(err)
					}
					continue
				}
				if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte(data), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if err := validateLaunchWorld(dir); (err != nil) != tc.fail {
				t.Fatalf("validateLaunchWorld: %v, want failure %v", err, tc.fail)
			}
			for name, want := range tc.files {
				if strings.HasSuffix(name, "/") {
					continue
				}
				data, err := os.ReadFile(filepath.Join(dir, name))
				if err != nil || string(data) != want {
					t.Fatalf("%s changed: %q, %v", name, data, err)
				}
			}
		})
	}
}

func TestValidateLaunchWorldRejectsJunctions(t *testing.T) {
	target := t.TempDir()
	root := t.TempDir()
	junction := filepath.Join(root, "region")
	output, err := exec.Command("cmd.exe", "/c", "mklink", "/J", junction, target).CombinedOutput()
	if err != nil {
		t.Skipf("creating a junction is unavailable: %v: %s", err, output)
	}
	for _, dir := range []string{root, junction} {
		if err := validateLaunchWorld(dir); err == nil {
			t.Errorf("accepted world with junction at %s", dir)
		}
	}
}
