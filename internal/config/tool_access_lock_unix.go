//go:build darwin || linux

package config

import (
	"golang.org/x/sys/unix"
	"os"
)

// Keep a stable sidecar inode: locking the config itself would lose exclusion
// when Rename replaces it. All participating writers hold this through sync.
// Editors which do not participate in this protocol remain outside its scope.
func lockProjectUpdate(path string) (func(), error) {
	fd, err := unix.Open(path+".update.lock", unix.O_CREAT|unix.O_RDWR|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0600)
	if err != nil {
		return nil, ErrConfigWrite
	}
	f := os.NewFile(uintptr(fd), path+".update.lock")
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		f.Close()
		return nil, ErrConfigWrite
	}
	if err = unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB); err != nil {
		f.Close()
		if err == unix.EWOULDBLOCK || err == unix.EAGAIN {
			return nil, ErrConfigConflict
		}
		return nil, ErrConfigWrite
	}
	return func() { _ = f.Close() }, nil
}
