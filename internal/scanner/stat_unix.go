//go:build unix

package scanner

import (
	"os"
	"syscall"
)

func Inode(fi os.FileInfo) (ino, nlink uint64) {
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		return uint64(st.Ino), uint64(st.Nlink)
	}
	return 0, 1
}

// DiskUsage returns total and free bytes of the filesystem holding path.
func DiskUsage(path string) (total, free uint64, err error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0, 0, err
	}
	return uint64(st.Blocks) * uint64(st.Bsize), uint64(st.Bavail) * uint64(st.Bsize), nil
}
