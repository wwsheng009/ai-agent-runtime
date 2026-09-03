// Command ssh-keygen 是一个参考 OpenSSH ssh-keygen 实现的 SSH 密钥与证书工具。
//
// 支持的模式（与 OpenSSH ssh-keygen 对齐）：
//
//	生成密钥对:   ssh-keygen [-t ed25519|rsa|ecdsa] [-b bits] [-f file] [-N passphrase] [-C comment]
//	签发用户证书: ssh-keygen -s ca_key -I identity [-n principals] [-V validity] [-z serial] [-O option] key.pub...
//	签发主机证书: ssh-keygen -s ca_key -I identity -h [-n hostnames] [-V validity] [-z serial] key.pub...
//	查看证书:     ssh-keygen -L -f cert.pub
//	查看指纹:     ssh-keygen -l -f key.pub
//	打印公钥:     ssh-keygen -y -f private_key
//
// 证书格式遵循 OpenSSH PROTOCOL.certkeys（ssh-*-cert-v01@openssh.com），
// 签发出的证书可直接用于 ssh-client / sshd 的 CertificateFile 认证，
// 也可与 OpenSSH 的 ssh-keygen 交叉验证。
package main

import (
	"crypto/rand"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/pflag"
	"golang.org/x/crypto/ssh"
	"golang.org/x/term"
)

// version 通过构建参数 -X main.version=<v> 注入（例如 Win7 构建脚本）。
var version = "0.1.0"

const (
	// CertTypeUser / CertTypeHost 对应 OpenSSH sshkey.h 的 SSH2_CERT_TYPE_USER / HOST。
	CertTypeUser = 1
	CertTypeHost = 2

	// CertTimeInfinity 表示证书永不过期（-V 的 "forever"）。
	CertTimeInfinity = ^uint64(0)

	// 默认位数与 OpenSSH 对齐：RSA 3072、ECDSA 256、ed25519 固定。
	defaultRSAKeyBits   = 3072
	defaultECDSAKeyBits = 256
)

// cliFlags 保存解析后的命令行参数。
type cliFlags struct {
	keyType       string   // -t: ed25519 | rsa | ecdsa
	bits          int      // -b
	keyFile       string   // -f
	passphrase    string   // -N（新口令，密钥生成）
	comment       string   // -C
	caKeyPath     string   // -s：CA 私钥路径（进入签发模式）
	keyID         string   // -I：证书 Key ID
	principals    string   // -n：逗号分隔的 principal 列表
	hostCert      bool     // -h：主机证书
	validity      string   // -V：有效期，如 +52w、-1h:+1d、20260101:20280101
	serial        uint64   // -z：序列号（默认 0，与 OpenSSH 一致）
	serialAutoInc bool     // -z+：序列号自动递增
	options       []string // -O：证书选项（可重复）
	showCert      bool     // -L：查看证书
	fingerprint   bool     // -l：查看指纹
	printPublic   bool     // -y：从私钥打印公钥
	quiet         bool     // -q
	verbose       bool     // -v
	showHelp      bool     // --help
	// 目标公钥文件（仅 -s 模式可携带）
	keyFiles []string
}

func main() {
	flags, err := parseFlags(os.Args[1:])
	if err != nil {
		fmt.Fprintln(os.Stderr, "ssh-keygen:", err)
		os.Exit(255)
	}
	if flags.showHelp {
		usage()
		os.Exit(0)
	}
	if err := run(flags); err != nil {
		fmt.Fprintln(os.Stderr, "ssh-keygen:", err)
		os.Exit(1)
	}
}

