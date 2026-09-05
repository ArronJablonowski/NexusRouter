//go:build darwin

package releasepack

import "golang.org/x/sys/unix"

func publish(source, target string) error {
	return unix.RenameatxNp(unix.AT_FDCWD, source, unix.AT_FDCWD, target, unix.RENAME_EXCL)
}
