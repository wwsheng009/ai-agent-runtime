//go:build !windows

package tools

import (
	"os"
	"syscall"
)

// hasMultipleHardLinks is used by the link-fidelity regression test.
func hasMultipleHardLinks(info os.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && stat.Nlink > 1
}
