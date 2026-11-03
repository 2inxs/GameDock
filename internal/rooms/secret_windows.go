//go:build windows

package rooms

import (
	"errors"
	"unsafe"

	"golang.org/x/sys/windows"
)

const protectionName = "windows-dpapi-v1"

func roomBlob(data []byte) windows.DataBlob {
	if len(data) == 0 {
		return windows.DataBlob{}
	}
	return windows.DataBlob{Size: uint32(len(data)), Data: &data[0]}
}

func protectRooms(data []byte) ([]byte, error) {
	in := roomBlob(data)
	entropy := roomBlob([]byte("GameDock.Rooms.v1"))
	var out windows.DataBlob
	if err := windows.CryptProtectData(&in, nil, &entropy, 0, nil, windows.CRYPTPROTECT_UI_FORBIDDEN, &out); err != nil {
		return nil, err
	}
	defer windows.LocalFree(windows.Handle(unsafe.Pointer(out.Data)))
	return append([]byte(nil), unsafe.Slice(out.Data, int(out.Size))...), nil
}

func unprotectRooms(data []byte) ([]byte, error) {
	if len(data) == 0 || len(data) > maxStoreBytes {
		return nil, errors.New("неверный размер защищённых данных")
	}
	in := roomBlob(data)
	entropy := roomBlob([]byte("GameDock.Rooms.v1"))
	var out windows.DataBlob
	if err := windows.CryptUnprotectData(&in, nil, &entropy, 0, nil, windows.CRYPTPROTECT_UI_FORBIDDEN, &out); err != nil {
		return nil, err
	}
	defer windows.LocalFree(windows.Handle(unsafe.Pointer(out.Data)))
	plaintext := unsafe.Slice(out.Data, int(out.Size))
	defer clear(plaintext)
	return append([]byte(nil), plaintext...), nil
}

func replaceRoomFile(source, destination string) error {
	from, err := windows.UTF16PtrFromString(source)
	if err != nil {
		return err
	}
	to, err := windows.UTF16PtrFromString(destination)
	if err != nil {
		return err
	}
	return windows.MoveFileEx(from, to, windows.MOVEFILE_REPLACE_EXISTING|windows.MOVEFILE_WRITE_THROUGH)
}
