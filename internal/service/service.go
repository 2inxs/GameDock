package service

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"

	"gamedock/internal/model"
	"gamedock/internal/storage"
)

type ProcessManager interface {
	Start(string, model.LaunchSpec) (model.RuntimeSnapshot, error)
	Stop(context.Context, string) error
	Kill(string) error
	SendCommand(string, string) error
	Snapshot(string) model.RuntimeSnapshot
}

type Service struct {
	mu   sync.Mutex
	repo *storage.Repository
	pm   ProcessManager
}

func New(repo *storage.Repository, pm ProcessManager) *Service { return &Service{repo: repo, pm: pm} }

func (s *Service) ListServers(ctx context.Context) ([]model.ServerView, error) {
	servers, err := s.repo.ListServers(ctx)
	if err != nil {
		return nil, fmt.Errorf("не удалось загрузить серверы: %w", err)
	}
	views := make([]model.ServerView, 0, len(servers))
	for _, server := range servers {
		view := model.ServerView{Config: server, Runtime: s.pm.Snapshot(server.ID), Available: true}
		if err := validatePaths(server); err != nil {
			view.Available = false
			view.AvailabilityError = err.Error()
		}
		views = append(views, view)
	}
	return views, nil
}

func (s *Service) SaveServer(ctx context.Context, server model.Server) (model.Server, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if server.ID != "" {
		if _, err := s.repo.GetServer(ctx, server.ID); err != nil {
			return server, err
		}
		if active(s.pm.Snapshot(server.ID).State) {
			return server, errors.New("остановите сервер перед изменением настроек")
		}
	}
	server.Name = strings.TrimSpace(server.Name)
	if server.Name == "" {
		return server, errors.New("укажите название сервера")
	}
	if len([]rune(server.Name)) > 120 {
		return server, errors.New("название сервера должно быть не длиннее 120 символов")
	}
	server.Game = strings.ToLower(strings.TrimSpace(server.Game))
	if server.Game == "" {
		server.Game = "minecraft"
	}
	if server.Game != "minecraft" {
		return server, errors.New("поддерживаются только серверы Minecraft Java")
	}
	server.FilesDir = strings.TrimSpace(server.FilesDir)
	server.WorkingDir = strings.TrimSpace(server.WorkingDir)
	server.JavaPath = strings.TrimSpace(server.JavaPath)
	server.JarPath = strings.TrimSpace(server.JarPath)
	server.JavaArgsFile = strings.TrimSpace(server.JavaArgsFile)
	if server.WorkingDir == "" {
		server.WorkingDir = server.FilesDir
	}
	if server.MinMemoryMB < 256 || server.MaxMemoryMB < server.MinMemoryMB || server.MaxMemoryMB > 1048576 {
		return server, errors.New("память: минимум от 256 МБ; максимум не меньше минимума и не больше 1048576 МБ")
	}
	if err := validatePaths(server); err != nil {
		return server, err
	}
	server.FilesDir = filepath.Clean(server.FilesDir)
	server.WorkingDir = filepath.Clean(server.WorkingDir)
	server.JavaPath = filepath.Clean(server.JavaPath)
	if server.JarPath != "" {
		server.JarPath = filepath.Clean(server.JarPath)
	}
	if server.JavaArgsFile != "" {
		server.JavaArgsFile = filepath.Clean(server.JavaArgsFile)
	}
	if err := s.checkWorkingDir(ctx, server); err != nil {
		return server, err
	}
	saved, err := s.repo.SaveServer(ctx, server)
	if err != nil {
		return server, fmt.Errorf("не удалось сохранить сервер: %w", err)
	}
	return saved, nil
}

func (s *Service) DeleteServer(ctx context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if active(s.pm.Snapshot(id).State) {
		return errors.New("остановите сервер перед удалением из списка")
	}
	if err := s.repo.DeleteServer(ctx, id); err != nil {
		return fmt.Errorf("не удалось удалить сервер из списка: %w", err)
	}
	return nil
}

