package commands

import (
	"context"
	"strings"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/mcp/manager"
)

// chatMCPToolSurfaceEvents 触发会话工具面失效的 MCP 生命周期事件：
// 连接/重连完成、工具加载、服务启停、工具级启停；热重载经由停用/启动事件覆盖。
var chatMCPToolSurfaceEvents = map[string]bool{
	"mcp.connected":          true,
	"mcp.tools.loaded":       true,
	"mcp.reconnected":        true,
	"mcp.disabled":           true,
	"mcp.stopped":            true,
	"mcp.tool.state_changed": true,
}

// wireChatMCPToolSurfaceInvalidation 订阅 MCP 生命周期事件：目录变化后异步清除
// 活跃会话的稳定工具面缓存，使最新工具（如 list_pages）在下一个 turn 边界进入工具面。
func wireChatMCPToolSurfaceInvalidation(mgr manager.Manager) {
	observable, ok := mgr.(manager.ObservableManager)
	if !ok || observable == nil {
		return
	}
	observable.AddLifecycleObserver(func(event manager.LifecycleEvent) {
		if !chatMCPToolSurfaceEvents[strings.TrimSpace(event.Type)] {
			return
		}
		// 观察者在 manager 建连/重载路径同步触发，这里异步执行避免阻塞事件发布。
		go invalidateChatSessionToolSurfaces()
	})
}

// invalidateChatSessionToolSurfaces 清除当前进程活跃 chat 会话的稳定工具面，
// 返回处理的会话数（无本地运行时宿主时为 0）。
func invalidateChatSessionToolSurfaces() int {
	session := chatWebSession()
	if session == nil || session.LocalRuntimeHost == nil || session.LocalRuntimeHost.SessionHub == nil {
		return 0
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	return session.LocalRuntimeHost.SessionHub.InvalidateStableToolSurfaces(ctx)
}

// invalidateACPSessionToolSurface 清除单个 ACP 会话的稳定工具面缓存。
//
// ACP 会话不在 chatWebSession() 覆盖范围内，而 MCP 工具（客户端下发或本地
// 配置链）总是在 session/new 之后才连上；没有这一步，迟到的工具会一直停在
// 首个 turn 冻结的旧工具面之外（§4.7 R1 / §4.10）。
func invalidateACPSessionToolSurface(session *ChatSession) int {
	if session == nil || session.LocalRuntimeHost == nil || session.LocalRuntimeHost.SessionHub == nil {
		return 0
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	return session.LocalRuntimeHost.SessionHub.InvalidateStableToolSurfaces(ctx)
}
