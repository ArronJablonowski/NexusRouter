//go:build darwin || linux

package goose

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
)

func processSupported() bool { return true }

func killProcessGroup(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return os.ErrProcessDone
	}
	err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	if errors.Is(err, syscall.ESRCH) {
		return os.ErrProcessDone
	}
	return err
}
func groupStillAlive(cmd *exec.Cmd) bool {
	return !errors.Is(syscall.Kill(-cmd.Process.Pid, 0), syscall.ESRCH)
}
