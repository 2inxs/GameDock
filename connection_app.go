package main

import (
	"context"
	"errors"
	"fmt"
	"time"

	"gamedock/internal/engine"
	"gamedock/internal/network"
	"gamedock/internal/rooms"
	"github.com/wailsapp/wails/v2/pkg/runtime"
)

// RoomSummary deliberately omits network credentials and local executable paths.
type RoomSummary struct {
	ID             string    `json:"id"`
	Name           string    `json:"name"`
	Role           string    `json:"role"`
	ServerID       string    `json:"serverId"`
	GamePort       int       `json:"gamePort"`
	LocalPort      int       `json:"localPort"`
	HostVirtualIP  string    `json:"hostVirtualIp"`
	BootstrapPeers []string  `json:"bootstrapPeers"`
	AllowRelay     bool      `json:"allowRelay"`
	CreatedAt      time.Time `json:"createdAt"`
}

type RoomView struct {
	Config RoomSummary        `json:"config"`
	Status network.RoomStatus `json:"status"`
}

type CreateRoomRequest struct {
	Name           string   `json:"name"`
	ServerID       string   `json:"serverId"`
	GamePort       int      `json:"gamePort"`
	AllowRelay     bool     `json:"allowRelay"`
	BootstrapPeers []string `json:"bootstrapPeers"`
}

func defaultBootstrapPeers() []string {
	return []string{"tcp://public.easytier.cn:11010", "tcp://public.easytier.top:11010"}
}

func (a *App) viewRoom(c rooms.Config) RoomView {
	status := network.RoomStatus{RoomID: c.ID, State: "disconnected", AllowRelay: c.AllowRelay}
	if a.connections != nil {
		if current := a.connections.Status(); current.RoomID == c.ID {
			status = current
		}
	}
	return RoomView{Config: RoomSummary{ID: c.ID, Name: c.Name, Role: c.Role, ServerID: c.ServerID, GamePort: c.GamePort, LocalPort: c.LocalPort, HostVirtualIP: c.HostVirtualIP, BootstrapPeers: append([]string{}, c.BootstrapPeers...), AllowRelay: c.AllowRelay, CreatedAt: c.CreatedAt}, Status: status}
}

func (a *App) findRoom(id string) (rooms.Config, error) {
	list, err := a.roomStore.List()
	if err != nil {
		return rooms.Config{}, err
	}
	for _, c := range list {
		if c.ID == id {
			return c, nil
		}
	}
	return rooms.Config{}, errors.New("Комната не найдена")
}

func (a *App) ListRooms() ([]RoomView, error) {
	if err := a.checkReady(); err != nil {
		return nil, err
	}
	list, err := a.roomStore.List()
	if err != nil {
		return nil, err
	}
	views := make([]RoomView, 0, len(list))
	for _, c := range list {
		if editionName == "client" && c.Role != "guest" {
			continue
		}
		views = append(views, a.viewRoom(c))
	}
	return views, nil
}

func (a *App) JoinRoom(invitation string, localPort int, allowRelay bool) (RoomView, error) {
	if err := a.checkReady(); err != nil {
		return RoomView{}, err
	}
	c, err := rooms.ParseInvitation(invitation)
	if err != nil {
		return RoomView{}, err
	}
	c.LocalPort = localPort
	c.AllowRelay = allowRelay
	a.roomMu.Lock()
	defer a.roomMu.Unlock()
	if err = a.checkReady(); err != nil {
		return RoomView{}, err
	}
	list, err := a.roomStore.List()
	if err != nil {
		return RoomView{}, err
	}
	for _, existing := range list {
		if existing.Role == "guest" && existing.NetworkName == c.NetworkName {
			return RoomView{}, errors.New("Эта комната уже добавлена")
		}
	}
	c, err = a.roomStore.Save(c)
	if err != nil {
		return RoomView{}, err
	}
	return a.viewRoom(c), nil
}

func (a *App) DeleteRoom(id string) error {
	if err := a.checkReady(); err != nil {
		return err
	}
	a.roomMu.Lock()
	defer a.roomMu.Unlock()
	if a.connections.Status().RoomID == id && a.connectionActive() {
		return errors.New("Сначала отключите комнату")
	}
	return a.roomStore.Delete(id)
}

func (a *App) SaveRoomSettings(id string, localPort int, allowRelay bool, bootstrapPeers []string) error {
	if err := a.checkReady(); err != nil {
		return err
	}
	a.roomMu.Lock()
	defer a.roomMu.Unlock()
	if a.connections.Status().RoomID == id && a.connectionActive() {
		return errors.New("Отключите комнату перед изменением подключения")
	}
	c, err := a.findRoom(id)
	if err != nil {
		return err
	}
	if editionName == "client" && c.Role != "guest" {
		return errors.New("Эта редакция поддерживает только гостевые подключения")
	}
	c.LocalPort = localPort
	c.AllowRelay = allowRelay
	c.BootstrapPeers = append([]string{}, bootstrapPeers...)
	_, err = a.roomStore.Save(c)
	return err
}

func (a *App) ConnectRoom(id string) error {
	if err := a.checkReady(); err != nil {
		return err
	}
	a.roomMu.Lock()
	defer a.roomMu.Unlock()
	if err := a.checkReady(); err != nil {
		return err
	}
	info := a.engine.Info()
	if !info.Installed {
		return errors.New("Сначала установите сетевой движок EasyTier: " + info.Error)
	}
	c, err := a.findRoom(id)
	if err != nil {
		return err
	}
	if err = a.serverReadyForRoom(c); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(a.ctx, 20*time.Second)
	defer cancel()
	return a.connections.Connect(ctx, c)
}

func (a *App) DisconnectRoom() error {
	if err := a.checkReady(); err != nil {
		return err
	}
	a.roomMu.Lock()
	defer a.roomMu.Unlock()
	ctx, cancel := context.WithTimeout(a.ctx, 5*time.Second)
	defer cancel()
	return a.connections.Disconnect(ctx)
}

func (a *App) GetRoomStatus() (network.RoomStatus, error) {
	if err := a.checkReady(); err != nil {
		return network.RoomStatus{}, err
	}
	return a.connections.Status(), nil
}

func (a *App) GetRoomInvitation(id string) (string, error) {
	if err := a.checkReady(); err != nil {
		return "", err
	}
	if editionName != "full" {
		return "", errors.New("Приглашения создаёт владелец комнаты в полной версии GameDock")
	}
	c, err := a.findRoom(id)
	if err != nil {
		return "", err
	}
	if c.Role != "host" {
		return "", errors.New("Приглашения создаёт владелец комнаты")
	}
	return rooms.Invitation(c)
}

func (a *App) GetNetworkEngine() engine.Info {
	if a.engine == nil {
		return engine.Info{Error: "GameDock ещё запускается"}
	}
	return a.engine.Info()
}

func (a *App) InstallNetworkEngine() (engine.Info, error) {
	if err := a.checkReady(); err != nil {
		return engine.Info{}, err
	}
	ctx, cancel := context.WithTimeout(a.ctx, 3*time.Minute)
	defer cancel()
	info, err := a.engine.Install(ctx)
	if err != nil {
		return info, fmt.Errorf("Не удалось установить EasyTier: %w", err)
	}
	return info, nil
}

func (a *App) CopyText(text string) error {
	if err := a.checkReady(); err != nil {
		return err
	}
	if len(text) > 32*1024 {
		return errors.New("Текст слишком длинный")
	}
	return runtime.ClipboardSetText(a.ctx, text)
}
