//go:build !windows

package tools

import (
	"fmt"
	"os"
	"syscall"

	"golang.org/x/sys/unix"
)

func fileSystemIdentity(path string) (string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	// On Linux os.Stat returns *syscall.Stat_t, not *unix.Stat_t, even though
	// the two have identical layouts. Asserting only the unix type made every
	// call fail, so one_file_system could never take effect here.
	if stat, ok := info.Sys().(*syscall.Stat_t); ok && stat != nil {
		return fmt.Sprintf("%d", stat.Dev), nil
	}
	stat, ok := info.Sys().(*unix.Stat_t)
	if !ok || stat == nil {
		return "", fmt.Errorf("无法获取文件系统标识: %s", path)
	}
	return fmt.Sprintf("%d", stat.Dev), nil
}
