package hermes

import (
	"os/exec"
	"syscall"
)

// The kernel terminates the direct harness if its owning host thread dies.
// runProcess pins that thread until the child has been reaped.
func configureProcess(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true, Pdeathsig: syscall.SIGKILL}
}