func parseFlags(args []string) (*cliFlags, error) {
	flags := &cliFlags{}

	fs := pflag.NewFlagSet("ssh-keygen", pflag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fs.SortFlags = false

	fs.StringVarP(&flags.keyType, "type", "t", "ed25519", "key type: ed25519 (default), rsa, ecdsa")
	fs.IntVarP(&flags.bits, "bits", "b", 0, "key size in bits (rsa: 3072 default; ecdsa: 256 default)")
	fs.StringVarP(&flags.keyFile, "file", "f", "", "key file path (for -L/-l/-y: input; otherwise output)")
	fs.StringVarP(&flags.passphrase, "new-passphrase", "N", "", "passphrase for the new private key")
	fs.StringVarP(&flags.comment, "comment", "C", "", "new comment")
	fs.StringVarP(&flags.caKeyPath, "sign", "s", "", "CA private key used to sign certificates")
	fs.StringVarP(&flags.keyID, "identity", "I", "", "certificate identity (key ID, required for -s)")
	fs.StringVarP(&flags.principals, "principals", "n", "", "comma-separated principals (users or hostnames)")
	fs.BoolVarP(&flags.hostCert, "host-certificate", "h", false, "generate a host certificate instead of a user certificate")
	fs.StringVarP(&flags.validity, "validity", "V", "", "certificate validity, e.g. +52w, -1h:+1d, always:forever")
	fs.VarP(&serialValue{flags}, "serial", "z", "certificate serial number (0 default; -z+ auto-increments)")
	fs.StringArrayVarP(&flags.options, "option", "O", nil, "certificate option (permit-pty, force-command=..., source-address=..., extension:..., critical:..., clear, ...)")
	fs.BoolVarP(&flags.showCert, "show-cert", "L", false, "print certificate contents")
	fs.BoolVarP(&flags.fingerprint, "fingerprint", "l", false, "print fingerprint of the public key")
	fs.BoolVarP(&flags.printPublic, "print-public", "y", false, "print the public key of a private key file")
	fs.BoolVarP(&flags.quiet, "quiet", "q", false, "quiet mode")
	fs.BoolVarP(&flags.verbose, "verbose", "v", false, "verbose output")
	fs.BoolVarP(&flags.showHelp, "help", "H", false, "show this help and exit")

	if err := fs.Parse(args); err != nil {
		return nil, err
	}
	flags.keyFiles = fs.Args()
	if len(flags.keyFiles) == 0 && flags.caKeyPath != "" {
		return nil, fmt.Errorf("missing public key file(s) to sign")
	}
	if len(flags.keyFiles) > 0 && flags.caKeyPath == "" {
		return nil, fmt.Errorf("unexpected argument %q (extra arguments are only valid with -s)", flags.keyFiles[0])
	}
	return flags, nil
}

// serialValue 实现 pflag.Value，支持 -z N 与 -z+（自动递增）。
type serialValue struct {
	flags *cliFlags
}

func (s *serialValue) String() string { return fmt.Sprintf("%d", s.flags.serial) }

func (s *serialValue) Set(v string) error {
	if strings.HasPrefix(v, "+") {
		s.flags.serialAutoInc = true
		v = v[1:]
	}
	if v == "" {
		// -z+ 不带数值：仅启用自动递增，serial 保持当前值
		if s.flags.serialAutoInc {
			return nil
		}
		return errors.New("empty serial number")
	}
	var n uint64
	if _, err := fmt.Sscanf(v, "%d", &n); err != nil {
		return fmt.Errorf("invalid serial number %q", v)
	}
	s.flags.serial = n
	return nil
}

func (s *serialValue) Type() string { return "uint" }

// run 根据参数分发到对应模式。
func run(flags *cliFlags) error {
	switch {
	case flags.showCert:
		return runShowCert(flags)
	case flags.fingerprint:
		return runFingerprint(flags)
	case flags.printPublic:
		return runPrintPublic(flags)
	case flags.caKeyPath != "":
		return runSign(flags)
	default:
		return runGenerate(flags)
	}
}

// usage 打印帮助信息（模仿 ssh-keygen 的分节风格）。
func usage() {
	fmt.Fprintf(os.Stderr, `ssh-keygen (Go implementation, OpenSSH-compatible)

Usage:
  ssh-keygen [-t type] [-b bits] [-f file] [-N passphrase] [-C comment]   generate a key pair
  ssh-keygen -s ca_key -I identity [-h] [-n principals] [-V validity] [-z serial] [-O option] file...
                                                                          sign public keys into certificates
  ssh-keygen -L -f cert.pub                                               print certificate contents
  ssh-keygen -l -f key.pub                                                print key fingerprint
  ssh-keygen -y -f private_key                                            print public key of a private key

Options:
  -t type       key type: ed25519 (default), rsa, ecdsa
  -b bits       key size (rsa default 3072, ecdsa default 256)
  -f file       key file (input for -L/-l/-y; output otherwise)
  -N pass       passphrase for a new private key (empty by default)
  -C comment    comment stored in the public key
  -s ca_key     CA private key for signing certificates
  -I identity   certificate identity (key ID), required with -s
  -n names      comma-separated principals (user names, or hostnames with -h)
  -h            sign a host certificate (default: user certificate)
  -V validity   validity: +52w | -1h:+1d | 20260101:20280101 | always:forever
  -z serial     certificate serial (default 0; -z+ auto-increments per file)
  -O option     certificate option; repeatable. See below.
  -L            print certificate contents
  -l            print fingerprint of the public key
  -y            print public key derived from a private key
  -q            quiet mode
  -v            verbose output
  -H            show this help

Certificate options (-O):
  clear                          reset all permit-* extensions to off
  permit-pty, no-pty             allow/deny PTY allocation
  permit-agent-forwarding        allow agent forwarding
  permit-port-forwarding         allow port forwarding
  permit-X11-forwarding          allow X11 forwarding
  permit-user-rc                 allow execution of ~/.ssh/rc
  verify-required                require user verification on the CA key
  force-command=command          force the remote command
  source-address=addr[,addr...]  restrict to source addresses (IPv4/IPv6/CIDR)
  extension:name[=value]         add an arbitrary extension
  critical:name[=value]          add an arbitrary critical option

Validity formats (-V):
  +timespec                      from now-1min to now+timespec (e.g. +52w)
  from:to                        explicit range; each side: [+-]timespec, YYYYMMDD,
                                 YYYYMMDDHHMM, YYYYMMDDHHMMSS (suffix Z/UTC = UTC),
                                 0xHEX epoch, always, forever
  timespec                       Ns|Nm|Nh|Nd|Nw, combinable (e.g. 1h30m)

Examples:
  # Generate a CA key pair (ed25519)
  ssh-keygen -t ed25519 -f ca_key -C "user CA"

  # Sign a user certificate valid for 52 weeks, principals alice,bob
  ssh-keygen -s ca_key -I alice@example.com -n alice,bob -V +52w alice.pub

  # Sign a host certificate valid forever
  ssh-keygen -s ca_key -I web1 -h -n web1.example.com -V always:forever web1.pub

  # Certificate with forced command and source-address restriction
  ssh-keygen -s ca_key -I backup -n backup -O force-command="/usr/local/bin/backup" \
      -O source-address=192.168.1.0/24 -V +1y backup.pub

  # Inspect a certificate
  ssh-keygen -L -f alice-cert.pub
`)
}

// defaultKeyFile 返回未指定 -f 时的默认路径（$HOME/.ssh/id_<type>）。
func defaultKeyFile(keyType string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("cannot determine home directory: %w", err)
	}
	name := "id_" + strings.ToLower(keyType)
	return filepath.Join(home, ".ssh", name), nil
}

