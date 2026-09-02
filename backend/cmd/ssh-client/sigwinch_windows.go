//go:build windows

package main

import (
	"golang.org/x/crypto/ssh"
)

// watchWindowSize 在 Windows 平台为 no-op（Windows 无 SIGWINCH 信号）。
func watchWindowSize(_ *ssh.Session, _ int) func() {
	return func() {}
}
