//go:build !darwin && !linux

package telegrambridge

import "os"

func lockState(*os.Root) (func(), error) { return nil, ErrState }
