package client

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/mcp/config"
	"github.com/wwsheng009/ai-agent-runtime/internal/mcp/protocol"
	"github.com/wwsheng009/ai-agent-runtime/internal/mcp/transport"
	"github.com/wwsheng009/ai-agent-runtime/internal/pkg/logger"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Client MCP 客户端接口
type Client interface {
	// Connect 连接到 MCP Server
	Connect(ctx context.Context) error

	// Name 返回 MCP 名称
	Name() string

	// ListTools 列出所有工具
	ListTools(ctx context.Context) ([]*protocol.Tool, error)

	// CallTool 调用工具
	CallTool(ctx context.Context, name string, args map[string]interface{}) (*protocol.CallToolResult, error)

	// ListResources 列出所有资源
	ListResources(ctx context.Context, cursor *string) (*protocol.ListResourcesResult, error)

	// ReadResource 读取资源
	ReadResource(ctx context.Context, uri string) (*protocol.ReadResourceResult, error)

	// Close 关闭客户端
	Close() error

	// IsConnected 检查是否已连接
	IsConnected() bool
}

// LifecycleEvent 描述 MCP client 的生命周期事件。
type LifecycleEvent struct {
	Type          string
	TraceID       string
	ClientName    string
	TransportType string
	SessionID     string
	Payload       map[string]interface{}
	Timestamp     time.Time
}

// LifecycleObserver 订阅 MCP client 生命周期事件。
type LifecycleObserver func(LifecycleEvent)

// ObservableClient 暴露可选的生命周期事件能力。
type ObservableClient interface {
	AddLifecycleObserver(LifecycleObserver)
}

type traceIDContextKey struct{}

type mcpSession interface {
	ID() string
	ListTools(context.Context, *mcp.ListToolsParams) (*mcp.ListToolsResult, error)
	CallTool(context.Context, *mcp.CallToolParams) (*mcp.CallToolResult, error)
	ListResources(context.Context, *mcp.ListResourcesParams) (*mcp.ListResourcesResult, error)
	ReadResource(context.Context, *mcp.ReadResourceParams) (*mcp.ReadResourceResult, error)
	Close() error
}

// mcpClient MCP 客户端实现（使用官方 SDK）
type mcpClient struct {
	name           string
	cfg            *config.MCPConfig
	canceled       context.CancelFunc // Context cancel function (to keep subprocess alive)
	session        mcpSession
	connected      bool
	lastTraceID    string
	newTransport   func(cfg *transport.Config) (transport.Transport, error)
	connectSession func(ctx context.Context, mcpTransport mcp.Transport) (mcpSession, error)
	observerMu     sync.RWMutex
	observers      []LifecycleObserver
}

// NewClient 创建 MCP 客户端
func NewClient(name string, cfg *config.MCPConfig) (Client, error) {
	return &mcpClient{
		name:         name,
		cfg:          cfg,
		newTransport: transport.NewTransport,
		connectSession: func(ctx context.Context, mcpTransport mcp.Transport) (mcpSession, error) {
			sdkClient := mcp.NewClient(&mcp.Implementation{
				Name:    "ai-gateway",
				Version: "1.0.0",
			}, nil)
			return sdkClient.Connect(ctx, mcpTransport, &mcp.ClientSessionOptions{})
		},
		observers: make([]LifecycleObserver, 0),
	}, nil
}

// WithTraceID 将 trace_id 绑定到 client 上下文。
func WithTraceID(ctx context.Context, traceID string) context.Context {
	if strings.TrimSpace(traceID) == "" {
		return ctx
	}
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, traceIDContextKey{}, strings.TrimSpace(traceID))
}

// TraceIDFromContext 读取 client 上下文中的 trace_id。
func TraceIDFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	traceID, _ := ctx.Value(traceIDContextKey{}).(string)
	return strings.TrimSpace(traceID)
}

// AddLifecycleObserver 注册 client 生命周期观察者。
func (c *mcpClient) AddLifecycleObserver(observer LifecycleObserver) {
	if c == nil || observer == nil {
		return
	}
	c.observerMu.Lock()
	defer c.observerMu.Unlock()
	c.observers = append(c.observers, observer)
}

