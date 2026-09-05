//go:build !darwin && !linux

package cli

import "os/exec"

func detachDaemon(*exec.Cmd) bool { return false }