func (s *Service) StartServer(ctx context.Context, id string) (model.RuntimeSnapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	server, err := s.repo.GetServer(ctx, id)
	if err != nil {
		return model.RuntimeSnapshot{}, err
	}
	if active(s.pm.Snapshot(id).State) {
		return model.RuntimeSnapshot{}, errors.New("сервер уже запущен или останавливается")
	}
	if err = validatePaths(server); err != nil {
		return model.RuntimeSnapshot{}, err
	}
	if err = s.checkWorkingDir(ctx, server); err != nil {
		return model.RuntimeSnapshot{}, err
	}
	if err = writable(server.WorkingDir); err != nil {
		return model.RuntimeSnapshot{}, err
	}
	if err = checkEULA(server.WorkingDir); err != nil {
		return model.RuntimeSnapshot{}, err
	}
	if err = ctx.Err(); err != nil {
		return model.RuntimeSnapshot{}, err
	}
	args := []string{"-Xms" + strconv.Itoa(server.MinMemoryMB) + "M", "-Xmx" + strconv.Itoa(server.MaxMemoryMB) + "M"}
	if server.JavaArgsFile != "" {
		// Keep @path a single raw argv value. exec.Cmd handles platform quoting;
		// extra quotes here would become part of the Java argument-file name.
		args = append(args, "@"+javaArgsPath(server), "nogui")
	} else {
		args = append(args, "-jar", jarPath(server), "nogui")
	}
	snapshot, err := s.pm.Start(id, model.LaunchSpec{
		Executable: server.JavaPath,
		Args:       args,
		WorkingDir: server.WorkingDir, StopCommand: "stop",
	})
	if err != nil {
		return snapshot, fmt.Errorf("не удалось запустить сервер: %w", err)
	}
	return snapshot, nil
}

func (s *Service) checkWorkingDir(ctx context.Context, server model.Server) error {
	servers, err := s.repo.ListServers(ctx)
	if err != nil {
		return fmt.Errorf("не удалось проверить рабочую папку: %w", err)
	}
	for _, other := range servers {
		if other.ID != server.ID && active(s.pm.Snapshot(other.ID).State) && sameDirectory(other.WorkingDir, server.WorkingDir) {
			return fmt.Errorf("рабочая папка уже используется запущенным сервером «%s»", other.Name)
		}
	}
	return nil
}

func (s *Service) StopServer(ctx context.Context, id string) error {
	// Do not hold the mutation lock while waiting for process exit.
	if _, err := s.repo.GetServer(ctx, id); err != nil {
		return err
	}
	if err := s.pm.Stop(ctx, id); err != nil {
		return fmt.Errorf("не удалось остановить сервер: %w", err)
	}
	return nil
}

func (s *Service) KillServer(ctx context.Context, id string) error {
	if _, err := s.repo.GetServer(ctx, id); err != nil {
		return err
	}
	if err := s.pm.Kill(id); err != nil {
		return fmt.Errorf("не удалось принудительно завершить сервер: %w", err)
	}
	return nil
}

func (s *Service) SendCommand(ctx context.Context, id, command string) error {
	if _, err := s.repo.GetServer(ctx, id); err != nil {
		return err
	}
	if err := s.pm.SendCommand(id, command); err != nil {
		return fmt.Errorf("не удалось отправить команду: %w", err)
	}
	return nil
}

func (s *Service) GetSettings(ctx context.Context) (model.Settings, error) {
	settings, err := s.repo.GetSettings(ctx)
	if err != nil {
		return settings, fmt.Errorf("не удалось загрузить настройки: %w", err)
	}
	return settings, nil
}

func (s *Service) SaveSettings(ctx context.Context, settings model.Settings) (model.Settings, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	settings.LibraryDir = strings.TrimSpace(settings.LibraryDir)
	if settings.LibraryDir != "" {
		if err := directory(settings.LibraryDir, "папка библиотеки"); err != nil {
			return settings, err
		}
		if err := writable(settings.LibraryDir); err != nil {
			return settings, err
		}
		settings.LibraryDir = filepath.Clean(settings.LibraryDir)
	}
	saved, err := s.repo.SaveSettings(ctx, settings)
	if err != nil {
		return settings, fmt.Errorf("не удалось сохранить настройки: %w", err)
	}
	return saved, nil
}

func active(state model.ProcessState) bool {
	return state == model.Starting || state == model.Running || state == model.Stopping
}

func directory(path, label string) error {
	if !filepath.IsAbs(path) {
		return fmt.Errorf("%s: выберите абсолютный путь к существующей папке", label)
	}
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("%s недоступна: %s (%v)", label, path, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("%s: выбранный путь не является папкой: %s", label, path)
	}
	return nil
}

func jarPath(server model.Server) string {
	if filepath.IsAbs(server.JarPath) {
		return filepath.Clean(server.JarPath)
	}
	return filepath.Join(server.FilesDir, server.JarPath)
}

