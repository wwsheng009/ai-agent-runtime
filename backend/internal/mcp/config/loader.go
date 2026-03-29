package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Loader 配置加载器
type Loader struct {
	configPath string
	config     *Config
}

// NewLoader 创建配置加载器
func NewLoader(configPath string) *Loader {
	return &Loader{
		configPath: configPath,
	}
}

// Load 加载配置文件
func (l *Loader) Load() (*Config, error) {
	// 读取文件
	data, err := os.ReadFile(l.configPath)
	if err != nil {
		return nil, fmt.Errorf("读取配置文件失败 %s: %w", l.configPath, err)
	}

	// 解析配置
	var config Config
	ext := strings.ToLower(filepath.Ext(l.configPath))

	switch ext {
	case ".json":
		// 解析 JSON
		if err := json.Unmarshal(data, &config); err != nil {
			return nil, fmt.Errorf("解析配置文件失败: %w", err)
		}
	case ".yaml", ".yml":
		// 解析 YAML
		if err := yaml.Unmarshal(data, &config); err != nil {
			return nil, fmt.Errorf("解析配置文件失败: %w", err)
		}
	default:
		// 尝试自动检测（先尝试 JSON，再尝试 YAML）
		if err := json.Unmarshal(data, &config); err != nil {
			if err := yaml.Unmarshal(data, &config); err != nil {
				return nil, fmt.Errorf("无法解析配置文件（尝试了 JSON 和 YAML）: %w", err)
			}
		}
	}

	// 设置默认值
	l.setDefaults(&config)

	// 展开环境变量
	if err := l.expandEnvVars(&config); err != nil {
		return nil, fmt.Errorf("展开环境变量失败: %w", err)
	}

	// 验证配置
	if err := l.validate(&config); err != nil {
		return nil, fmt.Errorf("配置验证失败: %w", err)
	}

	l.config = &config
	return &config, nil
}

// GetConfig 获取已加载的配置
func (l *Loader) GetConfig() *Config {
	return l.config
}

// setDefaults 设置默认值
func (l *Loader) setDefaults(config *Config) {
	// 全局配置默认值
	if config.Global.HealthCheckInterval.Duration == 0 {
		config.Global.HealthCheckInterval.Duration = time.Minute
	}
	if config.Global.ConnectTimeout.Duration == 0 {
		config.Global.ConnectTimeout.Duration = 10 * time.Second
	}
	if !config.Global.AutoConnect {
		config.Global.AutoConnect = true // 默认启用自动连接
	}

	// 每个 MCP 的默认值
	for name, mcp := range config.MCPServers {
		mcp.Name = name
		if mcp.Type == "" {
			mcp.Type = "stdio" // 默认使用 stdio
		}
		if mcp.TrustLevel == "" {
			mcp.TrustLevel = mcp.ResolvedTrustLevel()
		}
		if mcp.Timeout.Duration == 0 {
			mcp.Timeout.Duration = 30 * time.Second
		}
		if mcp.MaxRetry == 0 {
			mcp.MaxRetry = 3
		}
		// 如果都没有启用字段，则默认启用
		if !mcp.Enabled && !mcp.Disabled {
			mcp.Enabled = true
		}
		config.MCPServers[name] = mcp
	}
}

// expandEnvVars 展开环境变量
func (l *Loader) expandEnvVars(config *Config) error {
	for name, mcp := range config.MCPServers {
		// 展开环境变量
		if mcp.URL != "" {
			mcp.URL = os.ExpandEnv(mcp.URL)
		}
		if mcp.Command != "" {
			mcp.Command = os.ExpandEnv(mcp.Command)
		}

		// 展开环境变量映射
		for key, value := range mcp.Env {
			mcp.Env[key] = os.ExpandEnv(value)
		}

		config.MCPServers[name] = mcp
	}
	return nil
}

// findConfigFile 查找配置文件
func findConfigFile(configPath string) (string, error) {
	// 如果提供了绝对路径，直接使用
	if filepath.IsAbs(configPath) {
		if _, err := os.Stat(configPath); err == nil {
			return configPath, nil
		}
	}

	// 检查相对路径
	paths := []string{
		configPath,
		filepath.Join(".", configPath),
		filepath.Join("~", ".aicli", configPath),
		filepath.Join("~", ".config", "aicli", configPath),
		"/etc/aicli/" + configPath,
	}

	for _, p := range paths {
		// 展开 ~
		if strings.HasPrefix(p, "~") {
			home, err := os.UserHomeDir()
			if err != nil {
				continue
			}
			p = filepath.Join(home, p[2:])
		}

		// 检查文件是否存在
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
	}

	return "", fmt.Errorf("找不到配置文件: %s", configPath)
}

// Validate 验证配置
func (l *Loader) validate(config *Config) error {
	// 验证传输类型
	validTypes := map[string]bool{
		"stdio":     true,
		"sse":       true,
		"websocket": true,
	}

	for name, mcp := range config.MCPServers {
		// 检查传输类型
		if !validTypes[mcp.Type] {
			return fmt.Errorf("无效的传输类型 '%s' (MCP: %s)，支持: stdio, sse, websocket", mcp.Type, name)
		}

		// stdio 类型必须有 command
		if mcp.Type == "stdio" && mcp.Command == "" {
			return fmt.Errorf("stdio 类型的 MCP 需要指定 command (MCP: %s)", name)
		}

		// sse/websocket 类型必须有 url
		if (mcp.Type == "sse" || mcp.Type == "websocket") && mcp.URL == "" {
			return fmt.Errorf("%s 类型的 MCP 需要指定 url (MCP: %s)", mcp.Type, name)
		}

		// 检查超时时间
		if mcp.Timeout.Duration < time.Second {
			return fmt.Errorf("超时时间不能小于 1 秒 (MCP: %s)", name)
		}

		// 检查重试次数
		if mcp.MaxRetry < 0 {
			return fmt.Errorf("重试次数不能为负数 (MCP: %s)", name)
		}
		switch mcp.ResolvedTrustLevel() {
		case MCPTrustLevelLocal, MCPTrustLevelTrustedRemote, MCPTrustLevelUntrusted:
		default:
			return fmt.Errorf("无效的 trustLevel '%s' (MCP: %s)，支持: local, trusted_remote, untrusted_remote", mcp.TrustLevel, name)
		}
	}

	return nil
}
