//go:build darwin || linux

package githubpublish

import "golang.org/x/sys/unix"

func duplicateCredentialDescriptor(fd int) (int, error) {
	return unix.FcntlInt(uintptr(fd), unix.F_DUPFD_CLOEXEC, 0)
}

func closeCredentialDescriptor(fd int) {
	_ = unix.Close(fd)
}
