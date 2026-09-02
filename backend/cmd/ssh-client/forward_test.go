package main

// 本文件验证 OpenSSH 兼容的 -L 转发简写（3 段：port:host:hostport）端到端可用。
// 与 internal/sshclient 的 stub server 模式一致：自建内存回环服务器，不依赖 docker。

import (
	"crypto/ed25519"
	"crypto/rand"
	"fmt"
	"io"
	"net"
	"strconv"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/wwsheng009/ai-agent-runtime/internal/sshclient"
)

// newEchoServer 启动一个回显 TCP 服务，返回 "127.0.0.1:port"。
func newEchoServer(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("echo listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				_, _ = io.Copy(c, c) // echo
			}(c)
		}
	}()
	return ln.Addr().String()
}

// newDirectTCPIPServer 启动支持 direct-tcpip 通道的 SSH 服务器，
// 返回 "127.0.0.1:port"。
func newDirectTCPIPServer(t *testing.T) string {
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
	t.Cleanup(func() { _ = ln.Close() })

	go func() {
		for {
			nc, err := ln.Accept()
			if err != nil {
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
					if newCh.ChannelType() != "direct-tcpip" {
						_ = newCh.Reject(ssh.UnknownChannelType, "unsupported channel")
						continue
					}
					// direct-tcpip payload 必须完整消费：
					// string dest_addr, uint32 dest_port, string orig_addr, uint32 orig_port
					var payload struct {
						Addr       string
						Port       uint32
						OriginAddr string
						OriginPort uint32
					}
					if err := ssh.Unmarshal(newCh.ExtraData(), &payload); err != nil {
						_ = newCh.Reject(ssh.ConnectionFailed, "bad payload")
						continue
					}
					ch, reqsCh, err := newCh.Accept()
					if err != nil {
						continue
					}
					go ssh.DiscardRequests(reqsCh)
					targetConn, err := net.Dial("tcp", net.JoinHostPort(payload.Addr, strconv.Itoa(int(payload.Port))))
					if err != nil {
						_ = ch.Close()
						continue
					}
					go func() {
						defer ch.Close()
						defer targetConn.Close()
						go func() { _, _ = io.Copy(ch, targetConn) }()
						_, _ = io.Copy(targetConn, ch)
					}()
				}
			}(nc)
		}
	}()
	return ln.Addr().String()
}

// freeTCPPort 占用后释放一个回环端口，用于预留给转发监听。
func freeTCPPort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()
	return port
}

// TestStartLocalForwardShorthand 验证 OpenSSH 3 段简写 -L port:host:hostport：
// bind 省略时默认绑定 localhost，且流量能通过 SSH 隧道到达目标回显服务。
func TestStartLocalForwardShorthand(t *testing.T) {
	echoAddr := newEchoServer(t)
	serverAddr := newDirectTCPIPServer(t)

	host, portStr, err := net.SplitHostPort(serverAddr)
	if err != nil {
		t.Fatalf("split server addr: %v", err)
	}
	port, _ := strconv.Atoi(portStr)

	opts := sshclient.Defaults()
	opts.Host = host
	opts.Port = port
	opts.Password = "pw"
	opts.PasswordSet = true
	opts.StrictHostKeyChecking = sshclient.StrictModeNo
	opts.ConnectTimeout = 5 * time.Second

	client, err := sshclient.NewClient(opts, io.Discard)
	if err != nil {
		t.Fatalf("connect to stub server: %v", err)
	}
	defer client.Close()

	_, echoPortStr, _ := net.SplitHostPort(echoAddr)
	localPort := freeTCPPort(t)
	// 3 段简写：port:host:hostport（bind 默认 localhost）
	spec := fmt.Sprintf("%d:127.0.0.1:%s", localPort, echoPortStr)
	if err := startLocalForward(client.SSHSession(), spec); err != nil {
		t.Fatalf("startLocalForward(%q): %v", spec, err)
	}

	// 连接到本地转发端口，验证回显
	conn, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", localPort))
	if err != nil {
		t.Fatalf("dial local forward: %v", err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))

	payload := []byte("forward-ok-12345")
	if _, err := conn.Write(payload); err != nil {
		t.Fatalf("write: %v", err)
	}
	buf := make([]byte, len(payload))
	if _, err := io.ReadFull(conn, buf); err != nil {
		t.Fatalf("read echo: %v", err)
	}
	if string(buf) != string(payload) {
		t.Fatalf("echo mismatch: got %q, want %q", buf, payload)
	}
}
