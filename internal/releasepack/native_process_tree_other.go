//go:build !darwin && !linux

package releasepack

import (
	"os"
	"os/exec"
)

func configureNativeProcessTree(_ *exec.Cmd) {}

func terminateNativeProcessTree(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return os.ErrProcessDone
	}
	return cmd.Process.Kill()
}
