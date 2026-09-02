package sshclient

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
	"golang.org/x/term"
)

// BuildAuthMethods 根据 Options 构建 SSH 认证方法列表。
// 返回的方法顺序按 OpenSSH 语义排列：agent 公钥 → 文件公钥 → password →
// keyboard-interactive。agent 密钥优先（已在 agent 中的密钥无需输入口令）；
// 文件私钥通过懒加载回调按需读取，仅在 agent 认证失败时才触发私钥口令提示，
// 与 OpenSSH 行为一致（见 buildKeySigners 的证书配对逻辑）。
// 调用方可将它们全部传入 ssh.ClientConfig.Auth。
func BuildAuthMethods(opts *Options, stderr io.Writer) ([]ssh.AuthMethod, error) {
	var methods []ssh.AuthMethod

	// 1. ssh-agent（除非 IdentitiesOnly）——优先，避免对已在 agent 中的密钥提示口令
	if !opts.IdentitiesOnly {
		if agentCallback, err := dialAgentSigners(stderr); err == nil {
			methods = append(methods, ssh.PublicKeysCallback(agentCallback))
		}
	}

	// 2. 文件私钥（懒加载回调，认证时才读取；含证书配对）
	if hasIdentitySource(opts) {
		methods = append(methods, ssh.PublicKeysCallback(func() ([]ssh.Signer, error) {
			signers, err := buildKeySigners(opts, stderr)
			if err != nil {
				return nil, err
			}
			if len(signers) == 0 {
				return nil, errors.New("no usable private keys")
			}
			return signers, nil
		}))
	}

	// 3. 密码
	if opts.PasswordSet && opts.Password != "" {
		password := opts.Password
		methods = append(methods, ssh.Password(password))
		// keyboard-interactive fallback（某些服务器要求）
		methods = append(methods, ssh.KeyboardInteractive(func(name, instruction string, questions []string, echos []bool) ([]string, error) {
			answers := make([]string, len(questions))
			for i := range questions {
				answers[i] = password
			}
			return answers, nil
		}))
	} else if !opts.PasswordSet && term.IsTerminal(int(os.Stdin.Fd())) {
		// 交互式输入密码（未通过 --password 提供时）。
		// 与 OpenSSH 一致：仅在服务器请求 password/kbd-interactive 认证
		// （即公钥/agent 认证失败后）才提示，而不是在连接前就询问。
		var once sync.Once
		var cachedPw string
		var cachedErr error
		readPassword := func() (string, error) {
			once.Do(func() {
				fmt.Fprint(stderr, "Password: ")
				pw, err := term.ReadPassword(int(os.Stdin.Fd()))
				fmt.Fprintln(stderr)
				if err != nil {
					cachedErr = err
					return
				}
				cachedPw = string(pw)
			})
			return cachedPw, cachedErr
		}
		methods = append(methods, ssh.PasswordCallback(func() (string, error) {
			return readPassword()
		}))
		// keyboard-interactive fallback（某些服务器要求）
		methods = append(methods, ssh.KeyboardInteractive(func(name, instruction string, questions []string, echos []bool) ([]string, error) {
			pw, err := readPassword()
			if err != nil {
				return nil, err
			}
			answers := make([]string, len(questions))
			for i := range questions {
				answers[i] = pw
			}
			return answers, nil
		}))
	}

	if len(methods) == 0 {
		return nil, errors.New("no authentication methods available (provide password, key, or start ssh-agent)")
	}
	return methods, nil
}

// hasIdentitySource 判断是否存在可用的文件密钥来源：
// 显式 IdentityFile/CertificateFile，或 ~/.ssh 下的默认密钥。
func hasIdentitySource(opts *Options) bool {
	if len(opts.IdentityFiles) > 0 || len(opts.CertificateFiles) > 0 {
		return true
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return false
	}
	for _, k := range []string{"id_ed25519", "id_ecdsa", "id_rsa"} {
		if _, err := os.Stat(filepath.Join(home, ".ssh", k)); err == nil {
			return true
		}
	}
	return false
}

// buildKeySigners 读取 IdentityFiles 或默认路径中的私钥，返回 ssh.Signer 列表。
// 每个私钥若存在配套用户证书（显式 CertificateFile 或同目录 <key>-cert.pub），
// 证书签名器排在裸私钥之前（与 OpenSSH 行为一致：优先以证书身份认证）。
func buildKeySigners(opts *Options, stderr io.Writer) ([]ssh.Signer, error) {
	files := opts.IdentityFiles
	if len(files) == 0 {
		// 按 Ed25519 → ECDSA → RSA 顺序搜索默认密钥
		home, err := os.UserHomeDir()
		if err == nil {
			keys := []string{"id_ed25519", "id_ecdsa", "id_rsa"}
			for _, k := range keys {
				p := filepath.Join(home, ".ssh", k)
				if _, err := os.Stat(p); err == nil {
					files = append(files, p)
				}
			}
		}
	}

	var signers []ssh.Signer
	for i, f := range files {
		// 第 i 个私钥对应的显式证书（OpenSSH 按出现顺序配对）
		certPath := ""
		if i < len(opts.CertificateFiles) && opts.CertificateFiles[i] != "" {
			certPath = opts.CertificateFiles[i]
		}
		ss, err := loadKeySigners(f, certPath, stderr)
		if err != nil {
			fmt.Fprintf(stderr, "Warning: skip key %q: %v\n", f, err)
			continue
		}
		signers = append(signers, ss...)
	}
	return signers, nil
}

