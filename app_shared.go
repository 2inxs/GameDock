package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"gamedock/internal/engine"
	"gamedock/internal/network"
	"gamedock/internal/rooms"
	"github.com/wailsapp/wails/v2/pkg/runtime"
)

const version = "0.2.0"

// App composes the shared connection module with edition-specific modules.
// The client build does not compile or bind the server-management module.
type App struct {
	editionModules
	ctx          context.Context
	dataDir      string
	startupError error
	ready        atomic.Bool
	closing      atomic.Bool
	canClose     atomic.Bool
	roomMu       sync.Mutex
	roomStore    *rooms.Store
	connections  *network.Manager
	engine       *engine.Installer
}

type Capabilities struct {
	Servers bool `json:"servers"`
	Rooms   bool `json:"rooms"`
	Hosting bool `json:"hosting"`
}

type AppInfo struct {
	Version      string       `json:"version"`
	DataDir      string       `json:"dataDir"`
	Error        string       `json:"error"`
	Edition      string       `json:"edition"`
	Capabilities Capabilities `json:"capabilities"`
}

func NewApp() *App { return &App{} }

func (a *App) startup(ctx context.Context) {
	a.ctx = ctx
	a.dataDir = os.Getenv("GAMEDOCK_DATA_DIR")
	if a.dataDir == "" {
		root, err := os.UserConfigDir()
		if err != nil {
			a.startupError = fmt.Errorf("Не удалось найти папку настроек: %w", err)
			return
		}
		a.dataDir = filepath.Join(root, editionDataDirectory)
	}
	var err error
	a.roomStore, err = rooms.Open(filepath.Join(a.dataDir, "rooms.json"))
	if err != nil {
		a.startupError = err
		return
	}
	a.engine = engine.New(filepath.Join(a.dataDir, "network-engine-portable"))
	a.connections = network.New(network.Options{
		CorePath: a.engine.CorePath(), CLIPath: a.engine.CLIPath(), RuntimeDir: filepath.Join(a.dataDir, "network-runtime"),
		Publish: func(status network.RoomStatus) {
			if !a.canClose.Load() {
				runtime.EventsEmit(ctx, "gamedock:network", status)
			}
		},
	})
	if err = a.initializeHost(ctx); err != nil {
		a.startupError = err
		return
	}
	a.ready.Store(true)
}

func (a *App) checkReady() error {
	if a.startupError != nil {
		return a.startupError
	}
	if !a.ready.Load() {
		return errors.New("GameDock ещё запускается")
	}
	if a.closing.Load() {
		return errors.New("GameDock завершает работу")
	}
	return nil
}

func (a *App) GetAppInfo() AppInfo {
	info := AppInfo{Version: version, DataDir: a.dataDir, Edition: editionName, Capabilities: Capabilities{Servers: editionName == "full", Rooms: true, Hosting: editionName == "full"}}
	if a.startupError != nil {
		info.Error = a.startupError.Error()
	}
	return info
}

func (a *App) connectionActive() bool {
	if a.connections == nil {
		return false
	}
	s := a.connections.Status()
	return s.RoomID != "" && s.State != "disconnected" && s.State != "error"
}

func (a *App) closeModules(ctx context.Context) error {
	a.roomMu.Lock()
	defer a.roomMu.Unlock()
	var netErr error
	if a.connections != nil {
		netErr = a.connections.Shutdown(ctx)
	}
	hostErr := a.stopHost(ctx)
	return errors.Join(netErr, hostErr)
}

func (a *App) beforeClose(ctx context.Context) bool {
	if a.canClose.Load() {
		return false
	}
	if a.closing.Load() {
		return true
	}
	if a.hostActive() || a.connectionActive() {
		message := "Соединение комнаты будет отключено. Выйти?"
		if a.hostActive() {
			message = "Игровые серверы будут остановлены, соединение комнаты отключено. Если сервер не ответит за 20 секунд, он будет завершён принудительно. Выйти?"
		}
		answer, err := runtime.MessageDialog(ctx, runtime.MessageDialogOptions{Type: runtime.QuestionDialog, Title: "Закрыть " + editionTitle + "?", DefaultButton: "No", Message: message})
		if err != nil || answer != "Yes" {
			return true
		}
	}
	if !a.closing.CompareAndSwap(false, true) {
		return true
	}
	go func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		if err := a.closeModules(stopCtx); err != nil {
			a.closing.Store(false)
			runtime.EventsEmit(ctx, "gamedock:shutdown-error", "Не удалось завершить все процессы. Повторите закрытие приложения: "+err.Error())
			return
		}
		a.canClose.Store(true)
		runtime.Quit(ctx)
	}()
	return true
}

func (a *App) shutdown(_ context.Context) {
	a.closing.Store(true)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = a.closeModules(ctx)
	a.closeHostStorage()
}