// Connect 连接到 MCP Server
func (c *mcpClient) Connect(ctx context.Context) error {
	// 创建一个独立的 context，不使用传入的 timeoutCtx
	// 这样 subprocess 可以一直运行，直到调用 Close()
	backgroundCtx, cancel := context.WithCancel(context.Background())
	c.canceled = cancel
	traceID := TraceIDFromContext(ctx)
	c.lastTraceID = traceID
	backgroundCtx = transport.WithTraceID(backgroundCtx, traceID)
	c.emitLifecycleEvent(traceID, "mcp.client.connecting", "", map[string]interface{}{
		"target": c.connectionTarget(),
	})

	// 创建官方 SDK Transport
	transportCfg := &transport.Config{
		Type:       c.cfg.Type,
		Command:    c.cfg.Command,
		Args:       c.cfg.Args,
		URL:        c.cfg.URL,
		Env:        c.cfg.Env,
		WorkingDir: ".",
	}

	t, err := c.newTransport(transportCfg)
	if err != nil {
		cancel()
		c.emitLifecycleEvent(traceID, "mcp.client.transport_create_failed", "", map[string]interface{}{
			"error":  err.Error(),
			"target": c.connectionTarget(),
		})
		return fmt.Errorf("创建传输层失败: %w", err)
	}
	if observable, ok := t.(transport.ObservableTransport); ok && observable != nil {
		observable.AddLifecycleObserver(func(event transport.LifecycleEvent) {
			c.emitLifecycleEventWithSession(event.TraceID, event.Type, event.TransportType, event.SessionID, event.Payload)
		})
	}
	c.emitLifecycleEvent(traceID, "mcp.client.transport.created", t.Type(), map[string]interface{}{
		"target": c.connectionTarget(),
	})

	// 使用 backgroundCtx 创建 Transport
	mcpTransport := t.ToMCPSdkTransport(backgroundCtx)
	if mcpTransport == nil {
		cancel()
		c.emitLifecycleEvent(traceID, "mcp.client.transport_unsupported", t.Type(), map[string]interface{}{
			"target": c.connectionTarget(),
		})
		return fmt.Errorf("不支持的传输类型: %T", t)
	}

	// 连接并创建 Session
	logger.Infof("[Client] Connecting to MCP: %s", c.name)
	logger.Infof("[Client] Server command: %s %s", c.cfg.Command, strings.Join(c.cfg.Args, " "))
	deadline, _ := ctx.Deadline()
	logger.Infof("[Client] Using context with deadline: %v", deadline)
	c.emitLifecycleEvent(traceID, "mcp.client.session.connecting", t.Type(), map[string]interface{}{
		"target": c.connectionTarget(),
	})

	sessionCtx := transport.WithTraceID(ctx, traceID)
	session, err := c.connectSession(sessionCtx, mcpTransport)
	if err != nil {
		cancel()
		c.emitLifecycleEvent(traceID, "mcp.client.session.connect_failed", t.Type(), map[string]interface{}{
			"error":  err.Error(),
			"target": c.connectionTarget(),
		})
		return fmt.Errorf("连接 MCP Server 失败: %w", err)
	}
	if session == nil {
		cancel()
		err = fmt.Errorf("mcp session is nil")
		c.emitLifecycleEvent(traceID, "mcp.client.session.connect_failed", t.Type(), map[string]interface{}{
			"error":  err.Error(),
			"target": c.connectionTarget(),
		})
		return err
	}

	c.session = session
	c.connected = true
	c.emitLifecycleEvent(traceID, "mcp.client.session.connected", t.Type(), map[string]interface{}{
		"session_id": session.ID(),
		"target":     c.connectionTarget(),
	})
	logger.Infof("[Client] Connected to MCP: %s", c.name)
	return nil
}

// Name 返回 MCP 名称
func (c *mcpClient) Name() string {
	return c.name
}

// ListTools 列出所有工具
func (c *mcpClient) ListTools(ctx context.Context) ([]*protocol.Tool, error) {
	if !c.connected || c.session == nil {
		return nil, fmt.Errorf("客户端未连接")
	}

	logger.Infof("[Client] Listing tools for %s", c.name)

	// 使用官方 SDK 列出工具
	result, err := c.session.ListTools(ctx, &mcp.ListToolsParams{})
	if err != nil {
		return nil, fmt.Errorf("列出工具失败: %w", err)
	}

	logger.Infof("[Client] Found %d tools for %s", len(result.Tools), c.name)

	// 转换为内部 Tool 类型
	tools := make([]*protocol.Tool, 0, len(result.Tools))
	for _, sdkTool := range result.Tools {
		tools = append(tools, convertSDKTool(sdkTool))
	}

	return tools, nil
}

