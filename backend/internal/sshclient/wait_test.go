package sshclient

// 本文件回归隧道监督所依赖的语义：Client.Wait 在传输层结束时返回、
// 连接存活期间保持阻塞；Close 并发安全、幂等。

import (
	"crypto/ed25519"
	"crypto/rand"
	"io"
	"net"
	"strconv"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

// killableSSHServer 是最小可断链 SSH 服务器（只完成握手/认证）。
type killableSSHServer struct {
	ln    net.Listener
	mu    sync.Mutex
	conns []net.Conn
}

func newKillableSSHServer(t *testing.T) (*killableSSHServer, string, int) {
	t.Helper()

	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate host key: %v", err)
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatalf("signer: %v", err)
	}
	cfg := &ssh.ServerConfig{
		PasswordCallback: func(ssh.ConnMetadata, []byte) (*ssh.Permissions, error) {
			return nil, nil // 接受任意密码
		},
	}
	cfg.AddHostKey(signer)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("server listen: %v", err)
	}
	s := &killableSSHServer{ln: ln}
	go func() {
		for {
			nc, err := ln.Accept()
			if err != nil {
				return
			}
			s.mu.Lock()
			s.conns = append(s.conns, nc)
			s.mu.Unlock()

			go func(nc net.Conn) {
				sconn, chans, reqs, err := ssh.NewServerConn(nc, cfg)
				if err != nil {
					_ = nc.Close()
					return
				}
				defer sconn.Close()
				go ssh.DiscardRequests(reqs)
				for newCh := range chans {
					_ = newCh.Reject(ssh.UnknownChannelType, "unsupported channel")
				}
			}(nc)
		}
	}()
	t.Cleanup(s.Kill)

	host, portStr, err := net.SplitHostPort(ln.Addr().String())
	if err != nil {
		t.Fatalf("split addr: %v", err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		t.Fatalf("parse port: %v", err)
	}
	return s, host, port
}

// Kill 关闭监听并强制断开所有连接（幂等）。
func (s *killableSSHServer) Kill() {
	s.mu.Lock()
	ln := s.ln
	conns := s.conns
	s.ln = nil
	s.conns = nil
	s.mu.Unlock()

	if ln != nil {
		_ = ln.Close()
	}
	for _, c := range conns {
		_ = c.Close()
	}
}

// TestClientWaitUnblocksOnTransportLoss 验证 Wait 能在链路中断时立即解除阻塞
// （隧道进程据此触发重连，而不是变成「端口还在、转发已死」的僵尸）。
func TestClientWaitUnblocksOnTransportLoss(t *testing.T) {
	srv, host, port := newKillableSSHServer(t)

	client, err := NewClient(newTestOpts(host, port, 5*time.Second), io.Discard)
	if err != nil {
		t.Fatalf("connect to stub server: %v", err)
	}
	defer client.Close()

	done := make(chan error, 1)
	go func() { done <- client.Wait() }()

	// 连接存活期间必须保持阻塞
	select {
	case err := <-done:
		t.Fatalf("Wait returned while transport still alive: %v", err)
	case <-time.After(300 * time.Millisecond):
	}

	// 模拟链路中断 / 服务器重启
	srv.Kill()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Wait did not return after transport loss (zombie tunnel risk)")
	}
}

// TestClientWaitAfterCloseIsImmediate 验证 Close 语义：幂等，且不再有活跃连接时 Wait 立即返回。
func TestClientWaitAfterCloseIsImmediate(t *testing.T) {
	srv, host, port := newKillableSSHServer(t)
	defer srv.Kill()

	client, err := NewClient(newTestOpts(host, port, 5*time.Second), io.Discard)
	if err != nil {
		t.Fatalf("connect to stub server: %v", err)
	}

	done := make(chan error, 1)
	go func() { done <- client.Wait() }()

	if err := client.Close(); err != nil {
		t.Logf("Close returned error (acceptable on abrupt teardown): %v", err)
	}
	if err := client.Close(); err != nil {
		t.Fatalf("second Close must be a no-op, got: %v", err)
	}

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Wait did not return after Close")
	}
	if err := client.Wait(); err != nil {
		t.Fatalf("Wait after Close = %v, want nil", err)
	}
}
