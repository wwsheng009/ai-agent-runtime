package main

import (
	"fmt"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync/atomic"
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
	port           int
	user           string
	identityFiles  []string
	password       string
	passwordSet    bool
	options        []string
	quiet          bool
	verbose        bool
	configFile     string
	noSession      bool
	localForwards  []string
	remoteForwards []string
	noReconnect    bool
	noTty          bool
	forceTty       bool
	showVersion    bool
	ipv4           bool
	ipv6           bool
	compress       bool
	timeout        int
	knownHostsFile string
	showHelp       bool
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
		fmt.Fprintf(fs.Output(), `ssh-client - OpenSSH 兼容的 SSH 客户端

Usage:
  ssh-client [options] [user@]host [command]

Description:
  Interactive remote shell, remote command execution and local/remote port
  forwarding. Authentication order: publickey -> ssh-agent -> password ->
  keyboard-interactive.

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
  - Dead-link detection: -o ServerAliveInterval=15 -o ServerAliveCountMax=3
    (enabled by default in -N mode: 15s x 3).
  - -N tunnel supervision: when the link dies the client reconnects with
    exponential backoff (1s..30s) and restores every -L/-R forward; use
    --no-reconnect to exit with code 255 instead and let a supervisor restart it.
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
	fs.BoolVar(&flags.noReconnect, "no-reconnect", false, "In -N mode, exit (255) on link loss instead of reconnecting")
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
			fs.SetOutput(os.Stdout)
			fs.Usage()
			os.Exit(0)
		}
		fmt.Fprintln(os.Stderr, "ssh-client:", err)
		fs.Usage()
		os.Exit(255)
	}
	if flags.showHelp {
		// 显式请求帮助时输出到 stdout，方便管道/分页查看
		fs.SetOutput(os.Stdout)
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

	// 4. -N（仅转发）模式：建链、转发与断线重连全部交由 runForwardOnly 监督，
	// 避免留下「端口仍在监听、转发已失效」的僵尸进程（见 forward.go）。
	if flags.noSession {
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
		return runForwardOnly(opts, flags.localForwards, flags.remoteForwards, os.Stderr, sigCh, !flags.noReconnect, defaultConnect)
	}

	// 5. 建立连接
	client, err := sshclient.NewClient(opts, os.Stderr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ssh-client: connection failed: %v\n", err)
		return 255
	}
	defer client.Close()

	sshConn := client.SSHSession()

	// 6. 端口转发（交互/命令模式只建立一次；监听器意外失效仅告警，不断开会话）
	handles, err := startForwards(sshConn, flags.localForwards, flags.remoteForwards, nil, opts.ExitOnForwardFailure, os.Stderr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ssh-client: %v\n", err)
		return 255
	}
	defer closeForwards(handles)

	// 7. 会话模式
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
		return sessionExitCode(session.Wait(), false)
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

	// 信号转发：Ctrl+C 关闭会话并按 130 退出（与链路掉线的 255 区分开）
	var interrupted atomic.Bool
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		interrupted.Store(true)
		session.Close()
	}()

	if err := session.Shell(); err != nil {
		fmt.Fprintf(os.Stderr, "ssh-client: shell: %v\n", err)
		return 255
	}

	return sessionExitCode(session.Wait(), interrupted.Load())
}

// sessionExitCode 把会话结束错误映射为退出码：
// 远程退出码优先；用户中断 → 130；其余（链路掉线、协议错误）→ 255 并给出原因。
func sessionExitCode(err error, interrupted bool) int {
	if err == nil {
		return 0
	}
	var exitErr *ssh.ExitError
	if sshclient.IsExitError(err, &exitErr) {
		return exitErr.ExitStatus()
	}
	if interrupted {
		return 130
	}
	fmt.Fprintf(os.Stderr, "ssh-client: connection lost: %v\n", err)
	return 255
}

// applyOptions 解析 -o key=value 并设置 Options。
func applyOptions(opts *sshclient.Options, options []string) error {
	whitelist := map[string]bool{
		"StrictHostKeyChecking":    true,
		"UserKnownHostsFile":       true,
		"ConnectTimeout":           true,
		"ServerAliveInterval":      true,
		"ServerAliveCountMax":      true,
		"HostKeyAlgorithms":        true,
		"PreferredAuthentications": true,
		"LogLevel":                 true,
		"Compression":              true,
		"ExitOnForwardFailure":     true,
		"ProxyJump":                true, // 只警告，不实现
		"ProxyCommand":             true,
		"CertificateFile":          true,
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
		case "ExitOnForwardFailure":
			opts.ExitOnForwardFailure = val == "yes" || val == "true"
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
