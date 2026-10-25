//go:build !darwin

package maintenance

import "os/exec"

// Scheduling is rejected before execution on unsupported platforms.
func configureScheduleCommand(cmd *exec.Cmd) {}
