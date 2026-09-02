package sshclient

import (
	"errors"
	"fmt"
	"io"
	"net"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// 本文件实现 OpenSSH ProxyCommand 指令：把外部命令的子进程 stdin/stdout 包装成
// net.Conn，SSH 握手与后续会话都走该连接。
//
// 与 OpenSSH 的差异与取舍（详见 docs/analysis/openssh-proxycommand-implementation.md）：
//   - OpenSSH（Unix）为 ProxyCommand 前置 "exec " 并由 /bin/sh 分词执行；
//     Win32-OpenSSH 用 CreateProcess 直接执行、无 shell。本实现同样不经过
//     cmd.exe/shell，采用 Win32 CreateProcess 风格分词（双引号分组、空格分隔）。
//   - ProxyUseFdpass（fd 回传）在 Windows 上 OpenSSH 亦不支持，本实现不支持。
//   - 连接超时（ConnectTimeout）通过对 proxyConn.SetDeadline 的 timer 到期后
//     终止子进程来实现，与直连路径的握手 deadline 语义一致。

// expandProxyCommand 按 OpenSSH expand_proxy_command 的令牌语义展开命令：
//
//	%h → host        （HostName 解析后的目标主机）
//	%n → alias       （命令行/配置中的原始别名，与 %k 一致；未设置 HostKeyAlias 时）
//	%k → alias       （HostKeyAlias；本实现未支持 HostKeyAlias，等价 %n）
//	%p → port        （目标端口字符串）
//	%r → user        （登录用户名）
//	%% → 字面 %
//
// 未知的 %x 序列保持原样输出（与 OpenSSH percent_expand 一致）。
func expandProxyCommand(cmd, host, alias, port, user string) string {
	var sb strings.Builder
	sb.Grow(len(cmd) + 16)
	for i := 0; i < len(cmd); i++ {
		ch := cmd[i]
		if ch == '%' && i+1 < len(cmd) {
			switch next := cmd[i+1]; next {
			case 'h':
				sb.WriteString(host)
				i++
				continue
			case 'k', 'n':
				sb.WriteString(alias)
				i++
				continue
			case 'p':
				sb.WriteString(port)
				i++
				continue
			case 'r':
				sb.WriteString(user)
				i++
				continue
			case '%':
				sb.WriteByte('%')
				i++
				continue
			}
		}
		sb.WriteByte(ch)
	}
	return sb.String()
}

// splitCommandLine 将命令字符串拆分为参数列表，采用 Win32 CreateProcess /
// CommandLineToArgvW 的核心语义（与 Win32-OpenSSH 经 CreateProcess 执行
// ProxyCommand 的方式一致）：
//   - 空白（空格/Tab）分隔参数；
//   - 双引号括起的片段视为一个整体（成对出现，最终不保留引号）；
//   - 允许参数中出现引号与空格的组合，例如
//     `"C:/Program Files/Git/mingw64/bin/connect.exe" -H 127.0.0.1:10810 %h %p`。
//
// 不经过 cmd.exe/shell，因此不会展开环境变量、通配符或重定向。
func splitCommandLine(cmd string) ([]string, error) {
	var args []string
	var cur strings.Builder
	inQuote := false
	hasToken := false

	for i := 0; i < len(cmd); i++ {
		ch := cmd[i]
		switch {
		case ch == '"':
			inQuote = !inQuote
			hasToken = true
		case (ch == ' ' || ch == '\t') && !inQuote:
			if hasToken {
				args = append(args, cur.String())
				cur.Reset()
				hasToken = false
			}
		default:
			cur.WriteByte(ch)
			hasToken = true
		}
	}
	if inQuote {
		return nil, errors.New("unterminated double quote in proxy command")
	}
	if hasToken {
		args = append(args, cur.String())
	}
	if len(args) == 0 {
		return nil, errors.New("empty proxy command")
	}
	return args, nil
}

// startProxyCommand 解析并启动 ProxyCommand 子进程，返回包装子进程
// stdin/stdout 的 net.Conn。连接建立前不进行任何 DNS 解析（与 OpenSSH 一致：
// 目标地址只作为令牌传给代理命令）。
func startProxyCommand(opts *Options, stderr io.Writer) (net.Conn, error) {
	host := opts.Host
	alias := opts.OriginalHost
	if alias == "" {
		alias = host
	}
	port := fmt.Sprintf("%d", opts.Port)
	user := opts.User
	if user == "" {
		user = "root"
	}

	expanded := expandProxyCommand(opts.ProxyCommand, host, alias, port, user)
	args, err := splitCommandLine(expanded)
	if err != nil {
		return nil, fmt.Errorf("proxy command %q: %w", opts.ProxyCommand, err)
	}

	if opts.Verbose {
		fmt.Fprintf(stderr, "ssh-client: proxy command: %s\n", strings.Join(args, " "))
	}

	cmd := exec.Command(args[0], args[1:]...)
	// 继承父进程 stderr：connect.exe 等工具的错误信息直接展示给用户，
	// 且避免子进程 stderr 写满管道导致阻塞。
	cmd.Stderr = stderr

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("proxy command: stdin pipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("proxy command: stdout pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("proxy command %q: %w", args[0], err)
	}

	waitDone := make(chan struct{})
	go func() {
		// 收尸：进程退出后通知。Close() 中 Kill 后再 <-waitDone 等待。
		_ = cmd.Wait()
		close(waitDone)
	}()

	// 逻辑地址：known_hosts 校验等依赖 RemoteAddr() 能解析出 host:port。
	remoteAddr := &net.TCPAddr{IP: net.ParseIP(host), Port: opts.Port}
	if remoteAddr.IP == nil {
		// host 可能是域名（host key 校验用原始字符串即可，端口单独保留）
		remoteAddr = &net.TCPAddr{IP: net.IPv4zero, Port: opts.Port}
	}

	return &proxyConn{
		stdin:      stdin,
		stdout:     stdout,
		cmd:        cmd,
		remoteAddr: remoteAddr,
		localAddr:  &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0},
		done:       make(chan struct{}),
		waitDone:   waitDone,
	}, nil
}

