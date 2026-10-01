//go:build !darwin && !linux

package openhands

import "os/exec"

// Job-object ownership has not yet been implemented on other platforms.
func processSupported() bool           { return false }
func configureProcess(*exec.Cmd)       {}
func killProcessGroup(*exec.Cmd) error { return ErrRun }
func groupStillAlive(*exec.Cmd) bool   { return true }