// CallTool 调用工具
func (c *mcpClient) CallTool(ctx context.Context, name string, args map[string]interface{}) (*protocol.CallToolResult, error) {
	if !c.connected {
		return nil, fmt.Errorf("客户端未连接")
	}

	// 使用官方 SDK 调用工具
	result, err := c.session.CallTool(ctx, &mcp.CallToolParams{
		Name:      name,
		Arguments: args,
	})
	if err != nil {
		return nil, fmt.Errorf("调用工具失败: %w", err)
	}

	// 转换为内部结果类型
	return convertCallToolResult(result), nil
}

// ListResources 列出所有资源
func (c *mcpClient) ListResources(ctx context.Context, cursor *string) (*protocol.ListResourcesResult, error) {
	if !c.connected {
		return nil, fmt.Errorf("客户端未连接")
	}

	// 使用官方 SDK 列出资源
	params := &mcp.ListResourcesParams{}
	if cursor != nil {
		params.Cursor = *cursor
	}
	result, err := c.session.ListResources(ctx, params)
	if err != nil {
		return nil, fmt.Errorf("列出资源失败: %w", err)
	}

	// 转换为内部 Resource 类型
	resources := make([]*protocol.Resource, 0, len(result.Resources))
	for _, sdkRes := range result.Resources {
		resources = append(resources, convertSDKResource(sdkRes))
	}

	var nextCursor *string
	if result.NextCursor != "" {
		nextCursor = &result.NextCursor
	}

	return &protocol.ListResourcesResult{
		Resources:  resources,
		NextCursor: nextCursor,
	}, nil
}

// ReadResource 读取资源
func (c *mcpClient) ReadResource(ctx context.Context, uri string) (*protocol.ReadResourceResult, error) {
	if !c.connected {
		return nil, fmt.Errorf("客户端未连接")
	}

	// 使用官方 SDK 读取资源
	result, err := c.session.ReadResource(ctx, &mcp.ReadResourceParams{
		URI: uri,
	})
	if err != nil {
		return nil, fmt.Errorf("读取资源失败: %w", err)
	}

	// 转换为内部结果类型
	return convertReadResourceResult(result), nil
}

// Close 关闭客户端
func (c *mcpClient) Close() error {
	sessionID := ""
	if c.session != nil {
		sessionID = c.session.ID()
	}
	if c.connected && c.session != nil {
		_ = c.session.Close()
		c.connected = false
	}
	// Cancel the context to stop the subprocess
	if c.canceled != nil {
		c.canceled()
	}
	c.emitLifecycleEvent(c.lastTraceID, "mcp.client.session.closed", c.transportType(), map[string]interface{}{
		"session_id": sessionID,
		"target":     c.connectionTarget(),
	})
	return nil
}

// IsConnected 检查是否已连接
func (c *mcpClient) IsConnected() bool {
	return c.connected
}

func (c *mcpClient) emitLifecycleEvent(traceID, eventType, transportType string, payload map[string]interface{}) {
	c.emitLifecycleEventWithSession(traceID, eventType, transportType, "", payload)
}

func (c *mcpClient) emitLifecycleEventWithSession(traceID, eventType, transportType, sessionID string, payload map[string]interface{}) {
	if c == nil {
		return
	}
	c.observerMu.RLock()
	observers := append([]LifecycleObserver(nil), c.observers...)
	c.observerMu.RUnlock()
	if len(observers) == 0 {
		return
	}
	event := LifecycleEvent{
		Type:          eventType,
		TraceID:       traceID,
		ClientName:    c.name,
		TransportType: firstNonEmpty(transportType, c.transportType()),
		Payload:       clonePayload(payload),
		Timestamp:     time.Now().UTC(),
	}
	if strings.TrimSpace(sessionID) != "" {
		event.SessionID = strings.TrimSpace(sessionID)
	} else if c.session != nil {
		event.SessionID = c.session.ID()
	}
	for _, observer := range observers {
		observer(event)
	}
}

func (c *mcpClient) connectionTarget() string {
	if c == nil || c.cfg == nil {
		return ""
	}
	switch c.cfg.Type {
	case "stdio":
		return strings.TrimSpace(c.cfg.Command)
	default:
		return strings.TrimSpace(c.cfg.URL)
	}
}

