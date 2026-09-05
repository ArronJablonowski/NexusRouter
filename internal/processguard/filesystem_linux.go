//go:build linux

package processguard

import (
	"os"

	"golang.org/x/sys/unix"
)

func localFilesystem(root *os.Root) bool {
	file, err := root.Open(".")
	if err != nil {
		return false
	}
	defer file.Close()
	var stat unix.Statfs_t
	if unix.Fstatfs(int(file.Fd()), &stat) != nil {
		return false
	}
	// Do not infer reliable local ownership on network or layered filesystems.
	// Container/overlay qualification is separate from the in-process MVP.
	switch stat.Type {
	case unix.EXT4_SUPER_MAGIC, unix.TMPFS_MAGIC, unix.XFS_SUPER_MAGIC, unix.BTRFS_SUPER_MAGIC:
		return true
	}
	return false
}
