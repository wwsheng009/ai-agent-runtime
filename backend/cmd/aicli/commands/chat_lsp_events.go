package commands

import (
	runtimelsp "github.com/wwsheng009/ai-agent-runtime/internal/lsp"
	"github.com/wwsheng009/ai-agent-runtime/internal/lsp/eventbridge"
)

// chatLSPRuntimeObserver 把 LSP 池事件接到当前会话的 EventBus（lsp.*，live-only）：
// 投影规则在 internal/lsp/eventbridge（与 runtime-server 共用同一份，避免两处漂移）。
//
// 会话归属优先取事件自带的 SessionID（Bridge 从工具执行 ctx 的 toolctx 解析）；
// 事件没有归属时回退到当前会话 id。总线未建立时返回 nil（SetLSPObserver 对 nil
// 是安全 no-op）。
func chatLSPRuntimeObserver(session *ChatSession) runtimelsp.Observer {
	if session == nil || session.LocalRuntimeHost == nil {
		return nil
	}
	bus := session.LocalRuntimeHost.EventBus
	if bus == nil {
		return nil
	}
	sessionID := ""
	if session.RuntimeSession != nil {
		sessionID = session.RuntimeSession.ID
	}
	return eventbridge.Observer(bus, eventbridge.Options{FallbackSessionID: sessionID})
}
