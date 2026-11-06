//go:build windows

package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"gamedock/internal/mcstatus"
	"gamedock/internal/runner"
	"gamedock/internal/vanilla"
	"gamedock/internal/world"

	"github.com/wailsapp/wails/v2/pkg/runtime"
)

const eulaURL = "https://www.minecraft.net/en-us/eula"
const serverPort = 25565

type App struct {
	ui      context.Context
	store   *world.Store
	runner  runner.Runner
	mutator sync.Mutex
	view    atomic.Pointer[storageView]
	closing atomic.Bool
	canQuit atomic.Bool
	probe   atomic.Pointer[launchProbe]
}

type launchProbe struct {
	pid         int
	confirmed   atomic.Bool
	unreachable atomic.Bool
}

type storageView struct {
	ready   bool
	backups []string
	notice  string
}

type Status struct {
	Ready   bool     `json:"ready"`
	Busy    bool     `json:"busy"`
	State   string   `json:"state"`
	Lines   []string `json:"lines"`
	Backups []string `json:"backups"`
	Version string   `json:"version"`
	Notice  string   `json:"notice"`
}

func newApp(root string) (*App, error) {
	store, err := world.Open(root)
	if err != nil {
		return nil, err
	}
	app := &App{store: store}
	app.refresh()
	return app, nil
}

func (a *App) PickFolder() (string, error) {
	return runtime.OpenDirectoryDialog(a.ui, runtime.OpenDialogOptions{Title: "Папка остановленного сервера Vanilla " + vanilla.Version})
}

func (a *App) PickJava() (string, error) {
	return runtime.OpenFileDialog(a.ui, runtime.OpenDialogOptions{
		Title:   "Файл java.exe из установленной Java 21 x64",
		Filters: []runtime.FileFilter{{DisplayName: "Java", Pattern: "java.exe"}},
	})
}

func (a *App) OpenEULA() {
	runtime.BrowserOpenURL(a.ui, eulaURL)
}

func (a *App) Setup(eulaAccepted bool) error {
	return a.mutate(true, func() error {
		if !eulaAccepted {
			return errors.New("подтвердите Minecraft EULA")
		}
		return a.prepare()
	})
}

