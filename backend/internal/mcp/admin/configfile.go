// Package admin 提供 MCP 配置的管理能力（读/写/增删改/启停/热重载）。
//
// 该包被 CLI（aicli mcp ...）、runtime-server HTTP API 与 aicli 微型 Web 客户端
// 共用，保证三处读写的是同一份 mcp.yaml 且行为一致。
package admin

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/wwsheng009/ai-agent-runtime/internal/mcp/config"
)

const (
	// DefaultTimeoutSeconds 新增 MCP 的默认超时（秒）
	DefaultTimeoutSeconds = 30
	// DefaultMaxRetry 新增 MCP 的默认重试次数
	DefaultMaxRetry = 3
)

// ValidationError 表示请求内容不合法（HTTP 层映射为 400）。
type ValidationError struct{ Message string }

func (e *ValidationError) Error() string { return e.Message }

// NotFoundError 表示目标 MCP 不存在（HTTP 层映射为 404）。
type NotFoundError struct{ Message string }

func (e *NotFoundError) Error() string { return e.Message }

func invalidf(format string, args ...interface{}) error {
	return &ValidationError{Message: fmt.Sprintf(format, args...)}
}

func notFoundf(format string, args ...interface{}) error {
	return &NotFoundError{Message: fmt.Sprintf(format, args...)}
}

// UpsertRequest 新增/更新 MCP 的请求体。
//
// 指针字段用于区分「未提供」与「显式置空」：nil 表示保持原值（新增时用默认值）。
type UpsertRequest struct {
	Name             string                          `json:"name"`
	Description      *string                         `json:"description,omitempty"`
	Type             string                          `json:"type,omitempty"`
	Command          string                          `json:"command,omitempty"`
	Args             []string                        `json:"args,omitempty"`
	URL              string                          `json:"url,omitempty"`
	Env              map[string]string               `json:"env,omitempty"`
	Headers          map[string]string               `json:"headers,omitempty"`
	Enabled          *bool                           `json:"enabled,omitempty"`
	TrustLevel       string                          `json:"trustLevel,omitempty"`
	TimeoutSeconds   *int                            `json:"timeoutSeconds,omitempty"`
	MaxParallelCalls *int                            `json:"maxParallelCalls,omitempty"`
	Tools            map[string]config.MCPToolConfig `json:"tools,omitempty"`
	// Auth 认证配置：nil = 保持既有；Type 为空 = 清除认证；否则整体替换。
	Auth *config.MCPAuthConfig `json:"auth,omitempty"`
}

// NormalizeTransportType 归一化传输类型别名。
func NormalizeTransportType(transportType string) string {
	normalized := strings.ToLower(strings.TrimSpace(transportType))
	switch normalized {
	case "streamablehttp", "streamable-http", "streamable_http", "http":
		return "streamable"
	case "ws":
		return "websocket"
	default:
		return normalized
	}
}

// IsURLTransport 判断传输类型是否使用 URL（而非本地命令）。
func IsURLTransport(transportType string) bool {
	switch NormalizeTransportType(transportType) {
	case "sse", "websocket", "streamable":
		return true
	default:
		return false
	}
}

// EnsureFile 确保配置文件存在（不存在时创建空配置并补齐父目录）。
func EnsureFile(path string) error {
	path = strings.TrimSpace(path)
	if path == "" {
		return fmt.Errorf("MCP 配置文件路径为空")
	}
	if _, err := os.Stat(path); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("读取 MCP 配置失败: %w", err)
	}
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("创建 MCP 配置目录失败: %w", err)
		}
	}
	return atomicWriteFile(path, []byte("mcpServers: {}\n"))
}

// LoadFile 读取配置文件；文件不存在时自动创建空配置。
func LoadFile(path string) (*config.Config, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, fmt.Errorf("MCP 配置文件路径为空")
	}
	if err := EnsureFile(path); err != nil {
		return nil, err
	}
	cfg, err := config.NewLoader(path).Load()
	if err != nil {
		return nil, fmt.Errorf("加载 MCP 配置失败: %w", err)
	}
	return cfg, nil
}

// SaveFile 原子写入配置文件（根据扩展名选择 YAML/JSON）。
func SaveFile(path string, cfg *config.Config) error {
	if cfg == nil {
		return fmt.Errorf("配置不能为空")
	}
	var (
		data []byte
		err  error
	)
	if strings.HasSuffix(strings.ToLower(path), ".json") {
		data, err = json.MarshalIndent(cfg, "", "  ")
	} else {
		var buf bytes.Buffer
		encoder := yaml.NewEncoder(&buf)
		encoder.SetIndent(2)
		if err = encoder.Encode(cfg); err == nil {
			err = encoder.Close()
		}
		data = buf.Bytes()
	}
	if err != nil {
		return fmt.Errorf("序列化 MCP 配置失败: %w", err)
	}
	if err := atomicWriteFile(path, data); err != nil {
		return fmt.Errorf("写入 MCP 配置失败: %w", err)
	}
	return nil
}