func javaArgsPath(server model.Server) string {
	if filepath.IsAbs(server.JavaArgsFile) {
		return filepath.Clean(server.JavaArgsFile)
	}
	return filepath.Join(server.FilesDir, server.JavaArgsFile)
}

func validatePaths(server model.Server) error {
	if err := directory(server.FilesDir, "папка файлов сервера"); err != nil {
		return err
	}
	if err := directory(server.WorkingDir, "рабочая папка"); err != nil {
		return err
	}
	if server.JavaArgsFile != "" {
		if !sameDirectory(server.FilesDir, server.WorkingDir) {
			return errors.New("для запуска NeoForge через файл аргументов рабочая папка должна совпадать с папкой файлов сервера: пути libraries в win_args.txt относительные")
		}
		if !strings.EqualFold(filepath.Ext(server.JavaArgsFile), ".txt") {
			return errors.New("выберите существующий файл аргументов Java с расширением .txt (например, win_args.txt)")
		}
		if !filepath.IsAbs(server.JavaArgsFile) && filepath.VolumeName(server.JavaArgsFile) != "" {
			return errors.New("файл аргументов Java: укажите абсолютный путь или путь относительно папки файлов сервера")
		}
		if err := regularFile(javaArgsPath(server), "файл аргументов Java"); err != nil {
			return err
		}
	} else {
		if server.JarPath == "" || !strings.EqualFold(filepath.Ext(server.JarPath), ".jar") {
			return errors.New("выберите существующий файл сервера с расширением .jar")
		}
		if err := regularFile(jarPath(server), "JAR-файл сервера"); err != nil {
			return err
		}
	}
	if !filepath.IsAbs(server.JavaPath) {
		return errors.New("выберите абсолютный путь к исполняемому файлу Java")
	}
	if err := regularFile(server.JavaPath, "исполняемый файл Java"); err != nil {
		return err
	}
	if runtime.GOOS == "windows" {
		if !strings.EqualFold(filepath.Base(server.JavaPath), "java.exe") {
			return errors.New("выберите исполняемый файл java.exe")
		}
	} else {
		info, err := os.Stat(server.JavaPath)
		if err != nil {
			return err
		}
		if info.Mode().Perm()&0111 == 0 {
			return errors.New("выбранный файл Java не имеет права на выполнение")
		}
	}
	return nil
}

func regularFile(path, label string) error {
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("%s недоступен: %s (%v)", label, path, err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("%s: выберите обычный файл: %s", label, path)
	}
	return nil
}

func writable(dir string) error {
	f, err := os.CreateTemp(dir, ".gamedock-write-check-*")
	if err != nil {
		return fmt.Errorf("нет доступа на запись в папку %s: %w", dir, err)
	}
	path := f.Name()
	closeErr := f.Close()
	removeErr := os.Remove(path)
	if closeErr != nil {
		return fmt.Errorf("не удалось проверить запись в папку: %w", closeErr)
	}
	if removeErr != nil {
		return fmt.Errorf("не удалось удалить проверочный файл %s: %w", path, removeErr)
	}
	return nil
}

func checkEULA(dir string) error {
	f, err := os.Open(filepath.Join(dir, "eula.txt"))
	if err != nil {
		return errors.New("для запуска создайте eula.txt в рабочей папке и самостоятельно примите Minecraft EULA, указав eula=true")
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	accepted := false
	for scanner.Scan() {
		line := strings.TrimSpace(strings.TrimPrefix(scanner.Text(), "\uFEFF"))
		if strings.HasPrefix(line, "#") || strings.HasPrefix(line, "!") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if ok && strings.TrimSpace(key) == "eula" {
			accepted = strings.TrimSpace(value) == "true"
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("не удалось прочитать eula.txt: %w", err)
	}
	if !accepted {
		return errors.New("Minecraft EULA не принята: самостоятельно ознакомьтесь с условиями и установите eula=true в eula.txt рабочей папки")
	}
	return nil
}

func sameDirectory(a, b string) bool {
	aInfo, aErr := os.Stat(a)
	bInfo, bErr := os.Stat(b)
	if aErr == nil && bErr == nil && os.SameFile(aInfo, bInfo) {
		return true
	}
	if resolved, err := filepath.EvalSymlinks(a); err == nil {
		a = resolved
	}
	if resolved, err := filepath.EvalSymlinks(b); err == nil {
		b = resolved
	}
	a, b = filepath.Clean(a), filepath.Clean(b)
	if runtime.GOOS == "windows" {
		return strings.EqualFold(a, b)
	}
	return a == b
}
