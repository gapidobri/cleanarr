//go:build !unix

package scanner

import (
	"errors"
	"os"
)

// Hardlink detection is only supported on unix systems.
func Inode(fi os.FileInfo) (ino, nlink uint64) { return 0, 1 }

func DiskUsage(path string) (total, free uint64, err error) {
	return 0, 0, errors.New("not supported on this platform")
}
