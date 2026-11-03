//go:build client

package main

import (
	"context"
	"errors"
	"gamedock/internal/rooms"
)

const editionName = "client"
const editionTitle = "GameDock Connect"
const editionDataDirectory = "GameDockConnect"
const editionLockID = "b06cb2c4-1d79-4f24-918f-0301c3ab6e39"

type editionModules struct{}

func (a *App) initializeHost(context.Context) error { return nil }
func (a *App) hostActive() bool                     { return false }
func (a *App) stopHost(context.Context) error       { return nil }
func (a *App) closeHostStorage()                    {}
func (a *App) serverReadyForRoom(c rooms.Config) error {
	if c.Role == "host" {
		return errors.New("GameDock Connect поддерживает только присоединение к комнатам")
	}
	return nil
}
