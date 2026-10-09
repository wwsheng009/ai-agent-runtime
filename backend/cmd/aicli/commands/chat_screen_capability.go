package commands

// chatSessionSurfaceUsable 是会话级「统一渲染面可用」的单点语义（D3 Batch D）：
// session.Surface 存在且 Enabled。收敛范围：composer
// （chatComposerUsesFixedSurface）、prompt overlay（surfaceEnabled）、
// 输入/补全/历史/transcript/调试展示/登录 picker 等会话级可用性判定（24 处）。
//
// 语义保持：原先各站点形态（nil 守卫、trackPrompt 布尔、调试字段、正向/反向
// guard）逐点等价替换；nil 安全（原隐式不变量点迁移后 fail-closed）。
// 机械门禁：chat_session_surface_freeze_test.go 冻结 `X.Surface.Enabled()`
// 生产直读为 1 处 = 本函数；新增可用性判定必须先经本单点。
func chatSessionSurfaceUsable(session *ChatSession) bool {
	return session != nil && session.Surface != nil && session.Surface.Enabled()
}

// chatSurfaceScreenGate 是统一渲染副屏能力的单点语义（D3，L5-2 方案 §2 D3
// 引出）：会话可承载备用屏/选择器输入的前提 = 统一渲染面已启用、独占
// viewport、无在途租约、无活动弹层（启用判定复用 chatSessionSurfaceUsable）。
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
	if !chatSessionSurfaceUsable(session) {
		return false
	}
	return session.Surface.OwnedViewport() &&
		!session.Surface.LeaseActive() &&
		!chatSessionPopupPort(session).HasActivePopup()
}

// chatSurfaceLeased 是副屏租约繁忙的单点语义（D3 Batch B）：surface 副屏租约
// 活跃 = 有备用屏/选择器正持有 stdin（租约释放即门打开，无需额外会话标志）。
// 收敛范围：chatBusyScreenActiveForSession（忙时主循环门）与 chatScreenCapability
// 的 degrade 原因标签（busy）；机械门禁见 chat_screen_gate_freeze_test.go
// （LeaseActive 链式直读白名单：仅本文件两处单点）。
func chatSurfaceLeased(session *ChatSession) bool {
	return session != nil && session.Surface != nil && session.Surface.LeaseActive()
}
