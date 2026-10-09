package commands

import "github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"

// currentPromptEditorPort 读取已注入的会话 prompt-editor 门面（L5-2c）。写点是
// SetSurface/Shutdown（c.mu 下经原子指针发布），读取点分布在 composer 回调、
// 不保证持有 c.mu，因此用原子读避免与 surface 生命周期切换竞争。
func (c *chatInteractionCoordinator) currentPromptEditorPort() ui.PromptEditorPort {
	if c == nil {
		return nil
	}
	if slot := c.promptEditorPort.Load(); slot != nil {
		return *slot
	}
	return nil
}

// chatSessionPromptPort 解析会话 prompt-editor 门面（L5-2c：方案 §2「经
// session/coordinator 注入 commands」）。
//
// 解析顺序：
//  1. coordinator 已注入的 ui.PromptEditorPort（SetSurface 生命周期绑定）：
//     unified 直投 SetPromptEditorStatusAction（reducer 写
//     Bottom.PromptEditorStatusLine）、预算走渲染器同源投影
//     （BottomPanePolicyForGeometry）；legacy/compat 回落 surface 本地实现；
//  2. 未注入（测试/headless 直接设置 session.Surface，未走 SetSurface）：
//     按 legacy 语义构造 surface 本地门面；
//  3. surface 也不可用：no-op 端口，调用安全。
//
// 始终返回非 nil 端口，调用点直接调用方法即可；prompt-editor 族
// （SetPromptEditorStatusLine / PromptInputMaxVisibleRows）不得再直读
// .Surface.*（门禁：TestChatPopupFamilyDirectReadsFrozen 邻近族）。
func chatSessionPromptPort(session *ChatSession) ui.PromptEditorPort {
	if session == nil {
		return ui.NewPromptEditorPort(nil, nil)
	}
	if session.Interaction != nil {
		if port := session.Interaction.currentPromptEditorPort(); port != nil {
			return port
		}
	}
	if session.Surface != nil {
		return ui.NewPromptEditorPort(session.Surface, nil)
	}
	return ui.NewPromptEditorPort(nil, nil)
}
