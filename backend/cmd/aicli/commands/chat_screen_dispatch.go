package commands

import (
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/render"
)

// chat_screen_dispatch.go 是副屏效应的唯一派发入口（方案 §4.1/§6.2，批次 0）。
//
// 批次 5（D-E）起：
//   - CommandResult 只保留 Screen（*chatScreenSpec）作为副屏效应字段；旧 Open*
//     字段、legacy 开关与 chatScreenLegacySpecs 映射层已删除（§6.1）；
//   - AICLI_CHAT_SCREEN_FRAMEWORK 只接受空/unified，其他值只记 unknown_env
//     警告事件（读取点见 openChatScreen）；
//   - B 族只读副屏（/debug display、/web endpoints、/usage、/account、/accounts）
//     的生产点直接调用本文件的构建函数写 result.Screen；A 族 picker 各自构造
//     带 RunScreen/AfterClose 的 Spec。
//
// 降级契约（D-F / I6）：能力不足、租约忙、Spec 非法或嵌套时，框架把内容
// 降级为主屏内联文档单元格；例外是 /debug display 与 transcript 页 —— 旧实现
// 在能力不足时就是静默不渲染，批次 1 用 SilentDegrade 保持该行为（只记
// degrade 事件与计数器）。

// dispatchChatScreenEffects 处理一次命令结果的副屏效应。调用方必须在
// commandMu 释放之后（renderErr == nil 时）调用，与批次 0 之前各 opener 的
// 调用点一致。AfterClose 钩子（I3）由 chatScreenOpenAndApply 在 close 序列
// 完成后执行：结果应用/文案渲染因此不会与副屏帧或主屏恢复重叠。
func dispatchChatScreenEffects(session *ChatSession, result CommandResult) {
	if session == nil || result.Screen == nil {
		return
	}
	if result.Screen.Effect != nil {
		// 复合交互（多次连续租约的 A 族 picker）：opener 自己经 openChatScreen
		// 编排每一段副屏，dispatch 只保证它在命令渲染完成后被调用一次。
		result.Screen.Effect(session)
		return
	}
	chatScreenOpenAndApply(session, *result.Screen)
}

// chatScreenSpecRef 返回 Spec 的独立指针副本，便于命令生产点直接写入
// CommandResult.Screen（每个结果一个副本，避免共享可变 Spec）。
func chatScreenSpecRef(spec chatScreenSpec) *chatScreenSpec {
	return &spec
}

// chatScreenEffectSpec 包装一个复合交互入口（chatScreenSpec.Effect）：命令
// 生产点在命令提交后仍按批次 5 之前的位置调用 opener，从而保持既有预检
// （actor 查询、窗口构建、多段租约）的时序与文案逐字不变。
func chatScreenEffectSpec(id, title string, effect func(*ChatSession)) *chatScreenSpec {
	return &chatScreenSpec{
		ID:      id,
		Title:   title,
		Kind:    chatScreenList,
		Effect:  effect,
		Trigger: "command",
	}
}

// chatScreenDebugDisplaySpec 构建 /debug display 副屏：快照在进入副屏前捕获
// 一次，屏内只做纯渲染（与旧 openChatDebugOverlay 同源，行为不变）。
func chatScreenDebugDisplaySpec(session *ChatSession) chatScreenSpec {
	spec := chatScreenSpec{
		ID:      "debug.display",
		Title:   "调试信息",
		Kind:    chatScreenDocument,
		Trigger: "command",
	}
	if !canOpenChatDebugOverlay(session) {
		// 旧行为：能力不足时静默不渲染（只记 degrade 事件与计数器）。
		spec.SilentDegrade = true
		spec.Doc = textLinesDocument([]string{"调试信息不可用"})
		return spec
	}
	spec.Doc = textLinesDocument(strings.Split(chatDebugOverlayBody(session), "\n"))
	return spec
}

