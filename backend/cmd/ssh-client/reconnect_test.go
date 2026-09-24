package main

// 本文件回归「僵尸隧道」缺陷：-N（仅转发）模式下 SSH 传输层死亡时，
// 进程既不能继续持有失效监听的端口，也不能静默退出或永久挂起——
// 必须自动重连并重建转发，或在禁用重连时以 255 退出交由外部监督进程重启。

import (
	"crypto/ed25519"
	"crypto/rand"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/wwsheng009/ai-agent-runtime/internal/sshclient"
)

// tunnelTestServer 是可强制断链的最小 SSH 服务器（支持 direct-tcpip），
// 用于模拟「链路中断 / 服务器重启」。
type tunnelTestServer struct {
	host string
	port int

	ln    net.Listener
	mu    sync.Mutex
	conns []net.Conn
}

func newTunnelTestServer(t *testing.T) *tunnelTestServer {
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
	host, portStr, err := net.SplitHostPort(ln.Addr().String())
	if err != nil {
		t.Fatalf("split addr: %v", err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		t.Fatalf("parse port: %v", err)
	}

	s := &tunnelTestServer{host: host, port: port, ln: ln}
	go s.serve(ln, cfg)
	t.Cleanup(s.Kill)
	return s
}

func (s *tunnelTestServer) serve(ln net.Listener, cfg *ssh.ServerConfig) {
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
				if newCh.ChannelType() != "direct-tcpip" {
					_ = newCh.Reject(ssh.UnknownChannelType, "unsupported channel")
					continue
				}
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
				target, err := net.Dial("tcp", net.JoinHostPort(payload.Addr, strconv.Itoa(int(payload.Port))))
				if err != nil {
					_ = ch.Close()
					continue
				}
				go func() {
					defer ch.Close()
					defer target.Close()
					go func() { _, _ = io.Copy(ch, target) }()
					_, _ = io.Copy(target, ch)
				}()
			}
		}(nc)
	}
}

