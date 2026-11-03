package main

import (
	"embed"
	"log"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	"github.com/wailsapp/wails/v2/pkg/options/windows"
	"github.com/wailsapp/wails/v2/pkg/runtime"
)

//go:embed all:frontend/dist
var assets embed.FS

func main() {
	app := NewApp()
	err := wails.Run(&options.App{
		Title: editionTitle, Width: 1380, Height: 880, MinWidth: 1050, MinHeight: 700,
		BackgroundColour: options.NewRGB(12, 17, 24),
		AssetServer:      &assetserver.Options{Assets: assets},
		OnStartup:        app.startup, OnShutdown: app.shutdown, OnBeforeClose: app.beforeClose,
		Bind: []interface{}{app},
		SingleInstanceLock: &options.SingleInstanceLock{
			UniqueId: editionLockID,
			OnSecondInstanceLaunch: func(_ options.SecondInstanceData) {
				if app.ctx != nil {
					runtime.WindowShow(app.ctx)
					runtime.WindowUnminimise(app.ctx)
				}
			},
		},
		Windows: &windows.Options{WebviewIsTransparent: false, WindowIsTranslucent: false},
	})
	if err != nil {
		log.Fatal(err)
	}
}
