//go:build !windows

package searchcore

import (
	"fmt"
	"os"
	"syscall"

	"golang.org/x/sys/unix"
)

// FilesystemIdentityFunc is the probe one_file_system uses to decide whether a
// directory still belongs to the search root's filesystem. It is a variable so
// a caller outside this package (the local tool's tests) can inject a boundary
// without mount privileges; production never reassigns it.
var FilesystemIdentityFunc = filesystemIdentity

// filesystemIdentity names the device a path lives on. one_file_system compares
// these values, so it must be stable and cheap: st_dev is exactly what the OS
// uses to answer the same question.
func filesystemIdentity(path string) (string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	// On Linux os.Stat returns a *syscall.Stat_t, not *unix.Stat_t, even though
	// the two have identical layouts; asserting only the unix type silently
	// fails every call. Accept both, and fall back to the syscall type.
	if stat, ok := info.Sys().(*syscall.Stat_t); ok && stat != nil {
		return fmt.Sprintf("%d", stat.Dev), nil
	}
	stat, ok := info.Sys().(*unix.Stat_t)
	if !ok || stat == nil {
		return "", fmt.Errorf("无法获取文件系统标识: %s", path)
	}
	return fmt.Sprintf("%d", stat.Dev), nil
}
