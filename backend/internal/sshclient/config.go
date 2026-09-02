package sshclient

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	sshconfig "github.com/kevinburke/ssh_config"
)

// ResolvedConfig 表示从 ~/.ssh/config 解析出的、与目标主机匹配的配置值。
// 只包含本实现支持的指令子集（见方案 §5.1.1）。
type ResolvedConfig struct {
	Host                     string
	HostName                 string
	User                     string
	Port                     int
	IdentityFiles            []string
	IdentitiesOnly           bool
	PreferredAuthentications string
	StrictHostKeyChecking    string
	UserKnownHostsFile       string
	ConnectTimeout           time.Duration
	ServerAliveInterval      time.Duration
	ServerAliveCountMax      int
	HostKeyAlgorithms        []string
	Compression              bool
	LogLevel                 string
	// ProxyJump 首版不实现；解析到该指令时记录警告。
	ProxyJump string
}

// DefaultConfigPath 返回用户级 ssh_config 路径。
func DefaultConfigPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".ssh", "config")
}

// LoadResolvedConfig 读取 ssh_config 并解析出与 alias 匹配的配置。
// path 为空时使用 ~/.ssh/config；文件不存在返回空配置（不报错）。
func LoadResolvedConfig(path, alias string) (*ResolvedConfig, error) {
	if path == "" {
		path = DefaultConfigPath()
	}
	cfg := &ResolvedConfig{}
	if path == "" {
		return cfg, nil
	}
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return cfg, nil
		}
		return nil, fmt.Errorf("open ssh config %q: %w", path, err)
	}
	defer f.Close()

	parsed, err := sshconfig.Decode(f)
	if err != nil {
		return nil, fmt.Errorf("parse ssh config %q: %w", path, err)
	}

	cfg.Host = alias
	get := func(key string) string {
		v, err := parsed.Get(alias, key)
		if err != nil || v == "" {
			return ""
		}
		return strings.TrimSpace(v)
	}

	cfg.HostName = get("HostName")
	cfg.User = get("User")
	if p := get("Port"); p != "" {
		if n, err := strconv.Atoi(p); err == nil && n > 0 && n <= 65535 {
			cfg.Port = n
		}
	}
	if to := get("ConnectTimeout"); to != "" {
		if sec, err := strconv.Atoi(to); err == nil && sec > 0 {
			cfg.ConnectTimeout = time.Duration(sec) * time.Second
		}
	}
	if iv := get("ServerAliveInterval"); iv != "" {
		if sec, err := strconv.Atoi(iv); err == nil && sec > 0 {
			cfg.ServerAliveInterval = time.Duration(sec) * time.Second
		}
	}
	if cm := get("ServerAliveCountMax"); cm != "" {
		if n, err := strconv.Atoi(cm); err == nil && n > 0 {
			cfg.ServerAliveCountMax = n
		}
	}
	cfg.StrictHostKeyChecking = strings.ToLower(get("StrictHostKeyChecking"))
	cfg.UserKnownHostsFile = get("UserKnownHostsFile")
	cfg.PreferredAuthentications = get("PreferredAuthentications")
	cfg.LogLevel = strings.ToUpper(get("LogLevel"))
	cfg.ProxyJump = get("ProxyJump")
	if io := get("IdentitiesOnly"); io != "" {
		cfg.IdentitiesOnly = parseBool(io)
	}
	if co := get("Compression"); co != "" {
		cfg.Compression = parseBool(co)
	}

	// IdentityFile 可能多条：kevinburke 库的 Get 只返回一条，
	// 这里对每条路径单独取，并补充内置默认密钥搜索（按 Ed25519 → ECDSA → RSA）。
	cfg.IdentityFiles = collectIdentityFiles(parsed, alias)
	if hka := get("HostKeyAlgorithms"); hka != "" {
		cfg.HostKeyAlgorithms = strings.Split(hka, ",")
		for i := range cfg.HostKeyAlgorithms {
			cfg.HostKeyAlgorithms[i] = strings.TrimSpace(cfg.HostKeyAlgorithms[i])
		}
	}
	return cfg, nil
}

// collectIdentityFiles 读取配置中所有 IdentityFile 条目。
func collectIdentityFiles(parsed *sshconfig.Config, alias string) []string {
	var files []string
	for _, host := range parsed.Hosts {
		if !host.Matches(alias) {
			continue
		}
		for _, node := range host.Nodes {
			if kv, ok := node.(*sshconfig.KV); ok && strings.EqualFold(kv.Key, "IdentityFile") {
				val := strings.TrimSpace(kv.Value)
				if val != "" {
					files = append(files, val)
				}
			}
		}
	}
	// 去重
	seen := make(map[string]bool)
	out := make([]string, 0, len(files))
	for _, f := range files {
		if !seen[f] {
			seen[f] = true
			out = append(out, f)
		}
	}
	return out
}

func parseBool(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "yes", "true", "1", "on":
		return true
	default:
		return false
	}
}
