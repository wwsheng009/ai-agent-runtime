package sshclient

import (
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"os/user"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
)

// defaultDialTimeout 是未显式指定 ConnectTimeout 时的连接超时。
const defaultDialTimeout = 30 * time.Second

// Client 封装一个已建立的 SSH 连接。
type Client struct {
	sshClient *ssh.Client
	opts      *Options
	stopCh    chan struct{} // 非 nil 时用于停止 keepAlive goroutine
}

// NewClient 建立到目标主机的 SSH 连接。
// opts.Host 支持 "host"、"host:port"、"user@host[:port]" 形式；
// 显式的 opts.User 优先于 user@host 中的 user。
func NewClient(opts *Options, stderr io.Writer) (*Client, error) {
	if opts == nil {
		return nil, fmt.Errorf("nil options")
	}
	globalVerbose = opts.Verbose

	host, port, user, err := resolveTarget(opts)
	if err != nil {
		return nil, err
	}
	addr := net.JoinHostPort(host, strconv.Itoa(port))

	// 认证方法
	authMethods, err := BuildAuthMethods(opts, stderr)
	if err != nil {
		return nil, err
	}

	// host key 校验
	hostKeyCallback, err := BuildHostKeyCallback(opts, stderr)
	if err != nil {
		return nil, err
	}

	config := &ssh.ClientConfig{
		User:            user,
		Auth:            authMethods,
		HostKeyCallback: hostKeyCallback,
		Timeout:         opts.ConnectTimeout,
		ClientVersion:   "SSH-2.0-ssh-client-go",
	}
	if len(opts.HostKeyAlgorithms) > 0 {
		config.HostKeyAlgorithms = opts.HostKeyAlgorithms
	}

	timeout := opts.ConnectTimeout
	if timeout <= 0 {
		timeout = defaultDialTimeout
	}

	var conn net.Conn
	if opts.ProxyCommand != "" {
		// ProxyCommand 模式：不进行 DNS 解析与 TCP 拨号，由外部命令
		// （如 connect.exe）负责建立到目标主机的连接；目标地址只作为
		// %h/%p 令牌传给代理命令（与 OpenSSH 一致）。
		conn, err = startProxyCommand(opts, stderr)
		if err != nil {
			return nil, fmt.Errorf("proxy command: %w", err)
		}
	} else {
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()

		network := opts.Network
		if network == "" {
			network = "tcp"
		}
		dialer := net.Dialer{Timeout: timeout}
		conn, err = dialer.DialContext(ctx, network, addr)
		if err != nil {
			return nil, fmt.Errorf("connect to %s: %w", addr, err)
		}
	}

	// 关键：SSH 握手（KEX + 认证）同样受 timeout 约束。
	// x/crypto/ssh 的 ClientConfig.Timeout 只作用于 ssh.Dial 内部的 TCP 拨号；
	// 此处是手动拨号后传入 NewClientConn，若服务器 accept TCP 后不回包（半开连接、
	// docker-proxy 转发但容器内 sshd 未就绪等），握手会永久阻塞。
	// 因此用 conn deadline 包住整个握手，超时后 NewClientConn 返回 i/o timeout。
	// ProxyCommand 的 proxyConn 以 timer→Close() 模拟 deadline（见 proxy.go）。
	if err := conn.SetDeadline(time.Now().Add(timeout)); err != nil {
		conn.Close()
		return nil, fmt.Errorf("set deadline on connection to %s: %w", addr, err)
	}
	sshConn, chans, reqs, err := ssh.NewClientConn(conn, addr, config)
	// 握手结束（无论成败）清除 deadline，避免影响后续会话 I/O。
	_ = conn.SetDeadline(time.Time{})
	if err != nil {
		conn.Close()
		// x/crypto/ssh 在 PermitRootLogin no + keyboard-interactive fallback 等场景下，
		// 服务器对 keyboard-interactive 请求直接回复 SSH_MSG_USERAUTH_FAILURE (51)
		// 而不是预期的 SSH_MSG_USERAUTH_INFO_REQUEST (60)，x/crypto 将其视为协议错误
		// （unexpected message type 51 (expected 60)），对用户不友好。
		// 此处将这种协议级内部错误归一化为友好的认证失败信息。
		errMsg := err.Error()
		if strings.Contains(errMsg, "unexpected message type") {
			return nil, fmt.Errorf("authentication failed: %s (check username, password, or key)", addr)
		}
		return nil, fmt.Errorf("ssh handshake with %s: %w", addr, err)
	}

	client := ssh.NewClient(sshConn, chans, reqs)

	// 启动保活
	var stopCh chan struct{}
	if opts.ServerAliveInterval > 0 {
		stopCh = make(chan struct{})
		go keepAlive(client, opts.ServerAliveInterval, opts.ServerAliveCountMax, stopCh)
	}

	return &Client{sshClient: client, opts: opts, stopCh: stopCh}, nil
}

