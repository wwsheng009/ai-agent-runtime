//go:build !win7compat

package transport

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/wwsheng009/ai-agent-runtime/internal/executor"
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

	// Headers 远程传输的 HTTP 头（streamable / sse / websocket）。
	// 优先于 Env；Env 仅在 Headers 为空时作为历史兼容的头部来源。
	Headers map[string]string

	// AccessToken 可选的 OAuth 访问令牌来源（streamable / sse）。
	// 非空且未配置静态 Authorization 头时，按需注入 Bearer 令牌并在 401 后刷新重试。
	AccessToken AccessTokenProvider

	// WorkingDir 工作目录（stdio）
	WorkingDir string

	// WebSocket 专用配置
	WSHeaders http.Header `json:"wsHeaders,omitempty"`
}

// StdioTransport stdio 传输封装
type StdioTransport struct {
	cfg     *Config
	emitter lifecycleEmitter

	// mu 保护 cmd / guard：ToMCPSdkTransport 在装配时写入，诊断与测试读取。
	mu sync.Mutex
	// cmd 是最近一次装配出的 stdio 命令（进程树守卫的根）。
	cmd *exec.Cmd
	// guard 是 stdio 进程树守卫（Windows: Job Object；Unix: 进程组）。
	guard *executor.ProcessGuard
	// stderr 是有界环形缓冲：连接失败时暴露子进程 stderr 尾部（计划 §5.3）。
	stderr *stderrTailBuffer
}

// NewTransport 创建传输实例（兼容现有接口）
func NewTransport(cfg *Config) (Transport, error) {
	switch NormalizeTransportType(cfg.Type) {
	case "stdio":
		return NewStdioTransport(cfg), nil
	case "sse":
		return &SSETransport{cfg: cfg}, nil
	case "websocket", "ws":
		return NewWebSocketTransport(cfg), nil
	case "streamable", "http":
		return NewStreamableTransport(cfg), nil
	default:
		return nil, fmt.Errorf("不支持的传输类型: %s", cfg.Type)
	}
}

// NormalizeTransportType 归一化传输类型别名。
//
// 兼容 "streamableHttp" / "streamable-http" / "streamable_http" 等写法，
// 统一返回 "streamable"；"ws" 统一返回 "websocket"。
func NormalizeTransportType(transportType string) string {
	normalized := strings.ToLower(strings.TrimSpace(transportType))
	switch normalized {
	case "streamablehttp", "streamable-http", "streamable_http":
		return "streamable"
	case "ws":
		return "websocket"
	default:
		return normalized
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
	// Windows 垫片（npx.cmd / uvx.cmd）需要 cmd.exe 包装后才能被 exec 执行；
	// 包装用的整条命令行由 resolveStdioCommand 拼好并经 SysProcAttr.CmdLine 下发。
	rc := resolveStdioCommand(t.cfg.Command, t.cfg.Args)
	cmd, guard, guardErr := newStdioCommandGuard(ctx, rc)
	if guardErr != nil {
		// 绑定失败只降级不阻断：仍然启动 server，但失去「父死子亡」兜底，
		// 因此必须留下可见诊断（计划 §4.11）。
		t.emitter.emitLifecycleEvent(TraceIDFromContext(ctx), "mcp.stdio.tree_guard_degraded", "stdio", "", map[string]interface{}{
			"target": strings.TrimSpace(t.cfg.Command),
			"error":  guardErr.Error(),
		})
	}

	// 子进程 stderr 默认会被 os/exec 接到 null device（go-sdk 的 CommandTransport
	// 不接管 Stderr），导致「进程没起来」时用户只看到 EOF。这里改接到有界环形
	// 缓冲，连接失败时由 client 拼进错误信息（计划 §5.3）。
	stderrTail := newStderrTailBuffer(stdioStderrTailLimit)
	cmd.Stderr = stderrTail

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

	t.mu.Lock()
	t.cmd = cmd
	t.guard = guard
	t.stderr = stderrTail
	t.mu.Unlock()

	inner := &mcp.CommandTransport{Command: cmd}
	target := strings.TrimSpace(t.cfg.Command)
	return newObservedMCPTransport(
		"stdio",
		target,
		newStdioTreeGuardTransport(inner, cmd, guard, target, &t.emitter),
		&t.emitter,
	)
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
	inner := &mcp.SSEClientTransport{
		Endpoint: t.cfg.URL,
	}
	headers := buildHeaders(t.cfg.Headers, t.cfg.Env)
	if len(headers) > 0 || t.cfg.AccessToken != nil {
		inner.HTTPClient = &http.Client{
			Transport: headerRoundTripper{base: http.DefaultTransport, headers: headers, provider: t.cfg.AccessToken},
		}
	}
	return newObservedMCPTransport("sse", strings.TrimSpace(t.cfg.URL), inner, &t.emitter)
}
