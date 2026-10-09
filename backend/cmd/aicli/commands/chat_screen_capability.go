package commands

// chatSurfaceScreenGate 是统一渲染副屏能力的单点语义（D3，L5-2 方案 §2 D3
// 引出）：会话可承载备用屏/选择器输入的前提 = 统一渲染面已启用、独占
// viewport、无在途租约、无活动弹层。
//
// 收敛范围：原先 12 处调用点（10 个 canOpenChatXxx + chatPickerSurfaceReady +
// chatScreenCapability）内联的完全相同的四联表达式一律经此判定；各调用点自身
// 的前置条件（NoInteractive/JSONOutput/Interaction/Surface 非空、
// RuntimeEventBridge 空闲、终端能力 CanUseFullScreenList 等）仍留在原处——
// 本函数只收敛公共 gate，不改变各站点的 fail-closed 顺序。
//
// 机械门禁：chat_screen_gate_freeze_test.go 冻结 OwnedViewport() 直读面
// （全仓仅剩本文件 1 处）；新增副屏 gate 必须先经本单点，不得内联复制。
func chatSurfaceScreenGate(session *ChatSession) bool {
	if session == nil || session.Surface == nil {
		return false
	}
	return session.Surface.Enabled() &&
		session.Surface.OwnedViewport() &&
		!session.Surface.LeaseActive() &&
		!chatSessionPopupPort(session).HasActivePopup()
}
