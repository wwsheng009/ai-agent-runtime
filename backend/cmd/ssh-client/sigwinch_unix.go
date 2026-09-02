//go:build !windows

package main

import (
	"os"
	"os/signal"
	"syscall"

	"golang.org/x/crypto/ssh"
	"golang.org/x/term"
)

// watchWindowSize 监听终端窗口大小变更并向远程会话传播（Unix 平台）。
func watchWindowSize(session *ssh.Session, fd int) func() {
	winCh := make(chan os.Signal, 1)
	signal.Notify(winCh, syscall.SIGWINCH)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case <-winCh:
				w, h, _ := term.GetSize(fd)
				if w > 0 && h > 0 {
					_ = session.WindowChange(h, w)
				}
			case <-done:
				return
			}
		}
	}()
	return func() {
		signal.Stop(winCh)
		close(done)
	}
}