// readPassphrase 交互式读取口令（写入 stderr，与 OpenSSH 一致）。
func readPassphrase(prompt string) (string, error) {
	fmt.Fprint(os.Stderr, prompt)
	pw, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return "", err
	}
	return string(pw), nil
}

// loadPrivateKey 读取私钥文件（支持 OpenSSH/PEM 格式、可带口令）。
// 优先使用环境变量 SSH_KEY_PASSPHRASE 避免交互。
func loadPrivateKey(path string) (ssh.Signer, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	key, err := ssh.ParseRawPrivateKey(data)
	if err == nil {
		return ssh.NewSignerFromKey(key)
	}
	var passErr *ssh.PassphraseMissingError
	if !errors.As(err, &passErr) {
		return nil, fmt.Errorf("parse key %q: %w", path, err)
	}
	passphrase := os.Getenv("SSH_KEY_PASSPHRASE")
	if passphrase == "" && term.IsTerminal(int(os.Stdin.Fd())) {
		var perr error
		passphrase, perr = readPassphrase(fmt.Sprintf("Enter passphrase for key %q: ", path))
		if perr != nil {
			return nil, perr
		}
	}
	if passphrase == "" {
		return nil, fmt.Errorf("key %q is encrypted; set SSH_KEY_PASSPHRASE or use an interactive terminal", path)
	}
	key, err = ssh.ParseRawPrivateKeyWithPassphrase(data, []byte(passphrase))
	if err != nil {
		return nil, fmt.Errorf("parse key %q with passphrase: %w", path, err)
	}
	return ssh.NewSignerFromKey(key)
}

// randReader 供测试注入；生产使用 crypto/rand。
var randReader = rand.Reader

// nowFunc 供测试注入；生产使用 time.Now。
var nowFunc = time.Now
