package main

import (
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/pflag"
	"golang.org/x/crypto/ssh"
	"golang.org/x/term"

	"github.com/wwsheng009/ai-agent-runtime/internal/sshclient"
	"github.com/wwsheng009/ai-agent-runtime/internal/winconsole"
)

// version 通过构建参数 -X main.version=<v> 注入（例如 Win7 构建脚本）。
var version = "0.1.0"

// CLI 参数
type cliFlags struct {
	port          int
	user          string
	identityFiles []string
	password      string
	passwordSet   bool
	options       []string
	quiet         bool
	verbose       bool
	configFile    string
	noSession     bool
	localForwards []string
	remoteForwards []string
	noTty         bool
	forceTty      bool
	showVersion   bool
	ipv4          bool
	ipv6          bool
	compress      bool
	timeout       int
	knownHostsFile string
	showHelp      bool
	// 目标
	host    string
	command []string
}

func main() {
	// Win7 及更早 conhost 无 VT 处理：先把控制台输出代码页切到 UTF-8，
	// 否则远程命令回显等非 ASCII 输出会被按 OEM 代码页（如 GBK）解码成乱码。
	// 支持 VT 的控制台、管道/文件重定向、非 Windows 平台均为空操作。
	if restore := winconsole.EnsureConsoleUTF8Output(); restore != nil {
		defer restore()
	}

	// 解析参数
	flags := parseFlags()

	// 退出码
	exitCode := run(flags)
	os.Exit(exitCode)
}

func parseFlags() *cliFlags {
	flags := &cliFlags{}

	fs := pflag.NewFlagSet("ssh-client", pflag.ContinueOnError)
	fs.Usage = func() {
		fmt.Fprintf(fs.Output(), `Usage: ssh-client [options] [user@]host [command]

OpenSSH-compatible SSH client: interactive remote shell, remote command execution,
local/remote port forwarding.

Options:
`)
		fs.PrintDefaults()
		fmt.Fprintf(fs.Output(), `
Examples:
  # Interactive shell
  ssh-client user@example.com

  # Remote command with key auth on a custom port
  ssh-client -i ~/.ssh/id_ed25519 -p 2222 user@host "ls -la"

  # Password auth (non-interactive)
  ssh-client --password 'secret' user@host "uptime"

  # Local port forwarding, no remote session (-N)
  ssh-client -N -L 8080:localhost:80 user@host

  # Remote port forwarding
  ssh-client -N -R 2222:localhost:22 user@host

  # Pipe mode: stdin is forwarded to the remote command
  echo 'uname -a' | ssh-client user@host

Notes:
  - Authentication order: publickey -> ssh-agent -> password -> keyboard-interactive.
  - Connection setup (TCP dial + SSH handshake) is bounded by ConnectTimeout
    (default 30s; use --timeout N or -o ConnectTimeout=N to change). A server that
    accepts TCP but never completes the handshake will time out instead of hanging.
  - Dead-link detection: -o ServerAliveInterval=15 -o ServerAliveCountMax=3.
  - Host key verification: -o StrictHostKeyChecking=yes|accept-new|no (default accept-new).
  - ProxyCommand (via config file) is supported; ProxyJump is parsed but not implemented.
`)
	}

	fs.IntVarP(&flags.port, "port", "p", 0, "SSH port (default 22)")
	fs.StringVarP(&flags.user, "user", "l", "", "Login username")
	fs.StringArrayVarP(&flags.identityFiles, "identity-file", "i", nil, "Identity file path (can be repeated)")
	fs.StringVar(&flags.password, "password", "", "Password (no short option; interactive if omitted)")
	fs.StringArrayVarP(&flags.options, "option", "o", nil, "OpenSSH config option (key=value or 'key value')")
	fs.BoolVarP(&flags.quiet, "quiet", "q", false, "Quiet mode (suppress warnings/banners)")
	fs.BoolVarP(&flags.verbose, "verbose", "v", false, "Verbose output (debug)")
	fs.StringVarP(&flags.configFile, "config-file", "F", "", "ssh_config file path (default ~/.ssh/config)")
	fs.BoolVarP(&flags.noSession, "no-session", "N", false, "Do not execute remote command (forwarding only)")
	fs.StringArrayVarP(&flags.localForwards, "local-forward", "L", nil, "Local port forwarding ([bind:]port:host:hostport; bind defaults to localhost)")
	fs.StringArrayVarP(&flags.remoteForwards, "remote-forward", "R", nil, "Remote port forwarding ([bind:]port:host:hostport; bind defaults to localhost)")
	fs.BoolVarP(&flags.noTty, "no-tty", "T", false, "Disable pseudo-terminal allocation")
	fs.BoolVarP(&flags.forceTty, "tty", "t", false, "Force pseudo-terminal allocation")
	fs.BoolVarP(&flags.showVersion, "version", "V", false, "Show version")
	fs.BoolVarP(&flags.ipv4, "ipv4", "4", false, "Use IPv4 only")
	fs.BoolVarP(&flags.ipv6, "ipv6", "6", false, "Use IPv6 only")
	fs.BoolVarP(&flags.compress, "compress", "C", false, "Enable compression")
	fs.IntVar(&flags.timeout, "timeout", 0, "Connection timeout (seconds)")
	fs.StringVar(&flags.knownHostsFile, "known-hosts-file", "", "known_hosts file path")
	fs.BoolVarP(&flags.showHelp, "help", "h", false, "Show this help message and exit")

	// 解析
	if err := fs.Parse(os.Args[1:]); err != nil {
		// -h/--help 未注册为显式 flag 时，pflag 会返回 ErrHelp
		if err == pflag.ErrHelp {
			os.Exit(0)
		}
		fmt.Fprintln(os.Stderr, "ssh-client:", err)
		fs.Usage()
		os.Exit(255)
	}
	if flags.showHelp {
		fs.Usage()
		os.Exit(0)
	}
	// 版本号优先于目标参数校验（-V 无需 host）
	if flags.showVersion {
		fmt.Fprintf(os.Stderr, "ssh-client version %s\n", version)
		os.Exit(0)
	}

	// 标记密码是否显式设置
	if f := fs.Lookup("password"); f != nil && f.Changed {
		flags.passwordSet = true
	}

	// 剩余参数：host [command...]
	args := fs.Args()
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "ssh-client: missing host")
		fs.Usage()
		os.Exit(255)
	}
	flags.host = args[0]
	flags.command = args[1:]

	return flags
}

