//go:build !client

package main

import (
	"context"
	"errors"
	"fmt"
	"gamedock/internal/model"
	"gamedock/internal/process"
	"gamedock/internal/rooms"
	"gamedock/internal/service"
	"gamedock/internal/storage"
	"github.com/wailsapp/wails/v2/pkg/runtime"
	"os"
	"path/filepath"
	"time"
)

const editionName = "full"
const editionTitle = "GameDock"
const editionDataDirectory = "GameDock"
const editionLockID = "d4789e90-8d09-41b0-9235-19191c7bad06"

type editionModules struct {
	repo    *storage.Repository
	manager *process.Manager
	service *service.Service
}

func (a *App) initializeHost(ctx context.Context) error {
	var err error
	a.repo, err = storage.Open(filepath.Join(a.dataDir, "gamedock.db"))
	if err != nil {
		return fmt.Errorf("Не удалось открыть настройки GameDock: %w", err)
	}
	a.manager = process.New(func(event model.Event) {
		if !a.canClose.Load() {
			runtime.EventsEmit(ctx, "gamedock:event", event)
		}
	})
	a.service = service.New(a.repo, a.manager)
	return nil
}

func (a *App) hostActive() bool {
	if a.repo == nil || a.manager == nil {
		return false
	}
	servers, err := a.repo.ListServers(context.Background())
	if err != nil {
		return true
	}
	for _, s := range servers {
		state := a.manager.Snapshot(s.ID).State
		if state == model.Starting || state == model.Running || state == model.Stopping {
			return true
		}
	}
	return false
}

func (a *App) stopHost(ctx context.Context) error {
	if a.manager != nil {
		return a.manager.Shutdown(ctx)
	}
	return nil
}

func (a *App) closeHostStorage() {
	if a.repo != nil {
		_ = a.repo.Close()
	}
}

func (a *App) serverReadyForRoom(c rooms.Config) error {
	if c.Role != "host" {
		return nil
	}
	if a.repo == nil || a.manager == nil {
		return errors.New("Модуль серверов недоступен")
	}
	if _, err := a.repo.GetServer(a.ctx, c.ServerID); err != nil {
		return err
	}
	if a.manager.Snapshot(c.ServerID).State != model.Running {
		return errors.New("Сначала запустите выбранный игровой сервер")
	}
	return nil
}

func (a *App) CreateRoom(request CreateRoomRequest) (RoomView, error) {
	if err := a.checkReady(); err != nil {
		return RoomView{}, err
	}
	if _, err := a.repo.GetServer(a.ctx, request.ServerID); err != nil {
		return RoomView{}, err
	}
	a.roomMu.Lock()
	defer a.roomMu.Unlock()
	peers := request.BootstrapPeers
	if len(peers) == 0 {
		peers = defaultBootstrapPeers()
	}
	c, err := a.roomStore.Save(rooms.Config{Name: request.Name, Role: "host", ServerID: request.ServerID, GamePort: request.GamePort, AllowRelay: request.AllowRelay, BootstrapPeers: peers})
	if err != nil {
		return RoomView{}, err
	}
	return a.viewRoom(c), nil
}

func (a *App) ListServers() ([]model.ServerView, error) {
	if err := a.checkReady(); err != nil {
		return nil, err
	}
	return a.service.ListServers(a.ctx)
}

func (a *App) SaveServer(config model.Server) (model.Server, error) {
	if err := a.checkReady(); err != nil {
		return model.Server{}, err
	}
	return a.service.SaveServer(a.ctx, config)
}

func (a *App) DeleteServer(id string) error {
	if err := a.checkReady(); err != nil {
		return err
	}
	return a.service.DeleteServer(a.ctx, id)
}

func (a *App) StartServer(id string) (model.RuntimeSnapshot, error) {
	if err := a.checkReady(); err != nil {
		return model.RuntimeSnapshot{}, err
	}
	return a.service.StartServer(a.ctx, id)
}

func (a *App) StopServer(id string) error {
	if err := a.checkReady(); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(a.ctx, 20*time.Second)
	defer cancel()
	return a.service.StopServer(ctx, id)
}

func (a *App) KillServer(id string) error {
	if err := a.checkReady(); err != nil {
		return err
	}
	return a.service.KillServer(a.ctx, id)
}

func (a *App) SendCommand(id, command string) error {
	if err := a.checkReady(); err != nil {
		return err
	}
	return a.service.SendCommand(a.ctx, id, command)
}

func (a *App) GetRuntime(id string) (model.RuntimeSnapshot, error) {
	if err := a.checkReady(); err != nil {
		return model.RuntimeSnapshot{}, err
	}
	return a.manager.Snapshot(id), nil
}

func (a *App) GetLogs(id string, after uint64) ([]model.LogEntry, error) {
	if err := a.checkReady(); err != nil {
		return nil, err
	}
	entries := a.manager.Logs(id, after)
	if entries == nil {
		entries = []model.LogEntry{}
	}
	return entries, nil
}

func (a *App) GetSettings() (model.Settings, error) {
	if err := a.checkReady(); err != nil {
		return model.Settings{}, err
	}
	return a.service.GetSettings(a.ctx)
}

func (a *App) SaveSettings(settings model.Settings) (model.Settings, error) {
	if err := a.checkReady(); err != nil {
		return model.Settings{}, err
	}
	return a.service.SaveSettings(a.ctx, settings)
}

func existingDirectory(path string) string {
	if path == "" {
		return ""
	}
	info, err := os.Stat(path)
	if err != nil {
		return ""
	}
	if info.IsDir() {
		return path
	}
	return filepath.Dir(path)
}

func (a *App) PickDirectory(title, current string) (string, error) {
	if title == "" {
		title = "Выберите папку сервера"
	}
	return runtime.OpenDirectoryDialog(a.ctx, runtime.OpenDialogOptions{
		Title: title, DefaultDirectory: existingDirectory(current),
	})
}

func (a *App) PickJava(current string) (string, error) {
	return runtime.OpenFileDialog(a.ctx, runtime.OpenDialogOptions{
		Title: "Выберите java.exe", DefaultDirectory: existingDirectory(current),
		Filters: []runtime.FileFilter{{DisplayName: "Java (java.exe)", Pattern: "java.exe"}},
	})
}

func (a *App) PickJar(current string) (string, error) {
	return runtime.OpenFileDialog(a.ctx, runtime.OpenDialogOptions{
		Title: "Выберите серверный JAR Minecraft", DefaultDirectory: existingDirectory(current),
		Filters: []runtime.FileFilter{{DisplayName: "Minecraft server (*.jar)", Pattern: "*.jar"}},
	})
}

func (a *App) PickArgsFile(current string) (string, error) {
	return runtime.OpenFileDialog(a.ctx, runtime.OpenDialogOptions{
		Title: "Выберите файл аргументов NeoForge (win_args.txt)", DefaultDirectory: existingDirectory(current),
		Filters: []runtime.FileFilter{{DisplayName: "Java argument files (*.txt)", Pattern: "*.txt"}},
	})
}