// SSHSession 返回底层 ssh.Client（供子命令使用）。
func (c *Client) SSHSession() *ssh.Client {
	return c.sshClient
}

// Close 关闭 SSH 连接。
func (c *Client) Close() error {
	// 先停保活 goroutine，避免其持有已关闭的连接或重复 Close。
	if c.stopCh != nil {
		close(c.stopCh)
		c.stopCh = nil
	}
	if c.sshClient != nil {
		err := c.sshClient.Close()
		c.sshClient = nil // 幂等：重复 Close 返回 nil
		return err
	}
	return nil
}

// Options 返回连接使用的选项。
func (c *Client) Options() *Options { return c.opts }

// resolveTarget 解析目标主机、端口与用户名。
func resolveTarget(opts *Options) (host string, port int, user string, err error) {
	host = opts.Host
	if host == "" {
		return "", 0, "", fmt.Errorf("empty host")
	}

	// 提取 user@host[:port]
	user = opts.User
	if at := strings.LastIndex(host, "@"); at >= 0 {
		embeddedUser := host[:at]
		host = host[at+1:]
		if user == "" {
			user = embeddedUser
		}
	}

	// 处理 host:port 或 [ipv6]:port
	if h, p, perr := net.SplitHostPort(host); perr == nil {
		host = h
		if n, aerr := strconv.Atoi(p); aerr == nil && n > 0 && n <= 65535 {
			port = n
		}
	} else if strings.HasPrefix(host, "[") && strings.Contains(host, "]") {
		// [ipv6] 无端口
		host = strings.TrimSuffix(strings.TrimPrefix(host, "["), "]")
	}

	if port == 0 {
		port = opts.Port
	}
	if port == 0 {
		port = 22
	}

	if user == "" {
		user = defaultUsername()
	}
	return host, port, user, nil
}

// defaultUsername 返回当前系统用户名。
func defaultUsername() string {
	if u, err := user.Current(); err == nil && u.Username != "" {
		// Windows 上 Username 可能是 DOMAIN\user，取最后一段
		if idx := strings.LastIndex(u.Username, "\\"); idx >= 0 {
			return u.Username[idx+1:]
		}
		return u.Username
	}
	if v := os.Getenv("USER"); v != "" {
		return v
	}
	if v := os.Getenv("USERNAME"); v != "" {
		return v
	}
	return "root"
}

// keepAlive 周期性发送 keepalive 请求，检测死连接；连续失败超过 countMax 次后关闭连接。
func keepAlive(client *ssh.Client, interval time.Duration, countMax int, stop <-chan struct{}) {
	if countMax <= 0 {
		countMax = 3
	}
	failures := 0
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			_, _, err := client.SendRequest("keepalive@openssh.com", true, nil)
			if err != nil {
				failures++
				if failures >= countMax {
					client.Close()
					return
				}
			} else {
				failures = 0
			}
		}
	}
}