// splitTarget 拆分 [user@]host[:port] 或 [user@][ipv6]:port。
func splitTarget(spec string) (host, user string, port int) {
	if at := strings.LastIndex(spec, "@"); at >= 0 {
		user = spec[:at]
		spec = spec[at+1:]
	}
	if strings.HasPrefix(spec, "[") {
		// [ipv6] 或 [ipv6]:port
		if idx := strings.Index(spec, "]"); idx >= 0 {
			host = spec[1:idx]
			rest := spec[idx+1:]
			if strings.HasPrefix(rest, ":") {
				if n, err := strconv.Atoi(strings.TrimPrefix(rest, ":")); err == nil {
					port = n
				}
			}
			return host, user, port
		}
		host = strings.Trim(spec, "[]")
		return host, user, port
	}
	if idx := strings.LastIndex(spec, ":"); idx >= 0 {
		host = spec[:idx]
		if n, err := strconv.Atoi(spec[idx+1:]); err == nil {
			port = n
		}
	} else {
		host = spec
	}
	return host, user, port
}

func run(flags *cliFlags) int {
	// 先拆分 [user@]host[:port]，用纯 host 做 ssh_config alias 匹配
	hostOnly, user, port := splitTarget(flags.host)

	// 1. 构建 Options
	opts := sshclient.Defaults()
	opts.Verbose = flags.verbose
	opts.Quiet = flags.quiet
	opts.Host = hostOnly
	opts.ConfigFile = flags.configFile

	// 应用 CLI 显式设置
	if port > 0 {
		opts.Port = port
	} else if flags.port > 0 {
		opts.Port = flags.port
	}
	if user != "" {
		opts.User = user
	}
	if flags.user != "" {
		opts.User = flags.user
	}
	if len(flags.identityFiles) > 0 {
		opts.IdentityFiles = flags.identityFiles
	}
	if flags.passwordSet {
		opts.Password = flags.password
		opts.PasswordSet = true
	}
	if flags.compress {
		opts.Compression = true
	}
	if flags.timeout > 0 {
		opts.ConnectTimeout = time.Duration(flags.timeout) * time.Second
	}
	if flags.knownHostsFile != "" {
		opts.UserKnownHostsFile = flags.knownHostsFile
	}
	if flags.ipv4 {
		opts.Network = "tcp4"
	} else if flags.ipv6 {
		opts.Network = "tcp6"
	}

	// 应用 -o 选项
	if err := applyOptions(opts, flags.options); err != nil {
		fmt.Fprintf(os.Stderr, "ssh-client: %v\n", err)
		return 255
	}

	// 2. 加载 ssh_config（文件不存在则空配置，不报错）
	cfg, err := sshclient.LoadResolvedConfig(opts.ConfigFile, opts.Host)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ssh-client: warning: %v\n", err)
	} else {
		// 保留原始别名（供 ProxyCommand 的 %n 令牌展开），再应用 config 覆盖。
		opts.OriginalHost = opts.Host
		opts.ApplyConfig(cfg)
		if !opts.Quiet && cfg.ProxyJump != "" {
			fmt.Fprintf(os.Stderr, "ssh-client: warning: ProxyJump %q is not implemented; ignored\n", cfg.ProxyJump)
		}
		if opts.Verbose {
			fmt.Fprintf(os.Stderr, "ssh-client: using config host %q -> %s:%d\n", cfg.Host, cfg.HostName, cfg.Port)
		}
	}

	// 3. 如果密码未设置但 stdin 是终端，打印横幅
	if !opts.Quiet && term.IsTerminal(int(os.Stdin.Fd())) {
		fmt.Fprintf(os.Stderr, "ssh-client: connecting to %s (port %d)...\n", opts.Host, opts.Port)
	}

	// 4. 建立连接
	client, err := sshclient.NewClient(opts, os.Stderr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ssh-client: connection failed: %v\n", err)
		return 255
	}
	defer client.Close()

	sshConn := client.SSHSession()

	// 5. 端口转发
	for _, lf := range flags.localForwards {
		if err := startLocalForward(sshConn, lf); err != nil {
			fmt.Fprintf(os.Stderr, "ssh-client: local forward: %v\n", err)
			return 255
		}
	}
	for _, rf := range flags.remoteForwards {
		if err := startRemoteForward(sshConn, rf); err != nil {
			fmt.Fprintf(os.Stderr, "ssh-client: remote forward: %v\n", err)
			return 255
		}
	}

	// 6. 会话模式
	if flags.noSession {
		// -N: 仅端口转发，等待信号
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
		<-sig
		fmt.Fprintln(os.Stderr, "ssh-client: forwarding closed")
		return 0
	}

	if len(flags.command) > 0 {
		// 远程命令执行
		cmdStr := strings.Join(flags.command, " ")
		code, err := sshclient.RunCommand(sshConn, cmdStr, os.Stdout, os.Stderr)
		if err != nil {
			fmt.Fprintf(os.Stderr, "ssh-client: command error: %v\n", err)
			return 255
		}
		return code
	}

	// 交互式 shell
	return interactiveShell(sshConn, opts, flags.noTty, flags.forceTty)
}

