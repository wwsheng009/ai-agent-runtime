package sshclient

import (
	"errors"
	"fmt"
	"io"
	"os"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
	"golang.org/x/term"
)

// BuildAuthMethods 根据 Options 构建 SSH 认证方法列表。
// 返回的方法顺序按优先级排列：publickey → agent → password → keyboard-interactive。
// 调用方可将它们全部传入 ssh.ClientConfig.Auth。
func BuildAuthMethods(opts *Options, stderr io.Writer) ([]ssh.AuthMethod, error) {
	var methods []ssh.AuthMethod

	// 1. 公钥认证（文件私钥）
	if signers, err := buildKeySigners(opts, stderr); err == nil && len(signers) > 0 {
		methods = append(methods, ssh.PublicKeys(signers...))
	}

	// 2. ssh-agent（除非 IdentitiesOnly 且未指定 IdentityFiles）
	if !opts.IdentitiesOnly {
		if agentCallback, err := dialAgentSigners(stderr); err == nil {
			methods = append(methods, ssh.PublicKeysCallback(agentCallback))
		}
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
		// 交互式输入密码（未通过 --password 提供时）
		fmt.Fprint(stderr, "Password: ")
		if pw, err := term.ReadPassword(int(os.Stdin.Fd())); err == nil {
			fmt.Fprintln(stderr)
			if len(pw) > 0 {
				password := string(pw)
				methods = append(methods, ssh.Password(password))
				methods = append(methods, ssh.KeyboardInteractive(func(name, instruction string, questions []string, echos []bool) ([]string, error) {
					answers := make([]string, len(questions))
					for i := range questions {
						answers[i] = password
					}
					return answers, nil
				}))
			}
		}
	}

	if len(methods) == 0 {
		return nil, errors.New("no authentication methods available (provide password, key, or start ssh-agent)")
	}
	return methods, nil
}

// buildKeySigners 读取 IdentityFiles 或默认路径中的私钥，返回 ssh.Signer 列表。
func buildKeySigners(opts *Options, stderr io.Writer) ([]ssh.Signer, error) {
	files := opts.IdentityFiles
	if len(files) == 0 {
		// 按 Ed25519 → ECDSA → RSA 顺序搜索默认密钥
		home, err := os.UserHomeDir()
		if err == nil {
			keys := []string{"id_ed25519", "id_ecdsa", "id_rsa"}
			for _, k := range keys {
				p := home + string(os.PathSeparator) + ".ssh" + string(os.PathSeparator) + k
				if _, err := os.Stat(p); err == nil {
					files = append(files, p)
				}
			}
		}
	}

	var signers []ssh.Signer
	for _, f := range files {
		s, err := loadKeyFile(f, stderr)
		if err != nil {
			fmt.Fprintf(stderr, "Warning: skip key %q: %v\n", f, err)
			continue
		}
		signers = append(signers, s)
	}
	return signers, nil
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