// chatScreenWebEndpointsSpec 构建 /web endpoints 副屏：端点清单在开屏前捕获
// （BuildChatDebugEndpointsText 已是静态投影）。
func chatScreenWebEndpointsSpec(session *ChatSession) chatScreenSpec {
	body := BuildChatDebugEndpointsText()
	spec := chatScreenSpec{
		ID:      "web.endpoints",
		Title:   "Web 调试端点",
		Kind:    chatScreenDocument,
		Doc:     textLinesDocument(strings.Split(body, "\n")),
		Trigger: "command",
	}
	if !canOpenChatWebEndpointsScreen(session) {
		// 旧实现在能力不足时走 printChatCommandOutput 直写 stdout（I8 违例）；
		// 统一路径按 D-F 降级为主屏内联单元格，绝不直写 TTY。
		spec.ForceInline = true
		spec.ForceInlineReason = "unavailable"
	}
	return spec
}

// chatScreenUsageSpec 构建 /usage 副屏：正文来自与旧 openChatUsageScreen 同一组
// 纯渲染函数；数据源缺失时按旧行为显示降级文档（不闪空屏）。
func chatScreenUsageSpec(session *ChatSession, req UsageScreenRequest) chatScreenSpec {
	body, ok := buildUsageScreenBody(session, req)
	if !ok {
		return chatScreenSpec{
			ID:                "usage.screen",
			Title:             usageScreenTitleForMode(req.Mode),
			Kind:              chatScreenDocument,
			Doc:               textLinesDocument(strings.Split(body, "\n")),
			ForceInline:       true,
			ForceInlineReason: "unavailable",
			Trigger:           "command",
		}
	}
	spec := chatScreenSpec{
		ID:      "usage.screen",
		Title:   usageScreenTitleForMode(req.Mode),
		Kind:    chatScreenDocument,
		Doc:     textLinesDocument(strings.Split(body, "\n")),
		Trigger: "command",
	}
	if !canOpenChatUsageScreen(session) {
		spec.ForceInline = true
		spec.ForceInlineReason = "unavailable"
		spec.Doc = chatScreenResultDocument(usageFallbackDocumentResult(session, req))
	}
	return spec
}

// chatScreenAccountSpec 构建 /account 副屏：报告在开屏前捕获；能力不足时按旧
// 行为降级为文档单元格（同一份报告文本）。
func chatScreenAccountSpec(session *ChatSession, req AccountScreenRequest) chatScreenSpec {
	spec := chatScreenSpec{
		ID:      "account.screen",
		Title:   accountScreenTitle(req.Report),
		Kind:    chatScreenDocument,
		Doc:     textLinesDocument(accountScreenBodyLines(req.Report)),
		Trigger: "command",
	}
	if !canOpenChatAccountScreen(session) {
		spec.ForceInline = true
		spec.ForceInlineReason = "unavailable"
		spec.Doc = chatScreenResultDocument(accountScreenFallbackResult(req))
	}
	return spec
}

// chatScreenAccountsSpec 构建 /accounts 副屏：表格与屏内刷新回调同源（保持按 r
// 刷新的既有能力），标题/正文取自同一刷新器快照，开屏第一帧不漂移。
func chatScreenAccountsSpec(session *ChatSession, req AccountListScreenRequest) chatScreenSpec {
	options := chatAccountsScreenOptions(session, req)
	spec := chatScreenSpec{
		ID:          "accounts.screen",
		Title:       options.Title,
		Kind:        chatScreenDocument,
		Doc:         textLinesDocument(strings.Split(options.Body, "\n")),
		Refresh:     options.Refresh,
		RefreshHint: options.RefreshHint,
		Trigger:     "command",
	}
	if !canOpenChatAccountScreen(session) {
		spec.ForceInline = true
		spec.ForceInlineReason = "unavailable"
		spec.Doc = chatScreenResultDocument(accountsScreenFallbackResult(req))
		spec.Refresh = nil
		spec.RefreshHint = ""
	}
	return spec
}

// chatScreenResultDocument 把一个结果单元格的文档投影为副屏降级文档：内容与
// 旧降级单元格一致（纯文本逐行重建），保证降级输出字符级不变。
func chatScreenResultDocument(result CommandResult) render.Document {
	return textLinesDocument(strings.Split(ui.RenderDocumentPlain(result.Document()), "\n"))
}
