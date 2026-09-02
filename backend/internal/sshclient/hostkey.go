package sshclient

import (
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

// BuildHostKeyCallback 根据 Options 构建 host key 校验回调。
// mode 取值：
//   "no"         — 跳过校验，输出警告（Insecure）
//   默认/空      — accept-new：首次自动接受并追加到 known_hosts
//   "yes" / 其他 — 严格模式：拒绝未知主机
func BuildHostKeyCallback(opts *Options, stderr io.Writer) (ssh.HostKeyCallback, error) {
	if opts.StrictHostKeyChecking == StrictModeNo {
		warnOnce(stderr, "Warning: StrictHostKeyChecking is disabled, hosts keys are not verified (MITM risk)")
		return ssh.InsecureIgnoreHostKey(), nil
	}

	files := resolveKnownHostsFiles(opts.UserKnownHostsFile)

	// 所有文件都不存在、且 strict=yes 时，默认使用 accept-new 行为（空回调→接受一切）
	var base ssh.HostKeyCallback
	if len(files) > 0 {
		var err error
		base, err = knownhosts.New(files...)
		if err != nil {
			warnOnce(stderr, fmt.Sprintf("Warning: known_hosts: %v (proceed without host key verification)", err))
			return ssh.InsecureIgnoreHostKey(), nil
		}
	}

	strict := opts.StrictHostKeyChecking == StrictModeYes

	return func(hostname string, remote net.Addr, key ssh.PublicKey) error {
		if base == nil {
			// 没有 known_hosts 文件，首次连接自动接受
			if strict {
				return fmt.Errorf("no known_hosts file and StrictHostKeyChecking=yes: host key for %q is unknown", hostname)
			}
			// accept-new 模式：无文件时自动接受
			return nil
		}

		err := base(hostname, remote, key)
		if err == nil {
			// 已知主机且匹配
			return nil
		}

		// 判断是否为未知主机（KeyError.Want 为空）
		var keyErr *knownhosts.KeyError
		if errors.As(err, &keyErr) && len(keyErr.Want) == 0 {
			if strict {
				return fmt.Errorf("host key for %q is unknown (StrictHostKeyChecking=yes)", hostname)
			}
			// accept-new：接受并尝试追加
			if err := appendKnownHostsKey(files, hostname, key); err != nil {
				warnOnce(stderr, fmt.Sprintf("Warning: failed to update known_hosts: %v", err))
			}
			return nil
		}

		// 主机密钥变更（可能被中间人攻击）
		return fmt.Errorf("host key mismatch for %q: %v. If you trust this host, remove the offending key from known_hosts", hostname, err)
	}, nil
}

// resolveKnownHostsFiles 展开 known_hosts 文件路径，只返回存在的文件。
func resolveKnownHostsFiles(path string) []string {
	if path == "" {
		path = "~/.ssh/known_hosts"
	}
	path = expandHome(path)
	var files []string
	for _, candidate := range []string{path, expandHome("~/.ssh/known_hosts2")} {
		if _, err := os.Stat(candidate); err == nil {
			files = append(files, candidate)
		}
	}
	return files
}

// appendKnownHostsKey 将主机密钥追加到 known_hosts 文件。
// 写入第一条已知的文件路径，若都不存在则创建默认路径。
func appendKnownHostsKey(files []string, hostname string, key ssh.PublicKey) error {
	line := knownhosts.Line([]string{hostname}, key)
	target := ""
	if len(files) > 0 {
		target = files[0]
	} else {
		home, err := os.UserHomeDir()
		if err != nil {
			return err
		}
		dir := filepath.Join(home, ".ssh")
		if err := os.MkdirAll(dir, 0700); err != nil {
			return err
		}
		target = filepath.Join(dir, "known_hosts")
	}
	f, err := os.OpenFile(target, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := f.WriteString(line + "\n"); err != nil {
		return err
	}
	return nil
}

// expandHome 展开 ~ 前缀为用户 home 目录。
func expandHome(p string) string {
	if !strings.HasPrefix(p, "~") {
		return p
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return p
	}
	return home + p[1:]
}

// 包级警告去重（避免重复打印已知主机警告）
var (
	warnedMessages = make(map[string]bool)
)

func warnOnce(w io.Writer, msg string) {
	if warnedMessages[msg] {
		return
	}
	warnedMessages[msg] = true
	fmt.Fprintln(w, msg)
}

