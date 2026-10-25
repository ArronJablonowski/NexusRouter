//go:build !darwin && !linux

package app

import (
	"os/exec"
	"time"
)

func configureDocumentProcess(cmd *exec.Cmd) func() { cmd.WaitDelay = time.Second; return func() {} }
