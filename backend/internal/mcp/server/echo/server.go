package echo

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"sync"

	"github.com/gorilla/websocket"
	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
)

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool {
		return true // 允许所有源（测试用）
	},
}

// EchoServer 简单的 Echo MCP 服务器（用于测试 WebSocket 传输）
type EchoServer struct {
	addr    string
	server  *http.Server
	clients map[*websocket.Conn]bool
	mu      sync.RWMutex
}

// NewEchoServer 创建 Echo 服务器
func NewEchoServer(addr string) *EchoServer {
	return &EchoServer{
		addr:    addr,
		clients: make(map[*websocket.Conn]bool),
	}
}

// Start 启动服务器
func (s *EchoServer) Start() error {
	mux := http.NewServeMux()
	mux.HandleFunc("/mcp", s.handleWebSocket)

	s.server = &http.Server{
		Addr:    s.addr,
		Handler: mux,
	}

	log.Printf("[EchoServer] Starting WebSocket MCP server on %s", s.addr)
	return s.server.ListenAndServe()
}

// Stop 停止服务器
func (s *EchoServer) Stop() error {
	if s.server != nil {
		return s.server.Close()
	}
	return nil
}

// handleWebSocket 处理 WebSocket 连接
func (s *EchoServer) handleWebSocket(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("[EchoServer] WebSocket upgrade failed: %v", err)
		return
	}
	defer conn.Close()

	s.mu.Lock()
	s.clients[conn] = true
	s.mu.Unlock()

	defer func() {
		s.mu.Lock()
		delete(s.clients, conn)
		s.mu.Unlock()
	}()

	log.Printf("[EchoServer] Client connected from %s", conn.RemoteAddr())

	// 处理消息
	for {
		_, data, err := conn.ReadMessage()
		if err != nil {
			log.Printf("[EchoServer] Read error: %v", err)
			break
		}

		log.Printf("[EchoServer] Received raw message: %s", string(data))

		// 使用 MCP SDK 解析消息
		msg, err := jsonrpc.DecodeMessage(data)
		if err != nil {
			log.Printf("[EchoServer] DecodeMessage error: %v", err)
			continue
		}

		log.Printf("[EchoServer] Parsed message type: %T", msg)

		// 处理消息
		response, err := s.handleMessage(msg)
		if err != nil {
			log.Printf("[EchoServer] Handle error: %v", err)
			continue
		}

		if response != nil {
			responseData, err := jsonrpc.EncodeMessage(response)
			if err != nil {
				log.Printf("[EchoServer] EncodeMessage error: %v", err)
				continue
			}

			if err := conn.WriteMessage(websocket.TextMessage, responseData); err != nil {
				log.Printf("[EchoServer] Write error: %v", err)
				break
			}
			log.Printf("[EchoServer] Sent response: %s", string(responseData))
		}
	}
}

// handleMessage 处理 JSON-RPC 消息
func (s *EchoServer) handleMessage(msg jsonrpc.Message) (jsonrpc.Message, error) {
	req, ok := msg.(*jsonrpc.Request)
	if !ok {
		// 不是请求，可能是响应或通知，不需要回复
		log.Printf("[EchoServer] Received non-request message: %T", msg)
		return nil, nil
	}

	log.Printf("[EchoServer] Method: %s, HasID: %v", req.Method, req.ID.IsValid())

	switch req.Method {
	case "initialize":
		return s.handleInitialize(req)
	case "tools/list":
		return s.handleToolsList(req)
	case "tools/call":
		return s.handleToolCall(req)
	default:
		return nil, nil // 不回复未知方法
	}
}

// handleInitialize 处理 initialize 请求
func (s *EchoServer) handleInitialize(req *jsonrpc.Request) (jsonrpc.Message, error) {
	result := map[string]interface{}{
		"protocolVersion": "2024-11-05",
		"capabilities": map[string]interface{}{
			"tools": map[string]interface{}{},
		},
		"serverInfo": map[string]interface{}{
			"name":    "echo-mcp",
			"version": "1.0.0",
		},
	}

	resp := &jsonrpc.Response{
		ID:     req.ID,
		Result: s.marshalResult(result),
	}

	log.Printf("[EchoServer] Created initialize response")
	return resp, nil
}

// handleToolsList 处理 tools/list 请求
func (s *EchoServer) handleToolsList(req *jsonrpc.Request) (jsonrpc.Message, error) {
	tools := []map[string]interface{}{
		{
			"name":        "echo",
			"description": "回显输入的文本",
			"inputSchema": map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"message": map[string]interface{}{
						"type":        "string",
						"description": "要回显的消息",
					},
				},
				"required": []string{"message"},
			},
		},
		{
			"name":        "add",
			"description": "加法运算",
			"inputSchema": map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"a": map[string]interface{}{
						"type":        "number",
						"description": "第一个数",
					},
					"b": map[string]interface{}{
						"type":        "number",
						"description": "第二个数",
					},
				},
				"required": []string{"a", "b"},
			},
		},
	}

	result := map[string]interface{}{
		"tools": tools,
	}

	return &jsonrpc.Response{
		ID:     req.ID,
		Result: s.marshalResult(result),
	}, nil
}

// handleToolCall 处理 tools/call 请求
func (s *EchoServer) handleToolCall(req *jsonrpc.Request) (jsonrpc.Message, error) {
	var params struct {
		Name string                 `json:"name"`
		Args map[string]interface{} `json:"arguments"`
	}

	if err := json.Unmarshal(req.Params, &params); err != nil {
		return nil, err
	}

	var content []map[string]interface{}

	switch params.Name {
	case "echo":
		msg := "no message"
		if m, ok := params.Args["message"].(string); ok {
			msg = m
		}
		content = []map[string]interface{}{
			{
				"type": "text",
				"text": "Echo: " + msg,
			},
		}

	case "add":
		result := 0.0
		if a, ok := params.Args["a"].(float64); ok {
			result += a
		}
		if b, ok := params.Args["b"].(float64); ok {
			result += b
		}
		content = []map[string]interface{}{
			{
				"type": "text",
				"text": fmt.Sprintf("Result: %v", result),
			},
		}

	default:
		content = []map[string]interface{}{
			{
				"type":  "text",
				"text": "Unknown tool: " + params.Name,
			},
		}
	}

	result := map[string]interface{}{
		"content": content,
	}

	return &jsonrpc.Response{
		ID:     req.ID,
		Result: s.marshalResult(result),
	}, nil
}

// marshalResult 辅助函数：将结果序列化为 json.RawMessage
func (s *EchoServer) marshalResult(result interface{}) json.RawMessage {
	data, err := json.Marshal(result)
	if err != nil {
		log.Printf("[EchoServer] Marshal error: %v", err)
		return nil
	}
	return json.RawMessage(data)
}
