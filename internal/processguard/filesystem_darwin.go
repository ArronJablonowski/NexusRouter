//go:build darwin

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
	if unix.Fstatfs(int(file.Fd()), &stat) != nil || stat.Flags&unix.MNT_LOCAL == 0 {
		return false
	}
	name := unix.ByteSliceToString(stat.Fstypename[:])
	return name == "apfs" || name == "hfs"
}
