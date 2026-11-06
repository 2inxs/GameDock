//go:build windows

package main

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	"golang.org/x/sys/windows"
)

//go:embed frontend/index.html frontend/style.css frontend/app.js
var embedded embed.FS

func main() {
	root, err := dataDir()
	if err != nil {
		showError(err)
		return
	}
	lock, err := lockWorkspace(root)
	if err != nil {
		showError(err)
		return
	}
	defer windows.CloseHandle(lock)
	app, err := newApp(root)
	if err != nil {
		showError(err)
		return
	}
	assets, err := fs.Sub(embedded, "frontend")
	if err != nil {
		showError(err)
		return
	}
	err = wails.Run(&options.App{
		Title:         "GameDock",
		Width:         1060,
		Height:        820,
		MinWidth:      620,
		MinHeight:     480,
		AssetServer:   &assetserver.Options{Assets: assets},
		OnStartup:     func(ctx context.Context) { app.ui = ctx },
		OnBeforeClose: app.beforeClose,
		Bind:          []interface{}{app},
	})
	if err != nil {
		showError(err)
	}
}

func dataDir() (string, error) {
	dir := os.Getenv("GAMEDOCK_DATA_DIR")
	if dir == "" {
		local := os.Getenv("LOCALAPPDATA")
		if local == "" {
			return "", errors.New("Windows не предоставила путь LOCALAPPDATA")
		}
		dir = filepath.Join(local, "GameDock", "Prototype")
	}
	if !filepath.IsAbs(dir) || strings.HasPrefix(dir, `\\`) {
		return "", errors.New("папка GameDock должна быть абсолютным путём на локальном диске")
	}
	return filepath.Clean(dir), nil
}

func lockWorkspace(root string) (windows.Handle, error) {
	if err := os.MkdirAll(root, 0700); err != nil {
		return 0, err
	}
	path, err := windows.UTF16PtrFromString(filepath.Join(root, ".gamedock.lock"))
	if err != nil {
		return 0, err
	}
	handle, err := windows.CreateFile(path, windows.GENERIC_READ|windows.GENERIC_WRITE, 0, nil,
		windows.OPEN_ALWAYS, windows.FILE_ATTRIBUTE_NORMAL|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		return 0, fmt.Errorf("папка GameDock занята или недоступна: %w", err)
	}
	return handle, nil
}

func showError(err error) {
	log.Print(err)
	title, _ := windows.UTF16PtrFromString("GameDock")
	message, messageErr := windows.UTF16PtrFromString(err.Error())
	if messageErr == nil {
		_, _ = windows.MessageBox(0, message, title, windows.MB_ICONERROR)
	}
}
