package config

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

type MCPTrustLevel string

const (
	MCPTrustLevelLocal         MCPTrustLevel = "local"
	MCPTrustLevelTrustedRemote MCPTrustLevel = "trusted_remote"
	MCPTrustLevelUntrusted     MCPTrustLevel = "untrusted_remote"
)

// Config 表示 MCP 配置
type Config struct {
	MCPServers map[string]MCPConfig `yaml:"mcpServers" json:"mcpServers"`
	Global     GlobalConfig         `yaml:"global" json:"global"`
}

// Duration 支持字符串形式的 time.Duration
type Duration struct {
	time.Duration
}

// UnmarshalJSON 实现 JSON 解析
func (d *Duration) UnmarshalJSON(b []byte) error {
	var v interface{}
	if err := json.Unmarshal(b, &v); err != nil {
		return err
	}
	switch value := v.(type) {
	case float64:
		d.Duration = time.Duration(value)
	case string:
		var err error
		d.Duration, err = time.ParseDuration(value)
		if err != nil {
			return err
		}
	default:
		return fmt.Errorf("invalid duration")
	}
	return nil
}

// UnmarshalYAML 实现 YAML 解析
func (d *Duration) UnmarshalYAML(unmarshal func(interface{}) error) error {
	var v interface{}
	if err := unmarshal(&v); err != nil {
		return err
	}
	switch value := v.(type) {
	case float64:
		d.Duration = time.Duration(value)
	case int:
		d.Duration = time.Duration(value)
	case string:
		var err error
		d.Duration, err = time.ParseDuration(value)
		if err != nil {
			return err
		}
	default:
		return fmt.Errorf("invalid duration")
	}
	return nil
}

// MarshalJSON 实现 JSON 编码
func (d Duration) MarshalJSON() ([]byte, error) {
	return json.Marshal(d.Duration.String())
}

// MarshalYAML 实现 YAML 编码
func (d Duration) MarshalYAML() (interface{}, error) {
	return d.Duration.String(), nil
}

// GlobalConfig 全局配置
type GlobalConfig struct {
	HealthCheckInterval Duration             `yaml:"healthCheckInterval" json:"healthCheckInterval"`
	ConnectTimeout      Duration             `yaml:"connectTimeout" json:"connectTimeout"`
	HealthCheck         MCPHealthCheckConfig `yaml:"healthCheck,omitempty" json:"healthCheck,omitempty"`
}

// MCPConfig 表示单个 MCP Server 的配置
type MCPConfig struct {
	Name             string                `yaml:"name" json:"name"`
	Description      string                `yaml:"description" json:"description"`
	Type             string                `yaml:"type" json:"type"` // stdio | sse | websocket | streamable
	TrustLevel       MCPTrustLevel         `yaml:"trustLevel,omitempty" json:"trustLevel,omitempty"`
	MaxParallelCalls int                   `yaml:"maxParallelCalls,omitempty" json:"maxParallelCalls,omitempty"`
	Command          string                `yaml:"command" json:"command"`   // 启动命令（stdio）
	Args             []string              `yaml:"args" json:"args"`         // 命令参数
	URL              string                `yaml:"url" json:"url"`           // 连接URL（sse/ws）
	Env              map[string]string     `yaml:"env" json:"env"`           // 环境变量
	Enabled          bool                  `yaml:"enabled" json:"enabled"`   // 是否启用
	Disabled         bool                  `yaml:"disabled" json:"disabled"` // 是否禁用（与 enabled 相反，用于兼容 MCP 官方格式）
	Timeout          Duration              `yaml:"timeout" json:"timeout"`   // 超时时间
	MaxRetry         int                   `yaml:"maxRetry" json:"maxRetry"` // 最大重试次数
	HealthCheck      *MCPHealthCheckConfig `yaml:"healthCheck,omitempty" json:"healthCheck,omitempty"`
	// Tools 工具级启停配置（key = 原始工具名；缺省条目 = 启用）。
	Tools map[string]MCPToolConfig `yaml:"tools,omitempty" json:"tools,omitempty"`
}

