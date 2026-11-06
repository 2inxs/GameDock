package world

import (
	"errors"
	"fmt"

	"golang.org/x/sys/windows"
)

// Keep an existing session lock closed to other handles throughout import.
// A stopped source without a session.lock file remains a caller precondition.
func lockSourceWorld(path string) (func(), error) {
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	handle, err := windows.CreateFile(name, windows.GENERIC_READ, 0, nil, windows.OPEN_EXISTING,
		windows.FILE_ATTRIBUTE_NORMAL|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if errors.Is(err, windows.ERROR_FILE_NOT_FOUND) {
		return func() {}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("не удалось заблокировать исходный мир. Остановите исходный сервер: %w", err)
	}
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(handle, &info); err != nil ||
		info.FileAttributes&(windows.FILE_ATTRIBUTE_REPARSE_POINT|windows.FILE_ATTRIBUTE_DIRECTORY) != 0 {
		windows.CloseHandle(handle)
		return nil, errors.New("session.lock должен быть обычным файлом")
	}
	return func() { windows.CloseHandle(handle) }, nil
}
