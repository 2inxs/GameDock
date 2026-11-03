package service

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"gamedock/internal/model"
	"gamedock/internal/storage"
)

type fakeManager struct {
	states map[string]model.RuntimeSnapshot
	specs  map[string]model.LaunchSpec
}

func (m *fakeManager) Snapshot(id string) model.RuntimeSnapshot {
	if state, exists := m.states[id]; exists {
		return state
	}
	return model.RuntimeSnapshot{ServerID: id, State: model.Stopped}
}
func (m *fakeManager) Start(id string, spec model.LaunchSpec) (model.RuntimeSnapshot, error) {
	m.specs[id] = spec
	m.states[id] = model.RuntimeSnapshot{ServerID: id, State: model.Running, RunID: "test-run"}
	return m.states[id], nil
}
func (m *fakeManager) Stop(_ context.Context, id string) error {
	m.states[id] = model.RuntimeSnapshot{ServerID: id, State: model.Stopped}
	return nil
}
func (m *fakeManager) Kill(id string) error             { return m.Stop(context.Background(), id) }
func (m *fakeManager) SendCommand(string, string) error { return nil }

func testService(t *testing.T) (*Service, *fakeManager) {
	t.Helper()
	repo, err := storage.Open(filepath.Join(t.TempDir(), "local.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { repo.Close() })
	pm := &fakeManager{states: make(map[string]model.RuntimeSnapshot), specs: make(map[string]model.LaunchSpec)}
	return New(repo, pm), pm
}

func serverFiles(t *testing.T) model.Server {
	t.Helper()
	root := filepath.Join(t.TempDir(), "Внешний диск с пробелами")
	files := filepath.Join(root, "Файлы сервера")
	world := filepath.Join(root, "Миры и настройки")
	for _, dir := range []string{files, world} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	javaName := "java"
	if runtime.GOOS == "windows" {
		javaName = "java.exe"
	}
	java := filepath.Join(root, javaName)
	if err := os.WriteFile(java, []byte("fake executable"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(files, "сервер с пробелами.jar"), []byte("fake jar"), 0600); err != nil {
		t.Fatal(err)
	}
	return model.Server{Name: "Мой сервер", JavaPath: java, JarPath: "сервер с пробелами.jar", FilesDir: files, WorkingDir: world, MinMemoryMB: 512, MaxMemoryMB: 4096}
}

func writeEULA(t *testing.T, dir, value string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, "eula.txt"), []byte(value), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestExternalFilesLaunchAndEULAGuards(t *testing.T) {
	ctx := context.Background()
	svc, pm := testService(t)
	server, err := svc.SaveServer(ctx, serverFiles(t))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = svc.StartServer(ctx, server.ID); err == nil || !strings.Contains(err.Error(), "eula") {
		t.Fatalf("missing EULA = %v", err)
	}
	if _, err = os.Stat(filepath.Join(server.WorkingDir, "eula.txt")); !os.IsNotExist(err) {
		t.Fatalf("service created EULA: %v", err)
	}
	writeEULA(t, server.WorkingDir, "# eula=true\neula=false\n")
	if _, err = svc.StartServer(ctx, server.ID); err == nil {
		t.Fatal("accepted eula=false")
	}
	writeEULA(t, server.WorkingDir, "# The user accepted the license\neula=true\n")
	state, err := svc.StartServer(ctx, server.ID)
	if err != nil || state.State != model.Running {
		t.Fatalf("start = %+v, %v", state, err)
	}
	spec := pm.specs[server.ID]
	wantArgs := []string{"-Xms512M", "-Xmx4096M", "-jar", filepath.Join(server.FilesDir, server.JarPath), "nogui"}
	if spec.WorkingDir != server.WorkingDir || spec.Executable != server.JavaPath || !reflect.DeepEqual(spec.Args, wantArgs) {
		t.Fatalf("launch = %+v", spec)
	}
	if _, err = svc.SaveServer(ctx, server); err == nil {
		t.Fatal("edited running server")
	}
	if err = svc.DeleteServer(ctx, server.ID); err == nil {
		t.Fatal("deleted running record")
	}
	if _, err = svc.StartServer(ctx, server.ID); err == nil {
		t.Fatal("started twice")
	}
	if err = svc.StopServer(ctx, server.ID); err != nil {
		t.Fatal(err)
	}
	if err = svc.DeleteServer(ctx, server.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(filepath.Join(server.WorkingDir, "eula.txt")); err != nil {
		t.Fatalf("delete removed user file: %v", err)
	}
	if _, err = os.Stat(filepath.Join(server.FilesDir, server.JarPath)); err != nil {
		t.Fatalf("delete removed jar: %v", err)
	}
	entries, err := os.ReadDir(server.WorkingDir)
	if err != nil || len(entries) != 1 {
		t.Fatalf("write probe left files: %+v, %v", entries, err)
	}
}

func TestUnavailableDiskPreservesConfiguration(t *testing.T) {
	ctx := context.Background()
	svc, _ := testService(t)
	server, err := svc.SaveServer(ctx, serverFiles(t))
	if err != nil {
		t.Fatal(err)
	}
	offline := server.FilesDir + " offline"
	if err = os.Rename(server.FilesDir, offline); err != nil {
		t.Fatal(err)
	}
	views, err := svc.ListServers(ctx)
	if err != nil || len(views) != 1 {
		t.Fatalf("list = %+v, %v", views, err)
	}
	if views[0].Available || views[0].AvailabilityError == "" || views[0].Config.ID != server.ID {
		t.Fatalf("offline view = %+v", views[0])
	}
	if _, err = svc.StartServer(ctx, server.ID); err == nil {
		t.Fatal("started with disconnected files")
	}
	if err = os.Rename(offline, server.FilesDir); err != nil {
		t.Fatal(err)
	}
	views, err = svc.ListServers(ctx)
	if err != nil || !views[0].Available {
		t.Fatalf("reconnected view = %+v, %v", views, err)
	}
}

func TestSameWorkingDirectoryCannotRunTwice(t *testing.T) {
	ctx := context.Background()
	svc, _ := testService(t)
	config := serverFiles(t)
	writeEULA(t, config.WorkingDir, "eula=true")
	first, err := svc.SaveServer(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	config.Name = "Второй сервер"
	second, err := svc.SaveServer(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = svc.StartServer(ctx, first.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = svc.StartServer(ctx, second.ID); err == nil || !strings.Contains(err.Error(), "рабочая папка") {
		t.Fatalf("shared working dir = %v", err)
	}
	if _, err = svc.SaveServer(ctx, second); err == nil {
		t.Fatal("assigned a running server's working directory")
	}
	if err = svc.StopServer(ctx, first.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = svc.StartServer(ctx, second.ID); err != nil {
		t.Fatalf("could not start after first stopped: %v", err)
	}
}

func TestConcurrentStartsSerializeSharedWorkingDirectory(t *testing.T) {
	ctx := context.Background()
	svc, _ := testService(t)
	config := serverFiles(t)
	writeEULA(t, config.WorkingDir, "eula=true")
	first, err := svc.SaveServer(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	config.Name = "Другой сервер"
	second, err := svc.SaveServer(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	gate := make(chan struct{})
	results := make(chan error, 2)
	for _, id := range []string{first.ID, second.ID} {
		go func(id string) { <-gate; _, err := svc.StartServer(ctx, id); results <- err }(id)
	}
	close(gate)
	successes := 0
	for range 2 {
		if err := <-results; err == nil {
			successes++
		}
	}
	if successes != 1 {
		t.Fatalf("simultaneous starts succeeded %d times; want exactly one", successes)
	}
}

func TestValidationAndIndependentDirectorySelection(t *testing.T) {
	ctx := context.Background()
	svc, _ := testService(t)
	valid := serverFiles(t)
	tests := []struct {
		name   string
		change func(*model.Server)
	}{
		{"relative files", func(s *model.Server) { s.FilesDir = "relative" }},
		{"missing working", func(s *model.Server) { s.WorkingDir = filepath.Join(s.WorkingDir, "нет папки") }},
		{"non-jar", func(s *model.Server) { s.JarPath = s.JavaPath }},
		{"missing jar", func(s *model.Server) { s.JarPath = "missing.jar" }},
		{"relative java", func(s *model.Server) { s.JavaPath = "java.exe" }},
		{"low memory", func(s *model.Server) { s.MinMemoryMB = 128 }},
		{"memory order", func(s *model.Server) { s.MaxMemoryMB = 256 }},
		{"huge memory", func(s *model.Server) { s.MaxMemoryMB = 1048577 }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			candidate := valid
			test.change(&candidate)
			if _, err := svc.SaveServer(ctx, candidate); err == nil {
				t.Fatal("accepted invalid server")
			}
		})
	}
	valid.JarPath = filepath.Join(valid.FilesDir, valid.JarPath)
	valid.WorkingDir = ""
	saved, err := svc.SaveServer(ctx, valid)
	if err != nil || saved.WorkingDir != valid.FilesDir {
		t.Fatalf("default working dir = %+v, %v", saved, err)
	}
	settings, err := svc.GetSettings(ctx)
	if err != nil || settings.LibraryDir != "" {
		t.Fatalf("default library = %+v, %v", settings, err)
	}
	if _, err = svc.SaveSettings(ctx, model.Settings{LibraryDir: "E:"}); err == nil {
		t.Fatal("accepted drive-relative library")
	}
	if _, err = svc.SaveSettings(ctx, model.Settings{LibraryDir: filepath.Join(valid.FilesDir, "missing")}); err == nil {
		t.Fatal("created missing library")
	}
	if _, err = svc.SaveSettings(ctx, model.Settings{LibraryDir: valid.FilesDir}); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(valid.FilesDir)
	if err != nil || len(entries) != 1 {
		t.Fatalf("settings created unexpected files: %+v, %v", entries, err)
	}
}

func argsServerFiles(t *testing.T) model.Server {
	t.Helper()
	server := serverFiles(t)
	server.WorkingDir = server.FilesDir
	server.JarPath = ""
	server.JavaArgsFile = filepath.Join("libraries", "NeoForge с пробелами", "win_args.txt")
	path := filepath.Join(server.FilesDir, server.JavaArgsFile)
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("-cp libraries/example.jar\nexample.Main\n"), 0600); err != nil {
		t.Fatal(err)
	}
	return server
}

func TestJavaArgsFileLaunchAndEULAGuards(t *testing.T) {
	for _, absolute := range []bool{false, true} {
		name := "relative"
		if absolute {
			name = "absolute"
		}
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			svc, pm := testService(t)
			config := argsServerFiles(t)
			path := filepath.Join(config.FilesDir, config.JavaArgsFile)
			if absolute {
				config.JavaArgsFile = path
			}
			server, err := svc.SaveServer(ctx, config)
			if err != nil {
				t.Fatal(err)
			}
			if server.JarPath != "" || server.JavaArgsFile != config.JavaArgsFile {
				t.Fatalf("optional launch paths changed: %+v", server)
			}
			if _, err = svc.StartServer(ctx, server.ID); err == nil || !strings.Contains(err.Error(), "eula") {
				t.Fatalf("argfile launch bypassed EULA: %v", err)
			}
			writeEULA(t, server.WorkingDir, "eula=false")
			if _, err = svc.StartServer(ctx, server.ID); err == nil {
				t.Fatal("argfile launch accepted false EULA")
			}
			writeEULA(t, server.WorkingDir, "eula=true")
			if _, err = svc.StartServer(ctx, server.ID); err != nil {
				t.Fatal(err)
			}
			spec := pm.specs[server.ID]
			want := []string{"-Xms512M", "-Xmx4096M", "@" + path, "nogui"}
			if spec.Executable != server.JavaPath || spec.WorkingDir != server.FilesDir || spec.StopCommand != "stop" || !reflect.DeepEqual(spec.Args, want) {
				t.Fatalf("argfile launch = %+v; want args %#v", spec, want)
			}
			if strings.Contains(spec.Args[2], `"`) {
				t.Fatal("argument-file path contains extra quote characters")
			}
			views, err := svc.ListServers(ctx)
			if err != nil || len(views) != 1 || !views[0].Available || views[0].Config.JavaArgsFile != config.JavaArgsFile {
				t.Fatalf("argfile availability = %+v, %v", views, err)
			}
		})
	}
}

func TestJavaArgsFileValidationAndAvailability(t *testing.T) {
	ctx := context.Background()
	svc, _ := testService(t)
	config := argsServerFiles(t)
	wrongDirectory := filepath.Join(t.TempDir(), "other-working-directory")
	if err := os.Mkdir(wrongDirectory, 0700); err != nil {
		t.Fatal(err)
	}
	directoryFile := filepath.Join(config.FilesDir, "directory.txt")
	if err := os.Mkdir(directoryFile, 0700); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name   string
		change func(*model.Server)
		want   string
	}{
		{"different working directory", func(s *model.Server) { s.WorkingDir = wrongDirectory }, "должна совпадать"},
		{"missing argument file", func(s *model.Server) { s.JavaArgsFile = "missing.txt" }, "файл аргументов Java"},
		{"not txt", func(s *model.Server) { s.JavaArgsFile = "run.bat" }, ".txt"},
		{"not regular", func(s *model.Server) { s.JavaArgsFile = directoryFile }, "обычный файл"},
		{"missing Java", func(s *model.Server) { s.JavaPath = filepath.Join(s.FilesDir, "missing", "java.exe") }, "Java"},
	} {
		t.Run(test.name, func(t *testing.T) {
			candidate := config
			test.change(&candidate)
			if _, err := svc.SaveServer(ctx, candidate); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("invalid argfile config error = %v; want %s", err, test.want)
			}
		})
	}
	server, err := svc.SaveServer(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	path := javaArgsPath(server)
	if err = os.Remove(path); err != nil {
		t.Fatal(err)
	}
	views, err := svc.ListServers(ctx)
	if err != nil || len(views) != 1 || views[0].Available || views[0].Config.JavaArgsFile != server.JavaArgsFile || !strings.Contains(views[0].AvailabilityError, "файл аргументов Java") {
		t.Fatalf("missing argfile availability = %+v, %v", views, err)
	}
	if _, err = svc.StartServer(ctx, server.ID); err == nil || !strings.Contains(err.Error(), "файл аргументов Java") {
		t.Fatalf("launched with missing argfile: %v", err)
	}
	if err = os.WriteFile(path, []byte("example.Main"), 0600); err != nil {
		t.Fatal(err)
	}
	views, err = svc.ListServers(ctx)
	if err != nil || !views[0].Available {
		t.Fatalf("restored argfile availability = %+v, %v", views, err)
	}
	server.WorkingDir = wrongDirectory
	if _, err = svc.repo.SaveServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	if _, err = svc.StartServer(ctx, server.ID); err == nil || !strings.Contains(err.Error(), "должна совпадать") {
		t.Fatalf("launched persisted mismatched directory: %v", err)
	}
}
