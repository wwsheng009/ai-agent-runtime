//go:build !windows

package sshclient

import (
	"errors"
	"io"
	"net"
	"os"
)

// dialAgent 拨号连接 ssh-agent（Unix domain socket）。
func dialAgent() (io.ReadWriteCloser, error) {
	sock := os.Getenv("SSH_AUTH_SOCK")
	if sock == "" {
		return nil, errors.New("SSH_AUTH_SOCK not set")
	}
	return net.Dial("unix", sock)
}
