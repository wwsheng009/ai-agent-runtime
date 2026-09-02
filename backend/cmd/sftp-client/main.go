package main

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/pflag"
	"golang.org/x/term"

	"github.com/wwsheng009/ai-agent-runtime/internal/sshclient"
	"github.com/wwsheng009/ai-agent-runtime/internal/winconsole"
)

// version 通过构建参数 -X main.version=<v> 注入（例如 Win7 构建脚本）。
var version = "0.1.0"

// cliFlags SFTP 客户端 CLI 参数。
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
	batchFile     string
	recursive     bool
	force         bool
	showVersion   bool
	ipv4          bool
	ipv6          bool
	timeout       int
	knownHostsFile string
	showHelp      bool

	// 目标（user@host[:path]）与本地/远程参数
	hostSpec string
	paths    []string
}

func main() {
	// Win7 及更早 conhost 无 VT 处理：先把控制台输出代码页切到 UTF-8，
	// 否则远端文件名/命令回显等非 ASCII 输出会被按 OEM 代码页（如 GBK）
	// 解码成乱码。支持 VT 的控制台、管道/文件重定向、非 Windows 平台
	// 均为空操作。
	if restore := winconsole.EnsureConsoleUTF8Output(); restore != nil {
		defer restore()
	}

	flags := parseFlags()
	os.Exit(run(flags))
}

func parseFlags() *cliFlags {
	flags := &cliFlags{}
	fs := pflag.NewFlagSet("sftp-client", pflag.ContinueOnError)
	fs.Usage = func() {
		fmt.Fprintf(fs.Output(), `Usage: sftp-client [options] [user@]host[:remote-path] [local-path...]

OpenSSH-compatible SFTP client for file transfer (upload / download / list,
interactive and batch modes).

Modes:
  Interactive  sftp-client user@host
  Batch        sftp-client -b batch.txt user@host
  Upload       sftp-client user@host local-file remote-path
  Download     sftp-client user@host:remote-path local-file
  List         sftp-client user@host:remote-dir/

Options:
`)
		fs.PrintDefaults()
		fmt.Fprintf(fs.Output(), `
Interactive / batch commands (full list via "help" inside the shell):
  ls [-la] [path]             List remote directory
  lls [path]                  List local directory
  cd <path>                   Change remote directory
  get [-r] <remote> [local]   Download file(s)/directory
  put [-r] <local> [remote]   Upload file(s)/directory
  rm <file> / rmdir <dir>     Delete remote file / empty directory
  mkdir <dir>                 Create remote directory
  chmod <mode> <path>         Change remote file permissions
  chown <uid>:<gid> <path>    Change remote file owner
  rename <old> <new>          Rename remote file or directory
  symlink <target> <link>     Create remote symlink
  stat <path>                 Show remote file info
  echo <text>                 Print text locally
  !<command>                  Execute local shell command
  help / ?                    Show help
  quit / exit / bye           Quit

Notes:
  - Directories require -R/--recursive; existing files are skipped unless -f/--force.
  - Batch mode (-b) aborts on the first failed command and returns non-zero.
  - Connection setup (TCP dial + SSH handshake) is bounded by ConnectTimeout
    (default 30s; use --timeout N or -o ConnectTimeout=N to change).
  - Dead-link detection: -o ServerAliveInterval=15 -o ServerAliveCountMax=3.
  - Host key verification: -o StrictHostKeyChecking=yes|accept-new|no (default accept-new).
`)
	}

	fs.IntVarP(&flags.port, "port", "P", 0, "SSH port (default 22)")
	fs.StringVarP(&flags.user, "user", "l", "", "Login username")
	fs.StringArrayVarP(&flags.identityFiles, "identity-file", "i", nil, "Identity file path (can be repeated)")
	fs.StringVar(&flags.password, "password", "", "Password (no short option; interactive if omitted)")
	fs.StringArrayVarP(&flags.options, "option", "o", nil, "OpenSSH config option (key=value)")
	fs.BoolVarP(&flags.quiet, "quiet", "q", false, "Quiet mode (suppress warnings/banners)")
	fs.BoolVarP(&flags.verbose, "verbose", "v", false, "Verbose output (debug)")
	fs.StringVarP(&flags.configFile, "config-file", "F", "", "ssh_config file path (default ~/.ssh/config)")
	fs.StringVarP(&flags.batchFile, "batch", "b", "", "Batch file with commands (one per line)")
	fs.BoolVarP(&flags.recursive, "recursive", "R", false, "Recursive directory transfer")
	fs.BoolVarP(&flags.force, "force", "f", false, "Force overwrite existing files")
	fs.BoolVarP(&flags.showVersion, "version", "V", false, "Show version")
	fs.BoolVarP(&flags.ipv4, "ipv4", "4", false, "Use IPv4 only")
	fs.BoolVarP(&flags.ipv6, "ipv6", "6", false, "Use IPv6 only")
	fs.IntVar(&flags.timeout, "timeout", 0, "Connection timeout (seconds)")
	fs.StringVar(&flags.knownHostsFile, "known-hosts-file", "", "known_hosts file path")
	fs.BoolVarP(&flags.showHelp, "help", "h", false, "Show this help message and exit")

	if err := fs.Parse(os.Args[1:]); err != nil {
		if err == pflag.ErrHelp {
			os.Exit(0)
		}
		fmt.Fprintln(os.Stderr, "sftp-client:", err)
		fs.Usage()
		os.Exit(254)
	}
	if flags.showHelp {
		fs.Usage()
		os.Exit(0)
	}
	// 版本号优先于目标参数校验（-V 无需 host）
	if flags.showVersion {
		fmt.Fprintf(os.Stderr, "sftp-client version %s\n", version)
		os.Exit(0)
	}
	if f := fs.Lookup("password"); f != nil && f.Changed {
		flags.passwordSet = true
	}

	args := fs.Args()
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "sftp-client: missing host")
		fs.Usage()
		os.Exit(254)
	}
	flags.hostSpec = args[0]
	flags.paths = args[1:]
	return flags
}

