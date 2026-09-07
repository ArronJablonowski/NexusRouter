//go:build linux

package releasepack

import (
	"os"

	"golang.org/x/sys/unix"
)

func publishWithin(parent *os.File, source, target string) error {
	fd := int(parent.Fd())
	return unix.Renameat2(fd, source, fd, target, unix.RENAME_NOREPLACE)
}
