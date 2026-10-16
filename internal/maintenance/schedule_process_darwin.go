package maintenance

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
)

// Cancel the owned helper and launchctl children together. A killed transaction
// retains its recovery record for the next explicit schedule save.
func configureScheduleCommand(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		return err
	}
}