// interactiveShell 启动交互式远程 shell。
func interactiveShell(client *ssh.Client, opts *sshclient.Options, noTty, forceTty bool) int {
	session, err := client.NewSession()
	if err != nil {
		fmt.Fprintf(os.Stderr, "ssh-client: session error: %v\n", err)
		return 255
	}
	defer session.Close()

	// 非交互（-T）时不请求 PTY
	if noTty {
		session.Stdin = os.Stdin
		session.Stdout = os.Stdout
		session.Stderr = os.Stderr
		if err := session.Shell(); err != nil {
			fmt.Fprintf(os.Stderr, "ssh-client: shell: %v\n", err)
			return 255
		}
		if err := session.Wait(); err != nil {
			var exitErr *ssh.ExitError
			if sshclient.IsExitError(err, &exitErr) {
				return exitErr.ExitStatus()
			}
			return 130
		}
		return 0
	}

	// 设置终端模式
	fd := int(os.Stdin.Fd())
	oldState, err := term.MakeRaw(fd)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ssh-client: terminal raw mode: %v\n", err)
		return 255
	}
	defer term.Restore(fd, oldState)

	// 获取终端尺寸
	width, height, err := term.GetSize(fd)
	if err != nil {
		width, height = 80, 24
	}

	// 请求 PTY
	modes := ssh.TerminalModes{
		ssh.ECHO:          1,
		ssh.TTY_OP_ISPEED: 115200,
		ssh.TTY_OP_OSPEED: 115200,
	}
	if err := session.RequestPty("xterm-256color", height, width, modes); err != nil {
		fmt.Fprintf(os.Stderr, "ssh-client: request pty: %v\n", err)
		return 255
	}

	session.Stdin = os.Stdin
	session.Stdout = os.Stdout
	session.Stderr = os.Stderr

	// 窗口尺寸变更
	stopWinCh := watchWindowSize(session, fd)
	defer stopWinCh()

	// 信号转发
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		session.Close()
	}()

	if err := session.Shell(); err != nil {
		fmt.Fprintf(os.Stderr, "ssh-client: shell: %v\n", err)
		return 255
	}

	if err := session.Wait(); err != nil {
		var exitErr *ssh.ExitError
		if sshclient.IsExitError(err, &exitErr) {
			return exitErr.ExitStatus()
		}
		// Ctrl+C 等中断
		return 130
	}
	return 0
}

