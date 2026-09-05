//go:build darwin || linux

package cli

import (
	"os/exec"
	"syscall"
)

func detachDaemon(command *exec.Cmd) bool {
	command.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	return true
}