func run(flags *cliFlags) int {
	// 拆分 hostSpec: [user@]host[:path]
	host, user, remotePath := splitHostSpec(flags.hostSpec)

	opts := sshclient.Defaults()
	opts.Verbose = flags.verbose
	opts.Quiet = flags.quiet
	opts.Host = host
	opts.ConfigFile = flags.configFile

	if flags.port > 0 {
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

	if err := applyOptions(opts, flags.options); err != nil {
		fmt.Fprintf(os.Stderr, "sftp-client: %v\n", err)
		return 254
	}

	// 加载 ssh_config
	cfg, err := sshclient.LoadResolvedConfig(opts.ConfigFile, opts.Host)
	if err != nil {
		fmt.Fprintf(os.Stderr, "sftp-client: warning: %v\n", err)
	} else {
		opts.OriginalHost = opts.Host
		opts.ApplyConfig(cfg)
		if !opts.Quiet && cfg.ProxyJump != "" {
			fmt.Fprintf(os.Stderr, "sftp-client: warning: ProxyJump %q is not implemented; ignored\n", cfg.ProxyJump)
		}
	}

	// 建立 SSH 连接
	client, err := sshclient.NewClient(opts, os.Stderr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "sftp-client: connection failed: %v\n", err)
		return 255
	}
	defer client.Close()

	// 建立 SFTP 会话
	sftp, err := sshclient.NewSFTP(client.SSHSession())
	if err != nil {
		fmt.Fprintf(os.Stderr, "sftp-client: sftp session failed: %v\n", err)
		return 255
	}
	defer sftp.Close()

	// 模式分发
	switch {
	case flags.batchFile != "":
		return runBatch(sftp, flags)
	case len(flags.paths) > 0 || remotePath != "":
		return runDirect(sftp, flags, remotePath)
	default:
		return runInteractive(sftp, flags)
	}
}

// splitHostSpec 拆分 [user@]host[:path]。
func splitHostSpec(spec string) (host, user, remotePath string) {
	// user@host
	if at := strings.LastIndex(spec, "@"); at >= 0 {
		user = spec[:at]
		spec = spec[at+1:]
	}
	// host[:path] — 注意 IPv6 使用 [] 包裹
	if strings.HasPrefix(spec, "[") {
		// [ipv6]:path 或 [ipv6]
		if idx := strings.Index(spec, "]"); idx >= 0 {
			host = spec[1:idx]
			rest := spec[idx+1:]
			if strings.HasPrefix(rest, ":") {
				remotePath = strings.TrimPrefix(rest, ":")
			}
			return host, user, remotePath
		}
		host = strings.Trim(spec, "[]")
		return host, user, ""
	}
	if idx := strings.Index(spec, ":"); idx >= 0 {
		host = spec[:idx]
		remotePath = spec[idx+1:]
	} else {
		host = spec
	}
	return host, user, remotePath
}

// applyOptions 解析 -o key=value 并设置 Options（与 ssh-client 相同的白名单）。
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
		"ProxyJump":                true,
		"ProxyCommand":             true,
		"CertificateFile":          true,
	}

	for _, o := range options {
		kv := strings.SplitN(o, "=", 2)
		if len(kv) != 2 {
			return fmt.Errorf("invalid -o option %q (expected key=value)", o)
		}
		key := strings.TrimSpace(kv[0])
		val := strings.TrimSpace(kv[1])

		if !whitelist[key] {
			fmt.Fprintf(os.Stderr, "sftp-client: warning: unsupported -o option %q (ignored)\n", o)
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
			fmt.Fprintf(os.Stderr, "sftp-client: warning: ProxyJump not implemented, ignoring %q\n", val)
		case "ProxyCommand":
			opts.ProxyCommand = val
		case "CertificateFile":
			opts.CertificateFiles = append(opts.CertificateFiles, val)
		}
	}
	return nil
}

// isTerminal 判断 stdin 是否为终端。
func isTerminal() bool {
	return term.IsTerminal(int(os.Stdin.Fd()))
}