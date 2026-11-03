package storage

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"gamedock/internal/model"
)

func TestRepositoryMigrationAndRestart(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "локальные настройки", "gamedock.db")
	repo, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	settings, err := repo.GetSettings(ctx)
	if err != nil || settings.LibraryDir != "" {
		t.Fatalf("default settings = %+v, %v", settings, err)
	}
	server, err := repo.SaveServer(ctx, model.Server{
		Name: "Мир друзей", Game: "minecraft", JavaPath: `C:\Java 21\bin\java.exe`, JarPath: "сервер.jar",
		FilesDir: `E:\Мои серверы`, WorkingDir: `E:\Мои миры\Первый мир`, MinMemoryMB: 512, MaxMemoryMB: 4096,
	})
	if err != nil {
		t.Fatal(err)
	}
	if server.ID == "" || server.CreatedAt.IsZero() || server.UpdatedAt.IsZero() {
		t.Fatalf("missing identity/timestamps: %+v", server)
	}
	settings, err = repo.SaveSettings(ctx, model.Settings{LibraryDir: `E:\Библиотека серверов`})
	if err != nil {
		t.Fatal(err)
	}
	if err = repo.Close(); err != nil {
		t.Fatal(err)
	}
	repo, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()
	actual, err := repo.GetServer(ctx, server.ID)
	if err != nil || !reflect.DeepEqual(actual, server) {
		t.Fatalf("restart roundtrip = %+v, %v; want %+v", actual, err, server)
	}
	restoredSettings, err := repo.GetSettings(ctx)
	if err != nil || restoredSettings != settings {
		t.Fatalf("settings roundtrip = %+v, %v", restoredSettings, err)
	}
	actual.Name = "Новое название"
	actual, err = repo.SaveServer(ctx, actual)
	if err != nil {
		t.Fatal(err)
	}
	if !actual.CreatedAt.Equal(server.CreatedAt) {
		t.Fatal("update changed creation time")
	}
	list, err := repo.ListServers(ctx)
	if err != nil || len(list) != 1 || list[0].Name != actual.Name {
		t.Fatalf("list = %+v, %v", list, err)
	}
	if err = repo.DeleteServer(ctx, server.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = repo.GetServer(ctx, server.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing record error = %v", err)
	}
	if err = repo.DeleteServer(ctx, server.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("repeat delete error = %v", err)
	}
}

func TestRejectsNewerDatabaseVersion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "gamedock.db")
	repo, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = repo.db.Exec("PRAGMA user_version = 3"); err != nil {
		t.Fatal(err)
	}
	if err = repo.Close(); err != nil {
		t.Fatal(err)
	}
	if repo, err = Open(path); err == nil {
		repo.Close()
		t.Fatal("opened a newer database version")
	}
}

func TestMigrationFromVersionOnePreservesData(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "gamedock-v1.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`CREATE TABLE servers (
id TEXT PRIMARY KEY, name TEXT NOT NULL, game TEXT NOT NULL,
java_path TEXT NOT NULL, jar_path TEXT NOT NULL, files_dir TEXT NOT NULL,
working_dir TEXT NOT NULL, min_memory_mb INTEGER NOT NULL, max_memory_mb INTEGER NOT NULL,
created_at TEXT NOT NULL, updated_at TEXT NOT NULL);
CREATE TABLE settings (id INTEGER PRIMARY KEY CHECK (id = 1), library_dir TEXT NOT NULL);
INSERT INTO settings (id, library_dir) VALUES (1, 'E:\Мои серверы');
PRAGMA user_version = 1;`)
	if err != nil {
		db.Close()
		t.Fatal(err)
	}
	stamp := time.Date(2026, 10, 1, 12, 34, 56, 123, time.UTC)
	want := model.Server{ID: "existing-server", Name: "Старый мир", Game: "minecraft", JavaPath: `C:\Java 21\bin\java.exe`, JarPath: "server.jar", FilesDir: `E:\Мои серверы\Мир`, WorkingDir: `E:\Мои серверы\Мир`, MinMemoryMB: 512, MaxMemoryMB: 4096, CreatedAt: stamp, UpdatedAt: stamp}
	_, err = db.Exec(`INSERT INTO servers VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, want.ID, want.Name, want.Game, want.JavaPath, want.JarPath, want.FilesDir, want.WorkingDir, want.MinMemoryMB, want.MaxMemoryMB, stamp.Format(time.RFC3339Nano), stamp.Format(time.RFC3339Nano))
	if err != nil {
		db.Close()
		t.Fatal(err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	repo, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	actual, err := repo.GetServer(ctx, want.ID)
	if err != nil || !reflect.DeepEqual(actual, want) {
		repo.Close()
		t.Fatalf("migration changed server: %+v, %v; want %+v", actual, err, want)
	}
	settings, err := repo.GetSettings(ctx)
	if err != nil || settings.LibraryDir != `E:\Мои серверы` {
		repo.Close()
		t.Fatalf("migration changed settings: %+v %v", settings, err)
	}
	var version int
	if err = repo.db.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != 2 {
		repo.Close()
		t.Fatalf("schema version = %d, %v", version, err)
	}
	actual.JavaArgsFile = `libraries\net\neoforged\neoforge\21.1.219\win_args.txt`
	actual.JarPath = ""
	actual, err = repo.SaveServer(ctx, actual)
	if err != nil {
		repo.Close()
		t.Fatal(err)
	}
	if err = repo.Close(); err != nil {
		t.Fatal(err)
	}
	repo, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()
	restored, err := repo.GetServer(ctx, want.ID)
	if err != nil || !reflect.DeepEqual(restored, actual) {
		t.Fatalf("argfile restart roundtrip = %+v, %v; want %+v", restored, err, actual)
	}
	list, err := repo.ListServers(ctx)
	if err != nil || len(list) != 1 || list[0].JavaArgsFile != actual.JavaArgsFile {
		t.Fatalf("argfile list = %+v, %v", list, err)
	}
	actual.JavaArgsFile = ""
	actual.JarPath = "server.jar"
	if _, err = repo.SaveServer(ctx, actual); err != nil {
		t.Fatal(err)
	}
	restored, err = repo.GetServer(ctx, want.ID)
	if err != nil || restored.JavaArgsFile != "" || restored.JarPath != "server.jar" {
		t.Fatalf("clear argfile = %+v, %v", restored, err)
	}
}
