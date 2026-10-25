//go:build darwin || linux

package telegrambridge

import (
	"golang.org/x/sys/unix"
	"os"
)

func lockState(root *os.Root) (func(), error) {
	f, err := root.OpenFile("worker.lock", os.O_CREATE|os.O_RDWR|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0600)
	if err != nil {
		return nil, ErrState
	}
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB) != nil {
		f.Close()
		return nil, ErrState
	}
	current, err := root.Lstat("worker.lock")
	if err != nil || !os.SameFile(info, current) {
		f.Close()
		return nil, ErrState
	}
	return func() { f.Close() }, nil
}
