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
	// 与分层加载（layered.go）共用同一套读取/解析/默认值/校验语义，避免两套行为。
	// 注意：这里保持原始值不展开。配置文件可能在管理操作（add/enable/remove）
	// 中被读改写回；若在加载期就地展开，`${VAR}` 会被真实值（或空串）替换并
	// 落盘，既可能泄露密钥，也会丢失字面量。展开只由运行时入口显式调用
	// （manager.LoadConfig / CloneWithDefaults），见 ExpandEnv。
	config, err := loadConfigFile(l.configPath)
	if err != nil {
		return nil, err
	}
	l.config = config
	return config, nil
}

// unmarshalConfig 按扩展名解析配置文本；未知扩展名先试 JSON 再试 YAML。
func unmarshalConfig(data []byte, path string) (*Config, error) {
	var config Config
	ext := strings.ToLower(filepath.Ext(path))
	switch ext {
	case ".json":
		if err := json.Unmarshal(data, &config); err != nil {
			return nil, fmt.Errorf("解析配置文件失败: %w", err)
		}
	case ".yaml", ".yml":
		if err := yaml.Unmarshal(data, &config); err != nil {
			return nil, fmt.Errorf("解析配置文件失败: %w", err)
		}
	default:
		if err := json.Unmarshal(data, &config); err != nil {
			if err := yaml.Unmarshal(data, &config); err != nil {
				return nil, fmt.Errorf("无法解析配置文件（尝试了 JSON 和 YAML）: %w", err)
			}
		}
	}
	return &config, nil
}

// GetConfig 获取已加载的配置
func (l *Loader) GetConfig() *Config {
	return l.config
}

// setDefaults 设置默认值
func (l *Loader) setDefaults(config *Config) {
	ApplyDefaults(config)
}

// ApplyDefaults 补齐全局与每个 server 的默认值。
//
// 与文件加载共用同一套默认值语义，供内存配置入口（会话级 manager）复用，
// 避免「文件加载有默认值、内存加载没有」的两套行为。
func ApplyDefaults(config *Config) {
	if config == nil {
		return
	}
	// 全局配置默认值
	if config.Global.HealthCheckInterval.Duration == 0 {
		config.Global.HealthCheckInterval.Duration = time.Minute
	}
	if config.Global.ConnectTimeout.Duration == 0 {
		config.Global.ConnectTimeout.Duration = 10 * time.Second
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
		if mcp.MaxParallelCalls <= 0 {
			mcp.MaxParallelCalls = 1
		}
		// 如果都没有启用字段，则默认启用
		if !mcp.Enabled && !mcp.Disabled {
			mcp.Enabled = true
		}
		config.MCPServers[name] = mcp
	}
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
		"ws":        true,
	}

	for name, mcp := range config.MCPServers {
		if strings.TrimSpace(mcp.EnvError) != "" {
			// 插值缺失的配置结构可能不完整（如 URL 展开为空），
			// 交由运行时状态报错，不在这里阻断整份配置。
			continue
		}
		// 检查传输类型
		if !validTypes[mcp.Type] && !IsStreamableHTTPTransport(mcp.Type) {
			return fmt.Errorf("无效的传输类型 '%s' (MCP: %s)，支持: stdio, sse, websocket, streamable", mcp.Type, name)
		}

		// stdio 类型必须有 command
		if strings.EqualFold(strings.TrimSpace(mcp.Type), "stdio") && mcp.Command == "" {
			return fmt.Errorf("stdio 类型的 MCP 需要指定 command (MCP: %s)", name)
		}

		// sse/websocket/streamable 类型必须有 url
		if (strings.EqualFold(strings.TrimSpace(mcp.Type), "sse") ||
			strings.EqualFold(strings.TrimSpace(mcp.Type), "websocket") ||
			strings.EqualFold(strings.TrimSpace(mcp.Type), "ws") ||
			IsStreamableHTTPTransport(mcp.Type)) && mcp.URL == "" {
			return fmt.Errorf("%s 类型的 MCP 需要指定 url (MCP: %s)", mcp.Type, name)
		}

		// 认证配置：目前仅支持 oauth，且只适用于 streamable/sse 远程传输。
		if mcp.Auth != nil && strings.TrimSpace(mcp.Auth.Type) != "" {
			if !mcp.IsOAuth() {
				return fmt.Errorf("不支持的 auth 类型 '%s' (MCP: %s)，目前仅支持 oauth；静态凭证请用 headers", mcp.Auth.Type, name)
			}
			if !IsStreamableHTTPTransport(mcp.Type) && !strings.EqualFold(strings.TrimSpace(mcp.Type), "sse") {
				return fmt.Errorf("oauth 认证目前仅支持 streamable/sse 传输 (MCP: %s, type: %s)；其它传输请用 headers 配置静态凭证", name, mcp.Type)
			}
			if mcp.Auth.CallbackPort < 0 || mcp.Auth.CallbackPort > 65535 {
				return fmt.Errorf("auth.callbackPort 必须在 0-65535 之间 (MCP: %s)", name)
			}
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
		if mcp.MaxParallelCalls < 0 {
			return fmt.Errorf("maxParallelCalls cannot be negative (MCP: %s)", name)
		}
		for toolName := range mcp.Tools {
			if strings.TrimSpace(toolName) == "" {
				return fmt.Errorf("工具级配置的工具名不能为空 (MCP: %s)", name)
			}
		}
	}

	return nil
}

// ValidateConfig 校验配置内容（不依赖配置文件，供管理端/CLI 写入前校验）。
func ValidateConfig(config *Config) error {
	if config == nil {
		return fmt.Errorf("配置不能为空")
	}
	return (&Loader{}).validate(config)
}
