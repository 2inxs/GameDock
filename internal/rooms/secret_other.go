//go:build !windows

package rooms

import "os"

// The non-Windows fallback relies on owner-only file permissions; it does not
// claim to encrypt the room password. Windows builds always use user-bound DPAPI.
const protectionName = "file-permissions-v1"

func protectRooms(data []byte) ([]byte, error)         { return append([]byte(nil), data...), nil }
func unprotectRooms(data []byte) ([]byte, error)       { return append([]byte(nil), data...), nil }
func replaceRoomFile(source, destination string) error { return os.Rename(source, destination) }