// proxyConn 将子进程的 stdin/stdout 管道包装为 net.Conn，供
// x/crypto/ssh 的 NewClientConn 使用。
type proxyConn struct {
	stdin  io.WriteCloser // 子进程 stdin（父进程写端）
	stdout io.ReadCloser  // 子进程 stdout（父进程读端）
	cmd    *exec.Cmd

	remoteAddr net.Addr // 目标主机地址（用于 host key 校验）
	localAddr  net.Addr // 本地占位地址

	done     chan struct{} // Close 后关闭，用于终止 deadline timer 回调
	waitDone chan struct{} // 进程退出后关闭

	closeOnce sync.Once
	mu        sync.Mutex
	timer     *time.Timer
}

// Read 从子进程 stdout 读取（SSH 下行数据）。
func (c *proxyConn) Read(p []byte) (int, error) { return c.stdout.Read(p) }

// Write 向子进程 stdin 写入（SSH 上行数据）。
func (c *proxyConn) Write(p []byte) (int, error) { return c.stdin.Write(p) }

// Close 终止代理子进程并关闭管道，可安全重复调用。
func (c *proxyConn) Close() error {
	c.closeOnce.Do(func() {
		c.mu.Lock()
		if c.timer != nil {
			c.timer.Stop()
			c.timer = nil
		}
		c.mu.Unlock()
		close(c.done)

		// 先终止子进程：其持有的管道句柄随之关闭，阻塞中的父进程
		// 管道 Read/Write 立即返回（断管），保证 x/crypto 握手 goroutine 能退出。
		if c.cmd != nil && c.cmd.Process != nil {
			_ = c.cmd.Process.Kill()
		}
		// 再关闭父进程侧管道句柄，释放资源。
		if c.stdin != nil {
			_ = c.stdin.Close()
		}
		if c.stdout != nil {
			_ = c.stdout.Close()
		}
		// 等待收尸 goroutine 结束（Kill 后 Wait 应立即返回）。
		select {
		case <-c.waitDone:
		case <-time.After(3 * time.Second):
		}
	})
	return nil
}

// LocalAddr 返回本地占位地址（子进程管道没有真实的本地地址）。
func (c *proxyConn) LocalAddr() net.Addr {
	if c.localAddr != nil {
		return c.localAddr
	}
	return proxyAddr{}
}

// RemoteAddr 返回目标主机地址，用于 host key 校验等场景（与直连路径的远程地址一致）。
func (c *proxyConn) RemoteAddr() net.Addr {
	if c.remoteAddr != nil {
		return c.remoteAddr
	}
	return proxyAddr{}
}

// SetDeadline 以 timer 到期后 Close() 的方式模拟超时中断。
// 管道上的阻塞 Read/Write 无法被直接打断，因此超时意味着终止整个连接
// （OpenSSH 在连接超时/中断时同样会结束会话）。传入零值清除超时。
func (c *proxyConn) SetDeadline(t time.Time) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.timer != nil {
		c.timer.Stop()
		c.timer = nil
	}
	if t.IsZero() {
		return nil
	}
	d := time.Until(t)
	if d <= 0 {
		// 已过期：立即关闭（异步，避免在锁内触发 Close 的重入）。
		go c.Close()
		return nil
	}
	c.timer = time.AfterFunc(d, func() {
		select {
		case <-c.done:
			// 连接已关闭，无需处理
		default:
			c.Close()
		}
	})
	return nil
}

// SetReadDeadline 与 SetWriteDeadline 共用同一 deadline（x/crypto 握手期主要
// 调用 SetDeadline；读写 deadline 的独立语义对管道无意义）。
func (c *proxyConn) SetReadDeadline(t time.Time) error  { return c.SetDeadline(t) }
func (c *proxyConn) SetWriteDeadline(t time.Time) error { return c.SetDeadline(t) }

// proxyAddr 实现 net.Addr 占位。
type proxyAddr struct{}

func (proxyAddr) Network() string { return "proxy" }
func (proxyAddr) String() string  { return "proxy" }
