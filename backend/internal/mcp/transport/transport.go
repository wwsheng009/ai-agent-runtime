package transport

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Transport 传输接口 - 封装官方 SDK 的 Transport
type Transport interface {
	// Type 返回传输类型
	Type() string
	// Config 返回传输配置
	Config() interface{}
	// ToMCPSdkTransport 转换为官方 SDK transport
	ToMCPSdkTransport(ctx context.Context) mcp.Transport
}

// Config 传输配置（用于兼容现有配置）
type Config struct {
	// Type 传输类型：stdio | sse | websocket
	Type string

	// Command 启动命令（stdio）
	Command string

	// Args 命令参数（stdio）
	Args []string

	// URL 连接 URL（sse/websocket）
	URL string

	// Env 环境变量
	Env map[string]string

	// WorkingDir 工作目录（stdio）
	WorkingDir string

	// WebSocket 专用配置
	WSHeaders http.Header `json:"wsHeaders,omitempty"`
}

// StdioTransport stdio 传输封装
type StdioTransport struct {
	cfg     *Config
	emitter lifecycleEmitter
}

// NewTransport 创建传输实例（兼容现有接口）
func NewTransport(cfg *Config) (Transport, error) {
	switch cfg.Type {
	case "stdio":
		return NewStdioTransport(cfg), nil
	case "sse":
		return &SSETransport{cfg: cfg}, nil
	case "websocket", "ws":
		return NewWebSocketTransport(cfg), nil
	default:
		return nil, fmt.Errorf("不支持的传输类型: %s", cfg.Type)
	}
}

// NewStdioTransport 创建 stdio 传输
func NewStdioTransport(cfg *Config) *StdioTransport {
	return &StdioTransport{cfg: cfg}
}

// Type 返回传输类型
func (t *StdioTransport) Type() string {
	return "stdio"
}

// Config 返回传输配置
func (t *StdioTransport) Config() interface{} {
	return t.cfg
}

func (t *StdioTransport) AddLifecycleObserver(observer LifecycleObserver) {
	t.emitter.AddLifecycleObserver(observer)
}

// ToMCPSdkTransport 转换为官方 SDK 的 CommandTransport
func (t *StdioTransport) ToMCPSdkTransport(ctx context.Context) mcp.Transport {
	cmd := exec.CommandContext(ctx, t.cfg.Command, t.cfg.Args...)

	// 设置工作目录
	if t.cfg.WorkingDir != "" {
		absDir, err := filepath.Abs(t.cfg.WorkingDir)
		if err == nil {
			cmd.Dir = absDir
		}
	} else {
		// 如果没有指定工作目录，使用项目根目录
		wd, err := os.Getwd()
		if err == nil {
			cmd.Dir = wd
		}
	}

	// 设置环境变量
	if len(t.cfg.Env) > 0 {
		env := []string{}
		// 先添加当前环境变量
		for _, e := range os.Environ() {
			env = append(env, e)
		}
		// 添加自定义环境变量
		for k, v := range t.cfg.Env {
			env = append(env, fmt.Sprintf("%s=%s", k, v))
		}
		cmd.Env = env
	}

	return newObservedMCPTransport("stdio", strings.TrimSpace(t.cfg.Command), &mcp.CommandTransport{
		Command: cmd,
	}, &t.emitter)
}

// SSETransport SSE 传输封装
type SSETransport struct {
	cfg     *Config
	emitter lifecycleEmitter
}

// NewSSETransport 创建 SSE 传输
func NewSSETransport(cfg *Config) *SSETransport {
	return &SSETransport{cfg: cfg}
}

// Type 返回传输类型
func (t *SSETransport) Type() string {
	return "sse"
}

// Config 返回传输配置
func (t *SSETransport) Config() interface{} {
	return t.cfg
}

func (t *SSETransport) AddLifecycleObserver(observer LifecycleObserver) {
	t.emitter.AddLifecycleObserver(observer)
}

// ToMCPSdkTransport 转换为官方 SDK 的 SSEClientTransport
func (t *SSETransport) ToMCPSdkTransport(ctx context.Context) mcp.Transport {
	return newObservedMCPTransport("sse", strings.TrimSpace(t.cfg.URL), &mcp.SSEClientTransport{
		Endpoint: t.cfg.URL,
	}, &t.emitter)
}
