//go:build !darwin && !linux

package skills

import (
	"context"
	"errors"
	"os"
)

func lockFile(context.Context, *os.File) error {
	return errors.New("skills: file locking unsupported on this platform")
}
func unlockFile(*os.File) {}
