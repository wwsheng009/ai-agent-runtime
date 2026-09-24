package main

// 本文件实现 -L/-R 端口转发，以及 -N（仅转发）模式的传输层监督：
//
//   - forwardHandle 让监听端口可被关闭，并在链路重建后重新绑定；
//   - pumpConns 双向转发，任一端结束后关闭两端，避免半开连接泄漏 fd；
//   - runForwardOnly 在传输层死亡（网络中断 / 服务器重启 / 保活判定死链）后
//     自动重连并恢复全部转发，而不是留下「端口仍在监听、转发已失效」的僵尸进程。

import (
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/wwsheng009/ai-agent-runtime/internal/sshclient"
)

// 隧道监督参数。声明为变量以便测试压缩等待时间。
var (
	// tunnelKeepAliveInterval 是 -N 模式下未显式配置 ServerAliveInterval 时的保活间隔：
	// 缺少保活时静默死链（NAT/防火墙回收、服务器重启）无法被及时感知。
	tunnelKeepAliveInterval = 15 * time.Second
	tunnelBackoffBase       = 1 * time.Second
	tunnelBackoffMax        = 30 * time.Second
)

// connectFunc 建立一个新的 SSH 连接；可注入，便于测试重连语义。
type connectFunc func(opts *sshclient.Options, stderr io.Writer) (*sshclient.Client, error)

// defaultConnect 是生产环境的连接函数。
func defaultConnect(opts *sshclient.Options, stderr io.Writer) (*sshclient.Client, error) {
	return sshclient.NewClient(opts, stderr)
}

// forwardHandle 表示一个已建立的转发监听。
type forwardHandle struct {
	name string
	ln   net.Listener
}

// Close 停止监听并释放端口；已接受的连接由 pumpConns 自行收尾。
func (h *forwardHandle) Close() {
	if h == nil || h.ln == nil {
		return
	}
	_ = h.ln.Close()
}

// startForwards 建立全部 -L/-R 转发。
// fatalCh 用于上报监听器意外失效（非阻塞发送，可为 nil）；
// exitOnFailure 为 false 时单个转发失败只告警并继续（对应 -o ExitOnForwardFailure=no）。
func startForwards(client *ssh.Client, localSpecs, remoteSpecs []string, fatalCh chan<- error, exitOnFailure bool, stderr io.Writer) ([]*forwardHandle, error) {
	handles := make([]*forwardHandle, 0, len(localSpecs)+len(remoteSpecs))
	abort := func(format string, args ...any) ([]*forwardHandle, error) {
		closeForwards(handles)
		return nil, fmt.Errorf(format, args...)
	}

	for _, spec := range localSpecs {
		h, err := startLocalForward(client, spec, fatalCh, stderr)
		if err != nil {
			if !exitOnFailure {
				fmt.Fprintf(stderr, "ssh-client: warning: local forward %s: %v\n", spec, err)
				continue
			}
			return abort("local forward %s: %v", spec, err)
		}
		handles = append(handles, h)
	}
	for _, spec := range remoteSpecs {
		h, err := startRemoteForward(client, spec, fatalCh, stderr)
		if err != nil {
			if !exitOnFailure {
				fmt.Fprintf(stderr, "ssh-client: warning: remote forward %s: %v\n", spec, err)
				continue
			}
			return abort("remote forward %s: %v", spec, err)
		}
		handles = append(handles, h)
	}
	return handles, nil
}

// closeForwards 关闭全部转发监听（幂等）。
func closeForwards(handles []*forwardHandle) {
	for _, h := range handles {
		h.Close()
	}
}

