//go:build !darwin && !linux

package releasepack

import "os"

func publishWithin(*os.File, string, string) error { return ErrApprovedBuild }