// Kill 关闭监听并强制断开所有已建立的连接（幂等）。
func (s *tunnelTestServer) Kill() {
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

// speedUpTunnelBackoff 压缩重连退避，便于测试快速观察重连行为。
func speedUpTunnelBackoff(t *testing.T) {
	t.Helper()
	base, max := tunnelBackoffBase, tunnelBackoffMax
	tunnelBackoffBase = 50 * time.Millisecond
	tunnelBackoffMax = 100 * time.Millisecond
	t.Cleanup(func() {
		tunnelBackoffBase = base
		tunnelBackoffMax = max
	})
}

func waitForCond(t *testing.T, timeout time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timeout waiting for %s", what)
}

// waitEcho 反复尝试经转发端口完成一次回显往返，直到成功或超时。
func waitEcho(t *testing.T, localPort int, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var lastErr error
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", localPort), time.Second)
		if err != nil {
			lastErr = err
			time.Sleep(50 * time.Millisecond)
			continue
		}
		_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
		payload := []byte("tunnel-ok")
		if _, err := conn.Write(payload); err != nil {
			lastErr = err
			_ = conn.Close()
			time.Sleep(50 * time.Millisecond)
			continue
		}
		buf := make([]byte, len(payload))
		_, err = io.ReadFull(conn, buf)
		_ = conn.Close()
		if err == nil && string(buf) == string(payload) {
			return
		}
		if err != nil {
			lastErr = err
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("forward did not become usable within %v (last error: %v)", timeout, lastErr)
}

// TestRunForwardOnlyReconnectsAfterTransportLoss 验证核心缺陷修复：
// 链路中断后自动重连、重新绑定监听端口并恢复转发；收到信号后干净退出并释放端口。
func TestRunForwardOnlyReconnectsAfterTransportLoss(t *testing.T) {
	speedUpTunnelBackoff(t)

	echoAddr := newEchoServer(t)
	_, echoPortStr, err := net.SplitHostPort(echoAddr)
	if err != nil {
		t.Fatalf("split echo addr: %v", err)
	}

	srv := newTunnelTestServer(t)

	var mu sync.Mutex
	cur := srv
	connects := 0
	connect := func(opts *sshclient.Options, stderr io.Writer) (*sshclient.Client, error) {
		mu.Lock()
		s := cur
		connects++
		mu.Unlock()
		opts.Host = s.host
		opts.Port = s.port
		return sshclient.NewClient(opts, stderr)
	}

	opts := sshclient.Defaults()
	opts.StrictHostKeyChecking = sshclient.StrictModeNo
	opts.Password = "pw"
	opts.PasswordSet = true
	opts.ConnectTimeout = 5 * time.Second
	opts.ServerAliveInterval = 200 * time.Millisecond
	opts.ServerAliveCountMax = 2

	localPort := freeTCPPort(t)
	spec := fmt.Sprintf("%d:127.0.0.1:%s", localPort, echoPortStr)

	sigCh := make(chan os.Signal, 1)
	exitCh := make(chan int, 1)
	go func() {
		exitCh <- runForwardOnly(opts, []string{spec}, nil, io.Discard, sigCh, true, connect)
	}()

	// 首次连接后转发可用
	waitEcho(t, localPort, 5*time.Second)

	// 链路中断：服务器断开所有连接（进程仍在，但传输层已死）
	srv.Kill()

	// 网络/服务器恢复：新实例（模拟服务器重启后重新可达）
	srv2 := newTunnelTestServer(t)
	mu.Lock()
	cur = srv2
	mu.Unlock()

	waitForCond(t, 10*time.Second, "reconnect attempt", func() bool {
		mu.Lock()
		defer mu.Unlock()
		return connects >= 2
	})
	waitEcho(t, localPort, 10*time.Second)

	// 信号退出：正常返回并释放本地监听端口
	sigCh <- os.Interrupt
	select {
	case code := <-exitCh:
		if code != 0 {
			t.Fatalf("exit code = %d, want 0", code)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("runForwardOnly did not exit after signal")
	}

	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", localPort))
	if err != nil {
		t.Fatalf("local forward port not released after shutdown: %v", err)
	}
	_ = ln.Close()
}

// TestRunForwardOnlyFatalConnectErrorExits 验证凭据/主机密钥类错误不做无意义重试。
func TestRunForwardOnlyFatalConnectErrorExits(t *testing.T) {
	speedUpTunnelBackoff(t)

	connect := func(*sshclient.Options, io.Writer) (*sshclient.Client, error) {
		return nil, fmt.Errorf("authentication failed: 127.0.0.1:22 (check username, password, or key)")
	}

	sigCh := make(chan os.Signal, 1)
	start := time.Now()
	code := runForwardOnly(sshclient.Defaults(), nil, nil, io.Discard, sigCh, true, connect)
	if code != 255 {
		t.Fatalf("exit code = %d, want 255", code)
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("fatal connect error must not be retried forever: elapsed=%v", elapsed)
	}
}

// TestRunForwardOnlyNoReconnectExitsOnTransportLoss 验证 --no-reconnect 语义：
// 链路中断时交给外部监督进程（退出码 255），而不是自行重连。
func TestRunForwardOnlyNoReconnectExitsOnTransportLoss(t *testing.T) {
	speedUpTunnelBackoff(t)

	srv := newTunnelTestServer(t)

	var mu sync.Mutex
	connects := 0
	connect := func(opts *sshclient.Options, stderr io.Writer) (*sshclient.Client, error) {
		mu.Lock()
		connects++
		mu.Unlock()
		opts.Host = srv.host
		opts.Port = srv.port
		return sshclient.NewClient(opts, stderr)
	}

	opts := sshclient.Defaults()
	opts.StrictHostKeyChecking = sshclient.StrictModeNo
	opts.Password = "pw"
	opts.PasswordSet = true
	opts.ConnectTimeout = 5 * time.Second
	opts.ServerAliveInterval = 200 * time.Millisecond
	opts.ServerAliveCountMax = 2

	sigCh := make(chan os.Signal, 1)
	exitCh := make(chan int, 1)
	go func() {
		exitCh <- runForwardOnly(opts, nil, nil, io.Discard, sigCh, false, connect)
	}()

	waitForCond(t, 5*time.Second, "initial connection", func() bool {
		mu.Lock()
		defer mu.Unlock()
		return connects >= 1
	})

	srv.Kill()

	select {
	case code := <-exitCh:
		if code != 255 {
			t.Fatalf("exit code = %d, want 255 with reconnect disabled", code)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("runForwardOnly kept running after transport loss with reconnect disabled")
	}
}
