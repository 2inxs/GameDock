//go:build !windows

package process

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
)

type processTree struct{ pid int }

func prepareTree(cmd *exec.Cmd) (processTree, error) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	return processTree{}, nil
}
func (t *processTree) attach(p *os.Process) error { t.pid = p.Pid; return nil }
func (t *processTree) kill() error {
	if t.pid > 0 {
		err := syscall.Kill(-t.pid, syscall.SIGKILL)
		if errors.Is(err, syscall.ESRCH) {
			return nil
		}
		return err
	}
	return nil
}
func (t *processTree) close() { _ = t.kill() }
