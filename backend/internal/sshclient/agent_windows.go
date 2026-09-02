//go:build windows

package sshclient

import (
	"io"

	"github.com/Microsoft/go-winio"
)

// dialAgent 拨号连接 Windows OpenSSH agent named pipe。
func dialAgent() (io.ReadWriteCloser, error) {
	return winio.DialPipe(`\\.\pipe\openssh-ssh-agent`, nil)
}
