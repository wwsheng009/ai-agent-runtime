//go:build !windows

package remotetools

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

func lockIdentityFile(path string) (*os.File, error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		_ = file.Close()
		if errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EAGAIN) {
			return nil, errIdentityLocked
		}
		return nil, err
	}
	return file, nil
}