// BuildConfig 根据请求构建（或更新）单个 MCP 配置项。
//
// existing 为 nil 表示新增；非 nil 表示在既有配置上做增量更新。
func BuildConfig(req UpsertRequest, existing *config.MCPConfig) (config.MCPConfig, error) {
	name := strings.TrimSpace(req.Name)
	if name == "" {
		return config.MCPConfig{}, invalidf("MCP 名称不能为空")
	}

	mcpCfg := config.MCPConfig{}
	if existing != nil {
		mcpCfg = *existing
		mcpCfg.Args = append([]string(nil), existing.Args...)
		mcpCfg.Env = cloneStringMap(existing.Env)
		mcpCfg.Tools = cloneToolConfigs(existing.Tools)
	}

	transportType := req.Type
	if strings.TrimSpace(transportType) == "" {
		transportType = mcpCfg.Type
	}
	if strings.TrimSpace(transportType) == "" {
		transportType = "stdio"
	}
	transportType = NormalizeTransportType(transportType)
	switch {
	case transportType == "stdio":
	case IsURLTransport(transportType):
	default:
		return config.MCPConfig{}, invalidf("不支持的传输类型: %s（支持: stdio, sse, websocket, streamable）", req.Type)
	}
	mcpCfg.Name = name
	mcpCfg.Type = transportType

	if req.Description != nil {
		mcpCfg.Description = *req.Description
	}
	if strings.TrimSpace(req.TrustLevel) != "" {
		level := config.MCPTrustLevel(strings.ToLower(strings.TrimSpace(req.TrustLevel)))
		switch level {
		case config.MCPTrustLevelLocal, config.MCPTrustLevelTrustedRemote, config.MCPTrustLevelUntrusted:
			mcpCfg.TrustLevel = level
		default:
			return config.MCPConfig{}, invalidf("无效的 trustLevel '%s'，支持: local, trusted_remote, untrusted_remote", req.TrustLevel)
		}
	}
	if req.TimeoutSeconds != nil {
		if *req.TimeoutSeconds < 1 {
			return config.MCPConfig{}, invalidf("超时时间不能小于 1 秒")
		}
		mcpCfg.Timeout = config.Duration{Duration: time.Duration(*req.TimeoutSeconds) * time.Second}
	}
	if req.MaxParallelCalls != nil {
		if *req.MaxParallelCalls < 0 {
			return config.MCPConfig{}, invalidf("maxParallelCalls 不能为负数")
		}
		mcpCfg.MaxParallelCalls = *req.MaxParallelCalls
	}
	if req.Tools != nil {
		merged := cloneToolConfigs(mcpCfg.Tools)
		if merged == nil {
			merged = map[string]config.MCPToolConfig{}
		}
		for toolName, toolCfg := range req.Tools {
			key := strings.TrimSpace(toolName)
			if key == "" {
				return config.MCPConfig{}, invalidf("工具级配置的工具名不能为空")
			}
			if toolCfg.Enabled == nil {
				// 未显式给值 = 保持既有配置。
				continue
			}
			enabled := *toolCfg.Enabled
			merged[key] = config.MCPToolConfig{Enabled: &enabled}
		}
		if len(merged) == 0 {
			merged = nil
		}
		mcpCfg.Tools = merged
	}

	if transportType == "stdio" {
		command := strings.TrimSpace(req.Command)
		if command == "" {
			command = strings.TrimSpace(mcpCfg.Command)
		}
		if command == "" {
			return config.MCPConfig{}, invalidf("stdio 类型的 MCP 需要指定 command")
		}
		mcpCfg.Command = command
		if req.Args != nil {
			mcpCfg.Args = append([]string(nil), req.Args...)
		}
		mcpCfg.URL = ""
	} else {
		url := strings.TrimSpace(req.URL)
		if url == "" {
			url = strings.TrimSpace(mcpCfg.URL)
		}
		if url == "" {
			return config.MCPConfig{}, invalidf("%s 类型的 MCP 需要指定 url", transportType)
		}
		mcpCfg.URL = url
		mcpCfg.Command = ""
		mcpCfg.Args = nil
	}

	if req.Env != nil {
		// 与 headers 一致：增量合并而不是整体替换，避免 Update 时静默丢掉已有 env。
		merged := cloneStringMap(mcpCfg.Env)
		if merged == nil {
			merged = map[string]string{}
		}
		for key, value := range req.Env {
			merged[key] = value
		}
		mcpCfg.Env = merged
	}
	if mcpCfg.Env == nil {
		mcpCfg.Env = map[string]string{}
	}
	if req.Headers != nil {
		mcpCfg.Env = applyHeaders(mcpCfg.Env, req.Headers)
	}

	if req.Auth != nil {
		if strings.TrimSpace(req.Auth.Type) == "" {
			mcpCfg.Auth = nil
		} else {
			authCfg := *req.Auth
			if authCfg.Scopes != nil {
				authCfg.Scopes = append([]string(nil), req.Auth.Scopes...)
			}
			mcpCfg.Auth = &authCfg
		}
	}

	enabled := true
	if existing != nil {
		enabled = existing.IsEnabled()
	}
	if req.Enabled != nil {
		enabled = *req.Enabled
	}
	mcpCfg.Enabled = enabled
	mcpCfg.Disabled = !enabled

	if mcpCfg.Timeout.Duration <= 0 {
		mcpCfg.Timeout = config.Duration{Duration: DefaultTimeoutSeconds * time.Second}
	}
	if existing == nil && mcpCfg.MaxRetry == 0 {
		mcpCfg.MaxRetry = DefaultMaxRetry
	}

	if err := config.ValidateConfig(&config.Config{
		MCPServers: map[string]config.MCPConfig{name: mcpCfg},
	}); err != nil {
		return config.MCPConfig{}, err
	}
	return mcpCfg, nil
}