// parseForwardSpec 解析 OpenSSH 兼容的转发规格：
//
//	[bind_address:]port:host:hostport
//
// bind_address 可省略（默认 localhost），可为 IPv4（如 192.168.1.1）、
// 带方括号的 IPv6（如 [::1]）或 "*"（所有接口）。host 也可以是带方括号的
// IPv6（如 [::1]）。解析从右向左进行，方括号内的冒号不会被当作分隔符。
func parseForwardSpec(spec string) (bind, port, host, hostport string, err error) {
	bind = "localhost" // OpenSSH 默认绑定回环地址
	invalid := func() error {
		return fmt.Errorf("invalid forward spec %q (expected [bind:]port:host:hostport)", spec)
	}

	// 1. 末尾 hostport 必须紧跟最后一段，且为数字
	lastColon := strings.LastIndex(spec, ":")
	if lastColon < 0 {
		return "", "", "", "", invalid()
	}
	hostport = spec[lastColon+1:]
	if _, err := strconv.Atoi(hostport); err != nil {
		return "", "", "", "", invalid()
	}
	rest := spec[:lastColon]

	// 2. 提取 host：若 rest 以 "]" 结尾则 host 是带方括号的 IPv6
	var head string
	if strings.HasSuffix(rest, "]") {
		open := strings.LastIndex(rest, "[")
		if open < 0 {
			return "", "", "", "", invalid()
		}
		host = rest[open:]
		head = strings.TrimSuffix(rest[:open], ":")
	} else {
		hc := strings.LastIndex(rest, ":")
		if hc < 0 {
			return "", "", "", "", invalid()
		}
		host = rest[hc+1:]
		head = rest[:hc]
	}
	if host == "" {
		return "", "", "", "", invalid()
	}

	// 3. head 为 "port" 或 "bind:port"
	if pc := strings.LastIndex(head, ":"); pc >= 0 {
		bind = head[:pc]
		port = head[pc+1:]
	} else {
		port = head
	}
	if port == "" {
		return "", "", "", "", invalid()
	}

	// 4. 规范化 bind：去掉 IPv6 方括号；"*"/"" 表示所有接口
	bind = strings.Trim(bind, "[]")
	if bind == "*" {
		bind = ""
	}
	return bind, port, host, hostport, nil
}

// startLocalForward 启动本地端口转发（-L）：
// 本地 listener 接受连接后，经 SSH 打开到 remoteAddr 的 direct-tcpip 通道。
func startLocalForward(client *ssh.Client, spec string, fatalCh chan<- error, stderr io.Writer) (*forwardHandle, error) {
	bind, portStr, remoteHost, remotePortStr, err := parseForwardSpec(spec)
	if err != nil {
		return nil, err
	}

	localAddr := net.JoinHostPort(bind, portStr)
	remoteAddr := net.JoinHostPort(remoteHost, remotePortStr)

	listener, err := net.Listen("tcp", localAddr)
	if err != nil {
		return nil, fmt.Errorf("listen on %s: %w", localAddr, err)
	}

	h := &forwardHandle{name: fmt.Sprintf("local forward %s -> %s", localAddr, remoteAddr), ln: listener}
	go acceptForward(h, func() (net.Conn, error) {
		return client.Dial("tcp", remoteAddr)
	}, fatalCh, stderr)

	fmt.Fprintf(stderr, "ssh-client: %s\n", h.name)
	return h, nil
}

// startRemoteForward 启动远程端口转发（-R）：
// 远端 listener 接受连接后，本地拨号到 localAddr。
func startRemoteForward(client *ssh.Client, spec string, fatalCh chan<- error, stderr io.Writer) (*forwardHandle, error) {
	bind, portStr, localHost, localPortStr, err := parseForwardSpec(spec)
	if err != nil {
		return nil, err
	}

	remoteAddr := net.JoinHostPort(bind, portStr)
	localAddr := net.JoinHostPort(localHost, localPortStr)

	listener, err := client.Listen("tcp", remoteAddr)
	if err != nil {
		return nil, fmt.Errorf("remote listen on %s: %w", remoteAddr, err)
	}

	h := &forwardHandle{name: fmt.Sprintf("remote forward %s -> %s", remoteAddr, localAddr), ln: listener}
	go acceptForward(h, func() (net.Conn, error) {
		return net.Dial("tcp", localAddr)
	}, fatalCh, stderr)

	fmt.Fprintf(stderr, "ssh-client: %s\n", h.name)
	return h, nil
}

// acceptForward 接受连接并通过 dial 建立对端；监听器被 Close 后返回。
// 监听器意外失效（非本次 Close）时上报 fatalCh，交由监督循环决定重连。
func acceptForward(h *forwardHandle, dial func() (net.Conn, error), fatalCh chan<- error, stderr io.Writer) {
	for {
		localConn, err := h.ln.Accept()
		if err != nil {
			if !errors.Is(err, net.ErrClosed) {
				fmt.Fprintf(stderr, "ssh-client: warning: %s: accept: %v\n", h.name, err)
				if fatalCh != nil {
					select {
					case fatalCh <- fmt.Errorf("%s: accept: %w", h.name, err):
					default:
					}
				}
			}
			return
		}
		go func(local net.Conn) {
			remote, err := dial()
			if err != nil {
				// 传输层已死或远端拒绝：立即断开本地连接，
				// 让客户端快速失败并重试，而不是悬挂在半开状态。
				_ = local.Close()
				return
			}
			pumpConns(local, remote)
		}(localConn)
	}
}

