//go:build windows

package ui

import "time"

func waitForInteractiveInputReady(fd int, timeout time.Duration) (bool, error) {
	_, _ = fd, timeout
	return true, nil
}
