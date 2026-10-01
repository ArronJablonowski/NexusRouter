//go:build darwin || linux

package remote

import (
	"errors"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

func lockReviewQueue(directory string) (func(), error) {
	path := filepath.Join(directory, "worker.lock")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0600)
	if err != nil {
		return nil, ErrUnavailable
	}
	fail := func() (func(), error) { file.Close(); return nil, ErrUnavailable }
	st, err := file.Stat()
	if err != nil || !st.Mode().IsRegular() || st.Mode().Perm()&0077 != 0 {
		return fail()
	}
	if err := unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		if errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EAGAIN) {
			file.Close()
			return nil, ErrReviewQueueBusy
		}
		return fail()
	}
	current, err := os.Lstat(path)
	if err != nil || !os.SameFile(st, current) {
		return fail()
	}
	return func() { file.Close() }, nil
}