// MCPToolConfig 单个 MCP 工具的配置。
type MCPToolConfig struct {
	// Enabled 显式启停该工具；nil 表示未配置（默认启用）。
	Enabled *bool `yaml:"enabled,omitempty" json:"enabled,omitempty"`
}

// MCPHealthCheckConfig MCP 健康检查配置
type MCPHealthCheckConfig struct {
	Tools     []string                          `yaml:"tools,omitempty" json:"tools,omitempty"`
	ToolArgs  map[string]map[string]interface{} `yaml:"toolArgs,omitempty" json:"toolArgs,omitempty"`
	Resources []string                          `yaml:"resources,omitempty" json:"resources,omitempty"`
}

// IsEnabled 检查是否启用（支持 disabled 字段反向）
func (m *MCPConfig) IsEnabled() bool {
	// 如果有 disabled 字段且为 true，则禁用
	if m.Disabled {
		return false
	}
	return m.Enabled
}

// IsToolEnabled 返回工具是否被配置启用；未配置或未显式设置时默认启用。
func (m *MCPConfig) IsToolEnabled(toolName string) bool {
	if m == nil {
		return true
	}
	key := strings.TrimSpace(toolName)
	if key == "" {
		return true
	}
	entry, ok := m.Tools[key]
	if !ok || entry.Enabled == nil {
		return true
	}
	return *entry.Enabled
}

// SetToolEnabled 设置工具级启停；enabled=true 时移除显式禁用条目（默认即启用）。
func (m *MCPConfig) SetToolEnabled(toolName string, enabled bool) {
	if m == nil {
		return
	}
	key := strings.TrimSpace(toolName)
	if key == "" {
		return
	}
	if enabled {
		if len(m.Tools) == 0 {
			return
		}
		delete(m.Tools, key)
		if len(m.Tools) == 0 {
			m.Tools = nil
		}
		return
	}
	if m.Tools == nil {
		m.Tools = map[string]MCPToolConfig{}
	}
	disabled := false
	m.Tools[key] = MCPToolConfig{Enabled: &disabled}
}

func (m *MCPConfig) ResolvedTrustLevel() MCPTrustLevel {
	if m == nil {
		return MCPTrustLevelUntrusted
	}
	if m.TrustLevel != "" {
		return m.TrustLevel
	}
	switch m.Type {
	case "stdio":
		return MCPTrustLevelLocal
	default:
		return MCPTrustLevelUntrusted
	}
}

// IsStreamableHTTPTransport 判断是否为 Streamable HTTP 传输类型。
//
// 兼容 "streamable" / "streamableHttp" / "streamable-http" /
// "streamable_http" / "http" 等常见写法。
func IsStreamableHTTPTransport(transportType string) bool {
	switch strings.ToLower(strings.TrimSpace(transportType)) {
	case "streamable", "streamablehttp", "streamable-http", "streamable_http", "http":
		return true
	default:
		return false
	}
}

func (m *MCPConfig) ExecutionMode() string {
	if m == nil {
		return "remote_mcp"
	}
	switch m.Type {
	case "stdio":
		return "local_mcp"
	default:
		return "remote_mcp"
	}
}

// MCPStatus MCP 状态
type MCPStatus struct {
	Name             string        `json:"name"`
	Type             string        `json:"type"`
	TrustLevel       MCPTrustLevel `json:"trustLevel,omitempty"`
	ExecutionMode    string        `json:"executionMode,omitempty"`
	MaxParallelCalls int           `json:"maxParallelCalls,omitempty"`
	Enabled          bool          `json:"enabled"`
	Connected        bool          `json:"connected"`
	ToolCount        int           `json:"toolCount"`
	LastError        string        `json:"lastError,omitempty"`
	LastConnect      time.Time     `json:"lastConnect,omitempty"`
	HealthCheck      time.Time     `json:"healthCheck,omitempty"`
}

// ToolInfo 工具信息
type ToolInfo struct {
	Name             string                 `json:"name"`
	Description      string                 `json:"description"`
	MCPName          string                 `json:"mcpName"`
	MaxParallelCalls int                    `json:"maxParallelCalls,omitempty"`
	Enabled          bool                   `json:"enabled"`
	InputSchema      map[string]interface{} `json:"inputSchema"`
}
