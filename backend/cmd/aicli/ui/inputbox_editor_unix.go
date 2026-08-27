//go:build !windows

package ui

import (
	"errors"
	"time"

	"golang.org/x/sys/unix"
)

func platformWaitForInteractiveInputReady(fd int, timeout time.Duration) (bool, error) {
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

func platformClipboardText() (string, error) {
	return "", errors.New("platform clipboard paste unsupported")
}

// interactiveStdinNeedsPolledReadiness 在 unix 下恒为 true：Poll 对 tty
// 和管道都可靠，维持轮询可取消语义不变。
func interactiveStdinNeedsPolledReadiness() bool { return true }

// Unix 终端输出直接走 pty（无 MobaXterm 式按行桥接缓冲），无需帧尾 \n 补偿。
func interactiveOutputNeedsTrailingNewline() bool { return false }

func platformConsumeSpecialInteractiveKey(int) (editorKey, bool, error) {
	return editorKey{}, false, nil
}
