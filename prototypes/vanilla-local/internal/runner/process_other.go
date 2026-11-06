//go:build !windows

package runner

import (
	"context"
	"errors"
	"os/exec"
)

type processJob struct{}

func (*processJob) close() {}

func startProcess(context.Context, *exec.Cmd) (processJob, error) {
	return processJob{}, errors.New("running Vanilla servers is supported on Windows")
}
