package transport

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

var (
	upgrader = websocket.Upgrader{
		CheckOrigin: func(r *http.Request) bool {
			return true // 允许所有源（生产环境应该更严格）
		},
	}
	noDeadline = time.Time{} // 零值表示没有超时
)

// WebSocketTransport WebSocket 传输封装
type WebSocketTransport struct {
	cfg      *Config
	httpConn *websocket.Conn
	emitter  lifecycleEmitter
}

// NewWebSocketTransport 创建 WebSocket 传输
func NewWebSocketTransport(cfg *Config) *WebSocketTransport {
	return &WebSocketTransport{
		cfg: cfg,
	}
}

// Type 返回传输类型
func (t *WebSocketTransport) Type() string {
	return "websocket"
}

// Config 返回传输配置
func (t *WebSocketTransport) Config() interface{} {
	return t.cfg
}

func (t *WebSocketTransport) AddLifecycleObserver(observer LifecycleObserver) {
	t.emitter.AddLifecycleObserver(observer)
}

// ToMCPSdkTransport 转换为官方 SDK 的 Transport
func (t *WebSocketTransport) ToMCPSdkTransport(ctx context.Context) mcp.Transport {
	return newObservedMCPTransport("websocket", t.cfg.URL, &WSClientTransport{
		cfg:     t.cfg,
		dialer:  websocket.DefaultDialer,
		headers: buildHeadersFromEnv(t.cfg.Env),
	}, &t.emitter)
}

// buildHeadersFromEnv 从 env 构建 HTTP 头
func buildHeadersFromEnv(env map[string]string) http.Header {
	if len(env) == 0 {
		return nil
	}
	headers := make(http.Header)
	for k, v := range env {
		headers.Set(k, v)
	}
	return headers
}

// WSClientTransport WebSocket 客户端传输实现
type WSClientTransport struct {
	cfg     *Config
	dialer  *websocket.Dialer
	headers http.Header
}

// Connect 连接到 WebSocket 服务器
func (t *WSClientTransport) Connect(ctx context.Context) (mcp.Connection, error) {
	deadline, ok := ctx.Deadline()
	fmt.Printf("[WS] 正在连接到 %s (ctx deadline: %v, has deadline: %v)\n", t.cfg.URL, deadline, ok)

	conn, _, err := t.dialer.DialContext(ctx, t.cfg.URL, t.headers)
	if err != nil {
		return nil, fmt.Errorf("WebSocket 连接失败: %w", err)
	}

	fmt.Printf("[WS] WebSocket 连接成功\n")

	// 移除任何超时限制，让连接无限期保持
	conn.SetReadDeadline(noDeadline)
	conn.SetWriteDeadline(noDeadline)

	// 创建连接
	return &WSConnection{
		conn:      conn,
		sessionID: fmt.Sprintf("ws-session-%d", 12345),
	}, nil
}

// WSConnection WebSocket 连接实现
type WSConnection struct {
	conn      *websocket.Conn
	sessionID string
	closeOnce sync.Once
}

// Read 从连接读取消息
func (c *WSConnection) Read(ctx context.Context) (jsonrpc.Message, error) {
	_, data, err := c.conn.ReadMessage()
	if err != nil {
		return nil, fmt.Errorf("读取消息失败: %w", err)
	}

	fmt.Printf("[WS] Read %d bytes: %s\n", len(data), string(data))

	// 使用 SDK 的 DecodeMessage
	msg, err := jsonrpc.DecodeMessage(data)
	if err != nil {
		return nil, fmt.Errorf("解析消息失败: %w", err)
	}

	return msg, nil
}

// Write 向连接写入消息
func (c *WSConnection) Write(ctx context.Context, msg jsonrpc.Message) error {
	data, err := jsonrpc.EncodeMessage(msg)
	if err != nil {
		return fmt.Errorf("序列化消息失败: %w", err)
	}

	fmt.Printf("[WS] Write %d bytes: %s\n", len(data), string(data))

	err = c.conn.WriteMessage(websocket.TextMessage, data)
	if err != nil {
		return fmt.Errorf("写入消息失败: %w", err)
	}

	return nil
}

// Close 关闭连接
func (c *WSConnection) Close() error {
	var err error
	c.closeOnce.Do(func() {
		err = c.conn.Close()
	})
	return err
}

// SessionID 返回会话 ID
func (c *WSConnection) SessionID() string {
	return c.sessionID
}
