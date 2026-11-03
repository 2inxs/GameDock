//go:build !windows

package network

import (
	"os"
	"os/exec"
)

func hideWindow(cmd *exec.Cmd)           {}
func privateDirectory(path string) error { return os.Chmod(path, 0700) }
