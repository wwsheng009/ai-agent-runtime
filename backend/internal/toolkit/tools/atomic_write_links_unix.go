//go:build !windows

package tools

import (
	"os"
	"syscall"
)

// hasMultipleHardLinks reports whether the file shares its inode with another
// directory entry: renaming over it would split the link set, so the write path
// switches to an in-place rewrite (review M4).
func hasMultipleHardLinks(info os.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && stat.Nlink > 1
}
