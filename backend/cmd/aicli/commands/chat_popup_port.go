package commands

import "github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"

// currentPopupPort 读取已注入的会话 popup 门面（L5-2 Batch B D1）。写点是
// SetSurface/Shutdown（c.mu 下经原子指针发布），读取点分布在命令处理器与
// 编辑器回调、不保证持有 c.mu，因此用原子读避免与 surface 生命周期切换竞争。
func (c *chatInteractionCoordinator) currentPopupPort() ui.PopupPort {
	if c == nil {
		return nil
	}
	if slot := c.popupPort.Load(); slot != nil {
		return *slot
	}
	return nil
}

// chatSessionPopupPort 解析会话 popup 门面（L5-2 Batch B D1：方案 §2「经
// session/coordinator 注入 commands」）。
//
// 解析顺序：
//  1. coordinator 已注入的 ui.PopupPort（SetSurface 生命周期绑定）：unified
//     直投 Show/Update/ClearPopupAction 到 controller（handle 分配在门面边界），
//     HasActivePopup 查询 reducer 权威 BottomPaneState；legacy/compat 回落
//     surface 本地实现；
//  2. 未注入（测试/headless 直接设置 session.Surface，未走 SetSurface）：
//     按 legacy 语义构造 surface 本地门面（无 unified 状态源；token 仍与
//     surface 直调路径共享分配器，不串号）；
//  3. surface 也不可用：no-op 端口，调用安全。
//
// 始终返回非 nil 端口，调用点直接调用方法即可；popup 族（含 HasActivePopup）
// 不得再直读 .Surface.*（门禁：TestChatPopupFamilyDirectReadsFrozen）。
func chatSessionPopupPort(session *ChatSession) ui.PopupPort {
	if session == nil {
		return ui.NewPopupPort(nil, nil)
	}
	if session.Interaction != nil {
		if port := session.Interaction.currentPopupPort(); port != nil {
			return port
		}
	}
	if session.Surface != nil {
		return ui.NewPopupPort(session.Surface, nil)
	}
	return ui.NewPopupPort(nil, nil)
}