// applyHeaders 把请求头映射为 HEADER_* 环境变量（先清空已有 HEADER_* 再写入）。
func applyHeaders(base map[string]string, headers map[string]string) map[string]string {
	if base == nil {
		base = map[string]string{}
	}
	for key := range base {
		if strings.HasPrefix(key, "HEADER_") {
			delete(base, key)
		}
	}
	for key, value := range headers {
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		base["HEADER_"+key] = value
	}
	return base
}

func cloneStringMap(src map[string]string) map[string]string {
	if src == nil {
		return nil
	}
	dst := make(map[string]string, len(src))
	for key, value := range src {
		dst[key] = value
	}
	return dst
}

// cloneToolConfigs 深拷贝工具级配置，避免 BuildConfig 改动调用方持有的 map。
func cloneToolConfigs(src map[string]config.MCPToolConfig) map[string]config.MCPToolConfig {
	if src == nil {
		return nil
	}
	dst := make(map[string]config.MCPToolConfig, len(src))
	for key, value := range src {
		entry := config.MCPToolConfig{}
		if value.Enabled != nil {
			enabled := *value.Enabled
			entry.Enabled = &enabled
		}
		dst[key] = entry
	}
	return dst
}

func atomicWriteFile(path string, data []byte) error {
	if err := guardNonTempWriteInTests(path); err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if dir == "" {
		dir = "."
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".mcp-config-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	_ = os.Chmod(tmpName, 0o644)
	return os.Rename(tmpName, path)
}

// guardNonTempWriteInTests 阻止测试进程把 MCP 配置写到临时目录之外。
//
// 背景：MCP 配置解析会优先命中 ./.aicli/mcp.yaml、~/.aicli/mcp.yaml 等真实路径，
// 一旦某个用例（或它间接触发的热重载/规范化保存）在测试里沿真实路径落盘，就会
// 静默改写开发者本机的工作区/主目录配置（历史问题：注释与自定义字段被规范化重写）。
// 测试一律应使用 t.TempDir()；命中该保护时会返回错误而不是写真实文件。
func guardNonTempWriteInTests(path string) error {
	if !testing.Testing() {
		return nil
	}
	if isWithinTempDir(path) {
		return nil
	}
	return fmt.Errorf("拒绝在测试进程中写入非临时目录的 MCP 配置: %s（请改用 t.TempDir()）", path)
}

func isWithinTempDir(path string) bool {
	abs, err := filepath.Abs(strings.TrimSpace(path))
	if err != nil {
		return false
	}
	roots := []string{os.TempDir()}
	for _, key := range []string{"TMPDIR", "TMP", "TEMP"} {
		if value := strings.TrimSpace(os.Getenv(key)); value != "" {
			roots = append(roots, value)
		}
	}
	for _, root := range roots {
		if strings.TrimSpace(root) == "" {
			continue
		}
		rootAbs, err := filepath.Abs(root)
		if err != nil {
			continue
		}
		rel, err := filepath.Rel(rootAbs, abs)
		if err != nil {
			continue
		}
		if rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))) {
			return true
		}
	}
	return false
}
