package sshclient

// 本文件是针对「连接/命令卡死」问题的回归测试：
//   - TestNewClientHandshakeTimeout   服务器 accept TCP 后不回任何 SSH 协议字节，
//     NewClient 必须在 ConnectTimeout 内返回错误，而不是永久阻塞。
//   - TestRunCommandContextTimeout    服务器完成握手与认证后，对 exec 请求永不发送
//     exit-status，RunCommandContext 必须在 ctx 超时时立即返回。
//
// 两个测试都使用内存/回环自建服务器，不依赖 docker 或外部 ~/.ssh 配置。

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"io"
	"net"
	"strconv"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

// silentListener 接受 TCP 连接后保持沉默（不回任何字节）。
type silentListener struct {
	ln     net.Listener
	closed chan struct{}
}

func newSilentListener(t *testing.T) *silentListener {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	s := &silentListener{ln: ln, closed: make(chan struct{})}
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				close(s.closed)
				return
			}
			go func(c net.Conn) {
				<-s.closed // 测试结束才关闭
				_ = c.Close()
			}(conn)
		}
	}()
	t.Cleanup(func() {
		_ = ln.Close()
		<-s.closed
	})
	return s
}

// newStubSSHServer 启动一个真实的最小 SSH 服务器（密码认证），
// 对 session 的 exec 请求回复 WantReply 但永不发送 exit-status，
// 用于模拟「远程命令永远不结束」的场景。
func newStubSSHServer(t *testing.T) (host string, port int, stop func()) {
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
	done := make(chan struct{})
	go func() {
		for {
			nc, err := ln.Accept()
			if err != nil {
				close(done)
				return
			}
			go func(nc net.Conn) {
				sconn, chans, reqs, err := ssh.NewServerConn(nc, cfg)
				if err != nil {
					_ = nc.Close()
					return
				}
				defer sconn.Close()
				go ssh.DiscardRequests(reqs)
				for newCh := range chans {
					switch newCh.ChannelType() {
					case "session":
						ch, reqsCh, err := newCh.Accept()
						if err != nil {
							continue
						}
						go func(ch ssh.Channel, reqsCh <-chan *ssh.Request) {
							defer ch.Close()
							for req := range reqsCh {
								switch req.Type {
								case "exec":
									// 接受命令，但不执行、不发送 exit-status：
									// 客户端 session.Run 将一直阻塞等待。
									if req.WantReply {
										_ = req.Reply(true, nil)
									}
								default:
									if req.WantReply {
										_ = req.Reply(false, nil)
									}
								}
							}
						}(ch, reqsCh)
					default:
						_ = newCh.Reject(ssh.UnknownChannelType, "unsupported channel")
					}
				}
			}(nc)
		}
	}()

	t.Cleanup(func() {
		_ = ln.Close()
		<-done
	})

	host, portStr, err := net.SplitHostPort(ln.Addr().String())
	if err != nil {
		t.Fatalf("split addr: %v", err)
	}
	port, err = strconv.Atoi(portStr)
	if err != nil {
		t.Fatalf("parse port: %v", err)
	}
	return host, port, func() { _ = ln.Close() }
}

// newTestOpts 构造测试用 Options：密码认证 + 跳过 host key 校验，
// 不读取用户的 ~/.ssh 配置，保证测试可重复、可并行。
func newTestOpts(host string, port int, connectTimeout time.Duration) *Options {
	opts := Defaults()
	opts.Host = host
	opts.Port = port
	opts.Password = "pw"
	opts.PasswordSet = true
	opts.StrictHostKeyChecking = StrictModeNo
	opts.ConnectTimeout = connectTimeout
	return opts
}

// TestNewClientHandshakeTimeout 回归：半开连接（accept 后无响应）时，
// NewClient 不得无限阻塞于 SSH 握手。
func TestNewClientHandshakeTimeout(t *testing.T) {
	s := newSilentListener(t)
	host, portStr, err := net.SplitHostPort(s.ln.Addr().String())
	if err != nil {
		t.Fatalf("split addr: %v", err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		t.Fatalf("parse port: %v", err)
	}

	opts := newTestOpts(host, port, time.Second)

	start := time.Now()
	client, err := NewClient(opts, io.Discard)
	elapsed := time.Since(start)
	if err == nil {
		_ = client.Close()
		t.Fatal("expected handshake timeout error, got nil")
	}
	if elapsed > 5*time.Second {
		t.Fatalf("NewClient hung too long: elapsed=%v err=%v", elapsed, err)
	}
	if elapsed < 700*time.Millisecond {
		t.Fatalf("NewClient returned before deadline (%v), deadline not enforced? err=%v", elapsed, err)
	}
	t.Logf("NewClient returned in %v: %v", elapsed, err)
}

// TestRunCommandContextTimeout 回归：远程命令永不结束时，
// RunCommandContext 应在 ctx 超时后立即返回（通过断开连接解除阻塞）。
func TestRunCommandContextTimeout(t *testing.T) {
	host, port, _ := newStubSSHServer(t)

	opts := newTestOpts(host, port, 5*time.Second)
	client, err := NewClient(opts, io.Discard)
	if err != nil {
		t.Fatalf("connect to stub server: %v", err)
	}
	defer client.Close()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	start := time.Now()
	code, err := RunCommandContext(ctx, client.SSHSession(), "sleep 999", io.Discard, io.Discard)
	elapsed := time.Since(start)
	if err == nil {
		t.Fatalf("expected context timeout error, got nil (code=%d)", code)
	}
	if elapsed > 5*time.Second {
		t.Fatalf("RunCommandContext hung too long: elapsed=%v err=%v", elapsed, err)
	}
	t.Logf("RunCommandContext returned in %v: code=%d err=%v", elapsed, code, err)
}

// TestCloseIdempotent 回归：重复 Close 不应 panic（keepAlive stopCh 已置 nil）。
func TestCloseIdempotent(t *testing.T) {
	host, port, _ := newStubSSHServer(t)
	opts := newTestOpts(host, port, 5*time.Second)
	opts.ServerAliveInterval = 100 * time.Millisecond // 开启保活，覆盖 stopCh 路径
	client, err := NewClient(opts, io.Discard)
	if err != nil {
		t.Fatalf("connect to stub server: %v", err)
	}
	if err := client.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	if err := client.Close(); err != nil {
		t.Fatalf("second Close should be safe: %v", err)
	}
}