func (c *mcpClient) transportType() string {
	if c == nil || c.cfg == nil {
		return ""
	}
	return strings.TrimSpace(c.cfg.Type)
}

func clonePayload(payload map[string]interface{}) map[string]interface{} {
	if len(payload) == 0 {
		return nil
	}
	cloned := make(map[string]interface{}, len(payload))
	for key, value := range payload {
		cloned[key] = value
	}
	return cloned
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

// convertSDKTool 转换官方 SDK Tool 为内部 Tool
func convertSDKTool(sdkTool *mcp.Tool) *protocol.Tool {
	inputSchema := make(map[string]interface{})
	if sdkTool.InputSchema != nil {
		if jsonInput, ok := sdkTool.InputSchema.(map[string]interface{}); ok {
			inputSchema = jsonInput
		} else {
			if raw, err := json.Marshal(sdkTool.InputSchema); err == nil {
				var decoded map[string]interface{}
				if err := json.Unmarshal(raw, &decoded); err == nil && decoded != nil {
					inputSchema = decoded
				}
			}
		}
	}

	return &protocol.Tool{
		Name:        sdkTool.Name,
		Description: sdkTool.Description,
		InputSchema: inputSchema,
	}
}

// convertCallToolResult 转换官方 SDK CallToolResult 为内部 CallToolResult
func convertCallToolResult(sdkResult *mcp.CallToolResult) *protocol.CallToolResult {
	content := make([]protocol.Content, 0, len(sdkResult.Content))
	for _, sdkContent := range sdkResult.Content {
		content = append(content, convertContent(sdkContent))
	}

	return &protocol.CallToolResult{
		Content: content,
		IsError: sdkResult.IsError,
		Meta:    sdkResult.Meta,
	}
}

// convertReadResourceResult 转换官方 SDK ReadResourceResult 为内部 ReadResourceResult
func convertReadResourceResult(sdkResult *mcp.ReadResourceResult) *protocol.ReadResourceResult {
	contents := make([]protocol.ResourceContents, 0, len(sdkResult.Contents))
	for _, sdkContent := range sdkResult.Contents {
		contents = append(contents, convertResourceContents(sdkContent))
	}

	return &protocol.ReadResourceResult{
		Contents: contents,
	}
}

// convertSDKResource 转换官方 SDK Resource 为内部 Resource
func convertSDKResource(sdkRes *mcp.Resource) *protocol.Resource {
	return &protocol.Resource{
		URI:         sdkRes.URI,
		Name:        sdkRes.Name,
		Description: sdkRes.Description,
		MIMEType:    sdkRes.MIMEType,
	}
}

// convertContent 转换官方 SDK Content 为内部 Content
func convertContent(sdkContent mcp.Content) protocol.Content {
	switch c := sdkContent.(type) {
	case *mcp.TextContent:
		return protocol.Content{
			Type: "text",
			Text: c.Text,
		}
	case *mcp.ImageContent:
		return protocol.Content{
			Type:     "image",
			Data:     string(c.Data),
			MIMEType: c.MIMEType,
		}
	case *mcp.EmbeddedResource:
		if c.Resource != nil {
			if c.Resource.Text != "" {
				return protocol.Content{
					Type: "resource",
					Text: c.Resource.Text,
					URI:  c.Resource.URI,
				}
			}
			if len(c.Resource.Blob) > 0 {
				return protocol.Content{
					Type: "resource",
					Data: string(c.Resource.Blob),
					URI:  c.Resource.URI,
				}
			}
		}
		return protocol.Content{
			Type: "resource",
		}
	default:
		return protocol.Content{
			Type: "unknown",
		}
	}
}

// convertResourceContents 转换官方 SDK ResourceContents 为内部 ResourceContents
func convertResourceContents(sdkContent *mcp.ResourceContents) protocol.ResourceContents {
	contents := make([]protocol.Content, 0)

	if sdkContent.Text != "" {
		contents = append(contents, protocol.Content{
			Type: "text",
			Text: sdkContent.Text,
		})
	}

	if len(sdkContent.Blob) > 0 {
		contents = append(contents, protocol.Content{
			Type: "blob",
			Data: string(sdkContent.Blob),
		})
	}

	return protocol.ResourceContents{
		URI:      sdkContent.URI,
		MIMEType: sdkContent.MIMEType,
		Contents: contents,
	}
}