// pumpConns 在两条连接之间双向拷贝：任一侧 EOF 后关闭对端写方向，
// 两个方向都结束后关闭两端，避免半开连接长期占用 fd。
func pumpConns(a, b net.Conn) {
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); _, _ = io.Copy(b, a); closeWriteConn(b) }()
	go func() { defer wg.Done(); _, _ = io.Copy(a, b); closeWriteConn(a) }()
	wg.Wait()
	_ = a.Close()
	_ = b.Close()
}

// closeWriteConn 尽力关闭写方向；不支持半关闭时退化为整体关闭。
func closeWriteConn(c net.Conn) {
	if cw, ok := c.(interface{ CloseWrite() error }); ok {
		_ = cw.CloseWrite()
		return
	}
	_ = c.Close()
}

// runForwardOnly 运行 -N（仅转发）模式：保持转发并监督传输层，链路死亡后自动重连。
// 返回进程退出码：
//   - 0   用户中断（SIGINT/SIGTERM），转发已关闭；
//   - 255 不可恢复的失败（认证/主机密钥/转发无法建立），或禁用重连时链路中断。
func runForwardOnly(opts *sshclient.Options, localSpecs, remoteSpecs []string, stderr io.Writer, sigCh <-chan os.Signal, reconnect bool, connect connectFunc) int {
	if connect == nil {
		connect = defaultConnect
	}
	if opts.ServerAliveInterval <= 0 {
		// 隧道默认开启保活，否则静默死链只能等到下一次真实流量才被发现。
		opts.ServerAliveInterval = tunnelKeepAliveInterval
	}

	attempt := 0
	backoff := tunnelBackoffBase

	// waitRetry 等待下一次重连；返回 false 表示收到信号，应正常退出。
	waitRetry := func(reason string) bool {
		attempt++
		fmt.Fprintf(stderr, "ssh-client: %s (reconnect attempt %d in %s)\n", reason, attempt, backoff)
		if waitForSignalOrTimeout(sigCh, backoff) {
			fmt.Fprintln(stderr, "ssh-client: forwarding closed")
			return false
		}
		backoff = nextBackoff(backoff)
		return true
	}

	for {
		client, err := connect(opts, stderr)
		if err != nil {
			if !reconnect || isFatalConnectError(err) {
				fmt.Fprintf(stderr, "ssh-client: connection failed: %v\n", err)
				return 255
			}
			if !waitRetry(fmt.Sprintf("connection failed: %v", err)) {
				return 0
			}
			continue
		}

		fatalCh := make(chan error, 1)
		handles, err := startForwards(client.SSHSession(), localSpecs, remoteSpecs, fatalCh, opts.ExitOnForwardFailure, stderr)
		if err != nil {
			fmt.Fprintf(stderr, "ssh-client: %v\n", err)
			_ = client.Close()
			return 255
		}

		if attempt > 0 {
			fmt.Fprintf(stderr, "ssh-client: reconnected, forwarding restored (attempt %d)\n", attempt)
		}
		attempt = 0
		backoff = tunnelBackoffBase

		// 监督传输层：Wait 返回即链路结束（无论对端断开还是本地保活判定死链）。
		downCh := make(chan error, 1)
		go func() { downCh <- client.Wait() }()

		var lostErr error
		select {
		case <-sigCh:
			closeForwards(handles)
			_ = client.Close()
			fmt.Fprintln(stderr, "ssh-client: forwarding closed")
			return 0
		case err := <-downCh:
			lostErr = err
		case err := <-fatalCh:
			lostErr = err
		}

		closeForwards(handles)
		_ = client.Close()

		if lostErr == nil {
			lostErr = errors.New("transport closed")
		}
		if !reconnect {
			fmt.Fprintf(stderr, "ssh-client: connection lost: %v\n", lostErr)
			return 255
		}
		if !waitRetry(fmt.Sprintf("connection lost: %v, retrying", lostErr)) {
			return 0
		}
	}
}

// nextBackoff 指数退避并封顶。
func nextBackoff(d time.Duration) time.Duration {
	d *= 2
	if d > tunnelBackoffMax {
		d = tunnelBackoffMax
	}
	return d
}

// waitForSignalOrTimeout 等待 d；期间收到中断信号返回 true。
func waitForSignalOrTimeout(sigCh <-chan os.Signal, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-sigCh:
		return true
	case <-t.C:
		return false
	}
}

// isFatalConnectError 判断连接错误是否属于「重试也不会好转」的配置/凭据问题：
// 认证失败、主机密钥不匹配等重连无意义，直接退出交由人工处理。
func isFatalConnectError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	for _, marker := range []string{
		"authentication failed",
		"unable to authenticate",
		"no supported methods",
		"host key mismatch",
		"host key verification failed",
	} {
		if strings.Contains(msg, marker) {
			return true
		}
	}
	return false
}