// applyOptions 解析 -o key=value 并设置 Options。
func applyOptions(opts *sshclient.Options, options []string) error {
	whitelist := map[string]bool{
		"StrictHostKeyChecking":   true,
		"UserKnownHostsFile":      true,
		"ConnectTimeout":          true,
		"ServerAliveInterval":     true,
		"ServerAliveCountMax":     true,
		"HostKeyAlgorithms":       true,
		"PreferredAuthentications": true,
		"LogLevel":                true,
		"Compression":             true,
		"ProxyJump":               true, // 只警告，不实现
		"ProxyCommand":            true,
		"CertificateFile":         true,
	}

	for _, o := range options {
		kv := strings.SplitN(o, "=", 2)
		if len(kv) != 2 {
			// OpenSSH 兼容：-o "Option Value"（与 ssh_config 文件格式一致）。
			// 值本身可含空格，因此只按第一个空格拆分。
			kv = strings.SplitN(o, " ", 2)
		}
		if len(kv) != 2 {
			return fmt.Errorf("invalid -o option %q (expected key=value or 'key value')", o)
		}
		key := strings.TrimSpace(kv[0])
		val := strings.TrimSpace(kv[1])

		if !whitelist[key] {
			fmt.Fprintf(os.Stderr, "ssh-client: warning: unsupported -o option %q (ignored)\n", o)
			continue
		}

		switch key {
		case "StrictHostKeyChecking":
			opts.StrictHostKeyChecking = val
		case "UserKnownHostsFile":
			opts.UserKnownHostsFile = val
		case "ConnectTimeout":
			if sec, err := strconv.Atoi(val); err == nil && sec > 0 {
				opts.ConnectTimeout = time.Duration(sec) * time.Second
			}
		case "ServerAliveInterval":
			if sec, err := strconv.Atoi(val); err == nil && sec > 0 {
				opts.ServerAliveInterval = time.Duration(sec) * time.Second
			}
		case "ServerAliveCountMax":
			if n, err := strconv.Atoi(val); err == nil && n > 0 {
				opts.ServerAliveCountMax = n
			}
		case "HostKeyAlgorithms":
			opts.HostKeyAlgorithms = strings.Split(val, ",")
		case "PreferredAuthentications":
			opts.PreferredAuthentications = val
		case "LogLevel":
			opts.LogLevel = strings.ToUpper(val)
		case "Compression":
			opts.Compression = val == "yes" || val == "true"
		case "ProxyJump":
			fmt.Fprintf(os.Stderr, "ssh-client: warning: ProxyJump not implemented, ignoring %q\n", val)
		case "ProxyCommand":
			opts.ProxyCommand = val
		case "CertificateFile":
			opts.CertificateFiles = append(opts.CertificateFiles, val)
		}
	}
	return nil
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

// startLocalForward 启动本地端口转发（-L）。
func startLocalForward(client *ssh.Client, spec string) error {
	bind, portStr, remoteHost, remotePortStr, err := parseForwardSpec(spec)
	if err != nil {
		return err
	}

	localAddr := net.JoinHostPort(bind, portStr)
	remoteAddr := net.JoinHostPort(remoteHost, remotePortStr)

	listener, err := net.Listen("tcp", localAddr)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", localAddr, err)
	}

	go func() {
		defer listener.Close()
		for {
			localConn, err := listener.Accept()
			if err != nil {
				return
			}
			go func() {
				remoteConn, err := client.Dial("tcp", remoteAddr)
				if err != nil {
					localConn.Close()
					return
				}
				go io.Copy(remoteConn, localConn)
				go io.Copy(localConn, remoteConn)
			}()
		}
	}()

	fmt.Fprintf(os.Stderr, "ssh-client: local forward %s -> %s\n", localAddr, remoteAddr)
	return nil
}

// startRemoteForward 启动远程端口转发（-R）。
func startRemoteForward(client *ssh.Client, spec string) error {
	bind, portStr, localHost, localPortStr, err := parseForwardSpec(spec)
	if err != nil {
		return err
	}

	remoteAddr := net.JoinHostPort(bind, portStr)
	localAddr := net.JoinHostPort(localHost, localPortStr)

	listener, err := client.Listen("tcp", remoteAddr)
	if err != nil {
		return fmt.Errorf("remote listen on %s: %w", remoteAddr, err)
	}

	go func() {
		defer listener.Close()
		for {
			remoteConn, err := listener.Accept()
			if err != nil {
				return
			}
			go func() {
				localConn, err := net.Dial("tcp", localAddr)
				if err != nil {
					remoteConn.Close()
					return
				}
				go io.Copy(remoteConn, localConn)
				go io.Copy(localConn, remoteConn)
			}()
		}
	}()

	fmt.Fprintf(os.Stderr, "ssh-client: remote forward %s -> %s\n", remoteAddr, localAddr)
	return nil
}
