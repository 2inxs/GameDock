package main

import (
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"

	"gamedock/internal/vanilla"

	"golang.org/x/sys/windows"
)

func checkLocalPort(port int) error {
	address := net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
	listener, err := net.Listen("tcp4", address)
	if err != nil {
		if errors.Is(err, windows.WSAEADDRINUSE) {
			return fmt.Errorf("порт %s занят. Остановите другое приложение, использующее этот порт: %w", address, err)
		}
		return fmt.Errorf("не удалось открыть порт %s для сервера: %w", address, err)
	}
	// Vanilla must bind the port itself, so this check cannot reserve it.
	return listener.Close()
}

func validateLaunchWorld(dir string) error {
	info, err := launchWorldEntry(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return errors.New("world должен быть обычной папкой")
	}

	info, err = launchWorldEntry(filepath.Join(dir, "level.dat"))
	switch {
	case err == nil:
		if !info.Mode().IsRegular() {
			return errors.New("level.dat должен быть обычным файлом")
		}
		return vanilla.ValidateWorld(dir)
	case !errors.Is(err, os.ErrNotExist):
		return err
	}

	// Vanilla may create the session lock and empty directories before its
	// first save. Allow only these artifacts when level.dat is absent.
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		path := filepath.Join(dir, entry.Name())
		switch entry.Name() {
		case "session.lock":
			if err := checkFreshSessionLock(path); err != nil {
				return err
			}
		case "advancements", "data", "datapacks", "DIM-1", "DIM1", "entities", "playerdata", "poi", "region", "stats":
			info, err := launchWorldEntry(path)
			if err != nil {
				return err
			}
			if info.IsDir() {
				children, err := os.ReadDir(path)
				if err != nil {
					return err
				}
				if len(children) == 0 {
					continue
				}
			}
			fallthrough
		default:
			return fmt.Errorf("level.dat отсутствует, но в мире есть данные (%s). Проверьте мир или восстановите его из резервной копии", entry.Name())
		}
	}
	return nil
}

func launchWorldEntry(path string) (os.FileInfo, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	attributes, err := windows.GetFileAttributes(name)
	if err != nil {
		return nil, err
	}
	if attributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		return nil, fmt.Errorf("ссылки и точки повторной обработки в мире запрещены (%s)", filepath.Base(path))
	}
	return info, nil
}

func checkFreshSessionLock(path string) error {
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	handle, err := windows.CreateFile(name, windows.GENERIC_READ, 0, nil, windows.OPEN_EXISTING,
		windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		return fmt.Errorf("не удалось проверить session.lock. Остановите сервер, использующий этот мир: %w", err)
	}
	file := os.NewFile(uintptr(handle), path)
	defer file.Close()
	var opened windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(handle, &opened); err != nil {
		return err
	}
	if opened.NumberOfLinks != 1 || opened.FileAttributes&(windows.FILE_ATTRIBUTE_REPARSE_POINT|windows.FILE_ATTRIBUTE_DIRECTORY) != 0 {
		return errors.New("session.lock должен быть обычным файлом без ссылок")
	}
	data, err := io.ReadAll(io.LimitReader(file, 4))
	if err != nil {
		return err
	}
	if len(data) != 0 && string(data) != "☃" {
		return errors.New("level.dat отсутствует, а session.lock имеет неожиданный формат")
	}
	return nil
}
