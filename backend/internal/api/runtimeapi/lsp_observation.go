package runtimeapi

import (
	"github.com/wwsheng009/ai-agent-runtime/internal/lsp"
	"github.com/wwsheng009/ai-agent-runtime/internal/lsp/eventbridge"
	"github.com/wwsheng009/ai-agent-runtime/internal/skill"
)

// attachLSPObservation 把 runtime-server 工具面的 LSP 池事件接到本 Handler 的
// 运行时事件总线（lsp.*，通道表态见 internal/events/contract.go）。
func (h *Handler) attachLSPObservation() {
	if h == nil {
		return
	}
	h.wireLSPObservation(h.mcpManager)
}

// wireLSPObservation 给单个工具面接线并原样返回 surface（profile 级临时
// manager 也走这里）；surface 不支持 SetLSPObserver 时是安全 no-op。
//
// runtime-server 的工具管理器是进程级共享实例（AgentAdapter→tools.Manager），
// 因此事件按工具执行 ctx 里的会话 id 归属（internal/lsp 的 SessionIDFromContext
// 已接 toolctx.SessionID）；没有执行上下文的池生命周期事件作为无会话事件进入
// observe 流。
func (h *Handler) wireLSPObservation(surface skill.MCPManager) skill.MCPManager {
	if h == nil || surface == nil {
		return surface
	}
	attacher, ok := surface.(interface{ SetLSPObserver(lsp.Observer) })
	if !ok {
		return surface
	}
	observer := eventbridge.Observer(h.runtimeEventBus, eventbridge.Options{})
	if observer == nil {
		return surface
	}
	attacher.SetLSPObserver(observer)
	return surface
}