func (a *App) Import(source string, eulaAccepted bool) error {
	return a.mutate(true, func() error {
		if !eulaAccepted {
			return errors.New("подтвердите Minecraft EULA")
		}
		if _, err := os.Lstat(filepath.Join(a.store.ActiveDir(), "world")); err == nil {
			return errors.New("в этой установке уже есть мир. Для импорта выберите отдельную папку данных GameDock")
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if err := a.store.Import(source); err != nil {
			return fmt.Errorf("импорт мира: %w", err)
		}
		if err := a.prepare(); err != nil {
			return fmt.Errorf("мир скопирован, подготовка сервера прервана. Повторите подготовку: %w", err)
		}
		return nil
	})
}

func (a *App) prepare() error {
	dir := a.store.ActiveDir()
	if err := vanilla.EnsureJar(context.Background(), filepath.Join(dir, "server.jar"), os.Getenv("GAMEDOCK_SERVER_JAR")); err != nil {
		return err
	}
	if err := vanilla.WriteLocalProperties(dir); err != nil {
		return err
	}
	return vanilla.AcceptEULA(dir)
}

func (a *App) Start(javaPath string, memoryMiB int) error {
	return a.mutate(true, func() error {
		dir := a.store.ActiveDir()
		if !vanilla.EULAAccepted(dir) {
			return errors.New("сначала подготовьте сервер и примите Minecraft EULA")
		}
		if err := vanilla.VerifyJar(filepath.Join(dir, "server.jar")); err != nil {
			return fmt.Errorf("повторите подготовку сервера: %w", err)
		}
		if err := validateLaunchWorld(filepath.Join(dir, "world")); err != nil {
			return fmt.Errorf("запуск мира: %w", err)
		}
		if memoryMiB < 1024 || memoryMiB > 8192 {
			return errors.New("укажите память от 1024 до 8192 МиБ")
		}
		if err := verifyJava(javaPath); err != nil {
			return err
		}
		if err := vanilla.WriteLocalProperties(dir); err != nil {
			return err
		}
		if err := checkLocalPort(serverPort); err != nil {
			return err
		}
		if err := a.runner.Start(context.Background(), dir, javaPath, memoryMiB, serverPort); err != nil {
			return err
		}
		ctx, pid := a.runner.LaunchProbeContext()
		probe := &launchProbe{pid: pid}
		a.probe.Store(probe)
		if ctx != nil {
			go a.confirmLaunch(ctx, probe)
		}
		return nil
	})
}

func (a *App) confirmLaunch(ctx context.Context, probe *launchProbe) {
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	var deadline time.Time
	for ctx.Err() == nil && a.probe.Load() == probe {
		run := a.runner.Status()
		if run.State != "running" || run.PID != probe.pid {
			return
		}
		if run.Ready {
			if deadline.IsZero() {
				deadline = time.Now().Add(15 * time.Second)
			}
			if err := mcstatus.Check(ctx, serverPort); err == nil && ctx.Err() == nil {
				probe.confirmed.Store(true)
				return
			}
			if time.Now().After(deadline) {
				probe.unreachable.Store(true)
				return
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (a *App) Stop() error {
	return a.mutate(false, a.stop)
}

func (a *App) stop() error {
	ctx, cancel := context.WithTimeout(context.Background(), 22*time.Second)
	defer cancel()
	err := a.runner.Stop(ctx)
	if errors.Is(err, runner.ErrStopTimeout) || errors.Is(err, context.DeadlineExceeded) {
		return errors.New("сервер ещё сохраняет данные или не отвечает. Подождите либо подтвердите принудительное завершение в окне GameDock")
	}
	return err
}

func (a *App) ForceStop() error {
	return a.mutate(false, func() error {
		switch a.runner.Status().State {
		case "stopped":
			return nil
		case "stopping":
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			return a.runner.ForceStop(ctx)
		default:
			return errors.New("сначала запросите штатную остановку сервера")
		}
	})
}

func (a *App) Backup() (string, error) {
	var id string
	err := a.mutate(true, func() error {
		var err error
		id, err = a.store.Backup()
		return err
	})
	return id, err
}

func (a *App) Restore(id string) error {
	return a.mutate(true, func() error { return a.store.Restore(id) })
}

func (a *App) Status() Status {
	busy := !a.mutator.TryLock()
	if !busy {
		a.mutator.Unlock()
	}
	view := a.view.Load()
	run := a.runner.Status()
	state := run.State
	probe := a.probe.Load()
	if state == "running" && (probe == nil || probe.pid != run.PID || !probe.confirmed.Load()) {
		state = "starting"
		if probe != nil && probe.pid == run.PID && probe.unreachable.Load() {
			state = "unreachable"
		}
	}
	if state == "stopped" && run.LastError != "" {
		state = "failed"
	}
	notice := view.notice
	if run.LastError != "" {
		notice = run.LastError
	} else if state == "unreachable" {
		notice = "Vanilla завершил запуск, но не ответил на проверку 127.0.0.1:25565. Проверьте журнал и перезапустите сервер."
	} else if state == "starting" && run.Ready && notice == "" {
		notice = "Сервер запустился. Ожидаем ответ игрового протокола на 127.0.0.1:25565."
	}
	return Status{
		Ready: view.ready, Busy: busy || a.closing.Load(), State: state, Lines: run.Lines,
		Backups: view.backups, Version: vanilla.Version, Notice: notice,
	}
}

func (a *App) mutate(stopped bool, fn func() error) error {
	if !a.mutator.TryLock() {
		return errors.New("другая операция GameDock ещё выполняется")
	}
	defer a.mutator.Unlock()
	if a.closing.Load() {
		return errors.New("GameDock завершает работу")
	}
	defer a.refresh()
	if stopped {
		if a.runner.Status().State != "stopped" {
			return errors.New("сначала остановите сервер")
		}
		if err := a.store.Recover(); err != nil {
			return fmt.Errorf("хранилище требует восстановления: %w", err)
		}
	}
	return fn()
}

func (a *App) refresh() {
	dir := a.store.ActiveDir()
	jarErr := vanilla.VerifyJar(filepath.Join(dir, "server.jar"))
	view := &storageView{ready: jarErr == nil && vanilla.EULAAccepted(dir)}
	if jarErr != nil && !errors.Is(jarErr, os.ErrNotExist) {
		view.notice = "Установленный server.jar не прошёл проверку. Повторите подготовку сервера."
	}
	var err error
	view.backups, err = a.store.ListBackups()
	if err != nil {
		view.notice = err.Error()
	}
	a.view.Store(view)
}

func (a *App) beforeClose(ctx context.Context) bool {
	if a.canQuit.Load() {
		return false
	}
	if a.closing.CompareAndSwap(false, true) {
		go func() {
			a.mutator.Lock()
			err := a.stop()
			a.mutator.Unlock()
			if err != nil {
				_, _ = runtime.MessageDialog(ctx, runtime.MessageDialogOptions{
					Type: runtime.WarningDialog, Title: "GameDock", Message: err.Error(),
				})
				a.closing.Store(false)
				return
			}
			a.canQuit.Store(true)
			runtime.Quit(ctx)
		}()
	}
	return true
}

var javaVersion = regexp.MustCompile(`(?m)^(?:openjdk|java) version "21(?:[.+-][^"]*)?"`)

func verifyJava(path string) error {
	if !filepath.IsAbs(path) || strings.HasPrefix(path, `\\`) || !strings.EqualFold(filepath.Base(path), "java.exe") {
		return errors.New("выберите локальный файл java.exe из доверенной Java 21 x64")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, path, "-version")
	command.Dir = filepath.Dir(path)
	command.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	command.Env = runner.JavaEnvironment(command.Environ())
	output, err := command.CombinedOutput()
	if err != nil || !javaVersion.Match(output) || !strings.Contains(string(output), "64-Bit") {
		return errors.New("выбранный java.exe не подтвердил Java 21 x64")
	}
	return nil
}