// loadKeySigners 读取私钥，并在存在配套证书时返回 [证书签名器, 裸私钥签名器]；
// 无证书时只返回裸私钥签名器。证书路径优先取显式 certPath（CertificateFile），
// 否则按 OpenSSH 命名约定自动探测 <私钥路径>-cert.pub。
func loadKeySigners(keyPath, certPath string, stderr io.Writer) ([]ssh.Signer, error) {
	raw, err := loadKeyFile(keyPath, stderr)
	if err != nil {
		return nil, err
	}

	if certPath == "" {
		certPath = keyPath + "-cert.pub"
	}
	if _, err := os.Stat(certPath); err != nil {
		return []ssh.Signer{raw}, nil // 无配套证书，使用裸私钥
	}

	cs, err := parseCertSigner(certPath, raw)
	if err != nil {
		fmt.Fprintf(stderr, "Warning: skip certificate %q: %v\n", certPath, err)
		return []ssh.Signer{raw}, nil // 证书不可用，回退裸私钥
	}
	return []ssh.Signer{cs, raw}, nil
}

// parseCertSigner 解析 OpenSSH 用户证书文件并包装私钥签名器。
// 证书文件为 authorized_keys 格式的单行 ssh-*-cert-v01@openssh.com 条目。
// NewCertSigner 会校验证书公钥与私钥签名器匹配，不匹配返回错误。
func parseCertSigner(certPath string, raw ssh.Signer) (ssh.Signer, error) {
	data, err := os.ReadFile(certPath)
	if err != nil {
		return nil, err
	}
	pub, _, _, _, err := ssh.ParseAuthorizedKey(data)
	if err != nil {
		return nil, fmt.Errorf("parse certificate %q: %w", certPath, err)
	}
	cert, ok := pub.(*ssh.Certificate)
	if !ok {
		return nil, fmt.Errorf("%q is not a certificate (type %s)", certPath, pub.Type())
	}
	cs, err := ssh.NewCertSigner(cert, raw)
	if err != nil {
		return nil, fmt.Errorf("certificate %q does not match its private key: %w", certPath, err)
	}
	return cs, nil
}

// loadKeyFile 读取单私钥文件，支持 PEM 和 OpenSSH 格式，可带密码。
func loadKeyFile(path string, stderr io.Writer) (ssh.Signer, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	key, err := ssh.ParseRawPrivateKey(data)
	if err == nil {
		return ssh.NewSignerFromKey(key)
	}

	// 带密码的私钥
	var passErr *ssh.PassphraseMissingError
	if !errors.As(err, &passErr) {
		return nil, fmt.Errorf("parse key %q: %w", path, err)
	}

	passphrase, err := readPassphrase(path, stderr)
	if err != nil {
		return nil, fmt.Errorf("read passphrase for %q: %w", path, err)
	}

	key, err = ssh.ParseRawPrivateKeyWithPassphrase(data, []byte(passphrase))
	if err != nil {
		return nil, fmt.Errorf("parse key %q with passphrase: %w", path, err)
	}
	return ssh.NewSignerFromKey(key)
}

// readPassphrase 从终端读取或从环境变量获取私钥解密密码。
func readPassphrase(keyPath string, stderr io.Writer) (string, error) {
	// 环境变量覆盖（非交互场景）
	if pw := os.Getenv("SSH_KEY_PASSPHRASE"); pw != "" {
		return pw, nil
	}
	// 交互式读取
	if term.IsTerminal(int(os.Stdin.Fd())) {
		fmt.Fprintf(stderr, "Enter passphrase for key %q: ", keyPath)
		pw, err := term.ReadPassword(int(os.Stdin.Fd()))
		fmt.Fprintln(stderr)
		if err != nil {
			return "", err
		}
		return string(pw), nil
	}
	return "", fmt.Errorf("stdin is not a terminal and SSH_KEY_PASSPHRASE is not set")
}

// dialAgentSigners 通过 ssh-agent 返回签名回调函数。
func dialAgentSigners(stderr io.Writer) (func() ([]ssh.Signer, error), error) {
	conn, err := dialAgent()
	if err != nil {
		if isVerbose() {
			fmt.Fprintf(stderr, "ssh-agent: %v (skip)\n", err)
		}
		return nil, err
	}
	ag := agent.NewClient(conn)
	return func() ([]ssh.Signer, error) {
		signers, err := ag.Signers()
		if err != nil {
			return nil, fmt.Errorf("agent signers: %w", err)
		}
		return signers, nil
	}, nil
}

// globalVerbose 是包级调试开关，由 conn.go 在连接时设置。
var globalVerbose bool

func isVerbose() bool { return globalVerbose }
