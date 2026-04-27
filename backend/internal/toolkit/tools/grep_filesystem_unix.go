//go:build !windows

package tools

import (
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

func fileSystemIdentity(path string) (string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	stat, ok := info.Sys().(*unix.Stat_t)
	if !ok || stat == nil {
		return "", fmt.Errorf("无法获取文件系统标识: %s", path)
	}
	return fmt.Sprintf("%d", stat.Dev), nil
}
