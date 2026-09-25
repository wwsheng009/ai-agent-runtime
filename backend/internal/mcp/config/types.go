package config

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
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
	Name        string `yaml:"name" json:"name"`
	Description string `yaml:"description" json:"description"`
	// EnvError 记录加载期环境变量插值的缺失项（仅内存态，不落盘）。
	// 非空时该 server 不参与连接，错误会带进运行时状态。
	EnvError         string            `yaml:"-" json:"-"`
	Type             string            `yaml:"type" json:"type"` // stdio | sse | websocket | streamable
	TrustLevel       MCPTrustLevel     `yaml:"trustLevel,omitempty" json:"trustLevel,omitempty"`
	MaxParallelCalls int               `yaml:"maxParallelCalls,omitempty" json:"maxParallelCalls,omitempty"`
	Command          string            `yaml:"command" json:"command"` // 启动命令（stdio）
	Args             []string          `yaml:"args" json:"args"`       // 命令参数
	URL              string            `yaml:"url" json:"url"`         // 连接URL（sse/ws）
	Env              map[string]string `yaml:"env" json:"env"`         // 环境变量
	// Headers 远程传输的 HTTP 头（streamable / sse / websocket）。
	// 优先于 Env：仅当 Headers 为空时才回退把 Env 整体当作 HTTP 头
	// （历史行为，见 transport.buildHeadersFromEnv）。
	Headers map[string]string `yaml:"headers,omitempty" json:"headers,omitempty"`
	// WorkingDir stdio 子进程工作目录；为空时由传输层回退到进程当前目录。
	WorkingDir  string                `yaml:"workingDir,omitempty" json:"workingDir,omitempty"`
	Enabled     bool                  `yaml:"enabled" json:"enabled"`   // 是否启用
	Disabled    bool                  `yaml:"disabled" json:"disabled"` // 是否禁用（与 enabled 相反，用于兼容 MCP 官方格式）
	Timeout     Duration              `yaml:"timeout" json:"timeout"`   // 超时时间
	MaxRetry    int                   `yaml:"maxRetry" json:"maxRetry"` // 最大重试次数
	HealthCheck *MCPHealthCheckConfig `yaml:"healthCheck,omitempty" json:"healthCheck,omitempty"`
	// Tools 工具级启停配置（key = 原始工具名；缺省条目 = 启用）。
	Tools map[string]MCPToolConfig `yaml:"tools,omitempty" json:"tools,omitempty"`
	// Auth 远程 MCP 的认证配置（目前支持 oauth）；nil 表示无认证。
	Auth *MCPAuthConfig `yaml:"auth,omitempty" json:"auth,omitempty"`
	// TokenSource 仅内存态：manager 连接前注入的 OAuth 会话。
	// 不落盘、不序列化，避免把运行时凭据写回配置文件。
	TokenSource AccessTokenProvider `yaml:"-" json:"-"`
}

// AccessTokenProvider 是运行时注入的访问令牌来源（OAuth 会话实现）。
//
// transport 与 config 各自声明同名接口：Go 的接口赋值按方法集结构匹配，
// 这样 transport 无需 import config，也不会形成环。
type AccessTokenProvider interface {
	// AccessToken 返回当前可用令牌（临近过期时内部自动刷新）。
	AccessToken(ctx context.Context) (string, error)
	// ForceRefresh 在收到 401/403 后强制刷新一次。
	ForceRefresh(ctx context.Context) (string, error)
	// NeedsAuth 返回是否处于「需要用户重新授权」状态，以及可行动的原因。
	NeedsAuth() (bool, string)
}

// MCPAuthConfig 描述远端 MCP 的认证方式。
//
// 支持两种 YAML 写法：`auth: oauth`（简写）与结构化写法：
//
//	auth:
//	  type: oauth
//	  clientId: xxx
//	  scopes: [read, write]
//	  callbackPort: 3334
//	  authorizationServer: https://auth.example.com
type MCPAuthConfig struct {
	// Type 认证类型，目前仅支持 "oauth"。
	Type string `yaml:"type" json:"type"`
	// ClientID 预注册客户端 ID；为空时尝试动态客户端注册（RFC 7591）。
	ClientID string `yaml:"clientId,omitempty" json:"clientId,omitempty"`
	// ClientSecret 机密客户端密钥；公共客户端留空。
	ClientSecret string `yaml:"clientSecret,omitempty" json:"clientSecret,omitempty"`
	// Scopes 请求的 scope；为空时使用授权服务器 metadata 的建议值。
	Scopes []string `yaml:"scopes,omitempty" json:"scopes,omitempty"`
	// CallbackPort 本地回调端口；0 表示随机端口。
	CallbackPort int `yaml:"callbackPort,omitempty" json:"callbackPort,omitempty"`
	// AuthorizationServer 覆盖自动发现得到的授权服务器地址。
	AuthorizationServer string `yaml:"authorizationServer,omitempty" json:"authorizationServer,omitempty"`
	// Resource 覆盖 RFC 8707 resource 参数（默认取 server URL）。
	Resource string `yaml:"resource,omitempty" json:"resource,omitempty"`
}

// UnmarshalYAML 同时接受标量简写（`auth: oauth`）与结构化映射。
func (a *MCPAuthConfig) UnmarshalYAML(value *yaml.Node) error {
	if value == nil {
		return nil
	}
	if value.Kind == yaml.ScalarNode {
		var scalar string
		if err := value.Decode(&scalar); err != nil {
			return err
		}
		a.Type = strings.TrimSpace(scalar)
		return nil
	}
	type rawAuth MCPAuthConfig
	var tmp rawAuth
	if err := value.Decode(&tmp); err != nil {
		return err
	}
	*a = MCPAuthConfig(tmp)
	return nil
}

// IsOAuth 判断该 server 是否启用 OAuth 认证。
func (m *MCPConfig) IsOAuth() bool {
	return m != nil && m.Auth != nil && strings.EqualFold(strings.TrimSpace(m.Auth.Type), "oauth")
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
	// RequiresAuth 表示该 server 配置了 OAuth 但缺少可用令牌（或令牌被拒绝），
	// 需要用户执行 `aicli mcp auth <name>`（chat 内为 `/mcp auth <name>`）重新授权。
	RequiresAuth bool      `json:"requiresAuth,omitempty"`
	LastConnect  time.Time `json:"lastConnect,omitempty"`
	HealthCheck  time.Time `json:"healthCheck,omitempty"`
	// 分层配置来源（§4.5 Step 1）：ConfigSource/ConfigPath 说明该 server 由哪一层
	// 配置文件提供；ShadowedSources 列出被它覆盖的同名低优先级定义，避免静默冲突。
	ConfigSource    string      `json:"configSource,omitempty"`
	ConfigPath      string      `json:"configPath,omitempty"`
	ShadowedSources []SourceRef `json:"shadowedSources,omitempty"`
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
