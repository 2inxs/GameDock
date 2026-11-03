//go:build !client

package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gamedock/internal/model"
)

func TestDesktopPersistenceAndDisconnectedStorage(t *testing.T) {
	dataDir := filepath.Join(t.TempDir(), "local-settings")
	t.Setenv("GAMEDOCK_DATA_DIR", dataDir)
	files := filepath.Join(t.TempDir(), "Server files")
	working := filepath.Join(t.TempDir(), "Мир друзей")
	for _, dir := range []string{files, working} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	java := filepath.Join(files, "java.exe")
	for _, path := range []string{java, filepath.Join(files, "server.jar")} {
		if err := os.WriteFile(path, []byte("test fixture"), 0700); err != nil {
			t.Fatal(err)
		}
	}
	a := NewApp()
	a.startup(context.Background())
	if a.startupError != nil {
		t.Fatal(a.startupError)
	}
	t.Cleanup(func() { a.shutdown(context.Background()) })
	views, err := a.ListServers()
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(views)
	if err != nil || string(encoded) != "[]" {
		t.Fatalf("new app list: %s %v", encoded, err)
	}
	saved, err := a.SaveServer(model.Server{Name: "Мир друзей", Game: "minecraft", JavaPath: java, JarPath: "server.jar", FilesDir: files, WorkingDir: working, MinMemoryMB: 1024, MaxMemoryMB: 2048})
	if err != nil {
		t.Fatal(err)
	}
	if saved.ID == "" {
		t.Fatal("saved config lacks ID")
	}
	if _, err := a.SaveSettings(model.Settings{LibraryDir: files}); err != nil {
		t.Fatal(err)
	}
	if _, err := a.StartServer(saved.ID); err == nil || !strings.Contains(err.Error(), "eula") {
		t.Fatalf("expected EULA error before launch, got %v", err)
	}
	a.shutdown(context.Background())
	if err := os.Rename(files, files+"-disconnected"); err != nil {
		t.Fatal(err)
	}
	b := NewApp()
	b.startup(context.Background())
	defer b.shutdown(context.Background())
	if b.startupError != nil {
		t.Fatal(b.startupError)
	}
	views, err = b.ListServers()
	if err != nil {
		t.Fatal(err)
	}
	if len(views) != 1 || views[0].Available || views[0].Config.ID != saved.ID || views[0].Runtime.State != model.Stopped {
		t.Fatalf("persisted offline config: %+v", views)
	}
	settings, err := b.GetSettings()
	if err != nil || settings.LibraryDir != files {
		t.Fatalf("library setting lost: %+v %v", settings, err)
	}
	if _, err := b.StartServer(saved.ID); err == nil {
		t.Fatal("launch allowed with disconnected storage")
	}
	if err := b.DeleteServer(saved.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(working); err != nil {
		t.Fatalf("deleting config removed world folder: %v", err)
	}
}

func TestDesktopStartupFailureIsVisible(t *testing.T) {
	file := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(file, []byte("fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GAMEDOCK_DATA_DIR", file)
	a := NewApp()
	a.startup(context.Background())
	if a.GetAppInfo().Error == "" {
		t.Fatal("startup failure is hidden")
	}
	if _, err := a.ListServers(); err == nil {
		t.Fatal("uninitialized app returned success")
	}
}
