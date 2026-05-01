//go:build !windows

package ui

import (
	"time"

	"golang.org/x/sys/unix"
)

func waitForInteractiveInputReady(fd int, timeout time.Duration) (bool, error) {
	if timeout < 0 {
		timeout = 0
	}
	ms := int(timeout / time.Millisecond)
	if timeout > 0 && ms == 0 {
		ms = 1
	}
	pollFD := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}
	for {
		n, err := unix.Poll(pollFD, ms)
		if err == unix.EINTR {
			continue
		}
		if err != nil {
			return false, err
		}
		if n <= 0 {
			return false, nil
		}
		return pollFD[0].Revents&unix.POLLIN != 0, nil
	}
}
