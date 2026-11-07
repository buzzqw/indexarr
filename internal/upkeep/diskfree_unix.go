//go:build unix

package upkeep

import (
	"path/filepath"
	"syscall"
)

// freeBytes returns the space available to this process on the file system
// that holds path, or -1 when it cannot be read.
func freeBytes(path string) int64 {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(filepath.Dir(path), &stat); err != nil {
		return -1
	}
	return int64(stat.Bavail) * int64(stat.Bsize)
}
