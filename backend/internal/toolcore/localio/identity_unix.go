//go:build !windows

package localio

import (
	"context"
	"fmt"
	"os"
	"syscall"

	"golang.org/x/sys/unix"
)

// FilesystemIdentity names the device a path lives on. one_file_system compares these
// values, so it must be stable and cheap: st_dev is exactly what the OS uses to answer the
// same question.
//
// This body is a move out of the search core, not a rewrite of it (design §4.4: the local
// implementation is where host calls belong, so a core can describe another machine). The
// comment below is the reason the two stat types are both accepted, and it cost a debugging
// cycle to learn: on Linux os.Stat returns a *syscall.Stat_t, not *unix.Stat_t, even though
// the two have identical layouts, so asserting only the unix type silently fails every call.
func FilesystemIdentity(path string) (string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if stat, ok := info.Sys().(*syscall.Stat_t); ok && stat != nil {
		return fmt.Sprintf("%d", stat.Dev), nil
	}
	stat, ok := info.Sys().(*unix.Stat_t)
	if !ok || stat == nil {
		return "", fmt.Errorf("无法获取文件系统标识: %s", path)
	}
	return fmt.Sprintf("%d", stat.Dev), nil
}

// FilesystemIdentity is the seam's answer for this machine's filesystem, so a core that was
// handed localio.Access asks the same question the pre-seam code asked directly.
func (osAccess) FilesystemIdentity(_ context.Context, path string) (string, error) {
	return FilesystemIdentity(path)
}
