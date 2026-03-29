package server

import (
	"context"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Server MCP 服务器接口
type Server interface {
	// Start 启动服务器
	Start(ctx context.Context) error

	// Stop 停止服务器
	Stop() error

	// HandleRequest 处理请求并返回响应
	HandleRequest(ctx context.Context, req []byte) ([]byte, error)

	// GetMCPServer 获取底层官方 SDK 的 Server 实例（用于高级操作）
	GetMCPServer() *mcp.Server
}

// BaseServer 基础服务器实现（使用官方 SDK）
type BaseServer struct {
	server *mcp.Server
	impl   *mcp.Implementation
}

// NewBaseServer 创建基础服务器
func NewBaseServer(name, version string) *BaseServer {
	impl := &mcp.Implementation{
		Name:    name,
		Version: version,
	}

	return &BaseServer{
		server: mcp.NewServer(impl, nil),
		impl:   impl,
	}
}

// GetMCPServer 获取底层官方 SDK 的 Server 实例
func (s *BaseServer) GetMCPServer() *mcp.Server {
	return s.server
}

// RegisterTool 注册工具处理器到官方 SDK
func (s *BaseServer) RegisterTool(name, description string, handler mcp.ToolHandler) {
	tool := &mcp.Tool{
		Name:        name,
		Description: description,
	}
	s.server.AddTool(tool, handler)
}

// RegisterResource 注册资源处理器到官方 SDK
func (s *BaseServer) RegisterResource(resource *mcp.Resource, handler mcp.ResourceHandler) {
	s.server.AddResource(resource, handler)
}

// Start 启动服务器在指定传输层上
func (s *BaseServer) Start(ctx context.Context, transport any) error {
	switch t := transport.(type) {
	case *mcp.StdioTransport:
		return s.server.Run(ctx, t)
	default:
		return fmt.Errorf("不支持的传输类型: %T", t)
	}
}

// Stop 停止服务器
func (s *BaseServer) Stop() error {
	return nil
}

// HandleRequest 处理单个请求（用于测试或手动控制流）
func (s *BaseServer) HandleRequest(ctx context.Context, req []byte) ([]byte, error) {
	// 注意：官方 SDK 的 Server 运行在 Transport 线程上，不支持手动处理单个请求
	// 这个方法保留用于兼容性，实际使用时应该通过 Start() 和 Transport 来运行
	return nil, fmt.Errorf("官方 SDK 不支持手动处理单个请求，请使用 Start() 方法")
}

// GetImplementation 获取实现信息
func (s *BaseServer) GetImplementation() *mcp.Implementation {
	return s.impl
}

// GetServerOptions 获取服务器选项
func (s *BaseServer) GetServerOptions() *mcp.ServerOptions {
	return &mcp.ServerOptions{}
}
