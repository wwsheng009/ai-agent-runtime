package commands

import (
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/render"
)

// chat_screen_batch3.go 汇总批次 3（长文档迁入）的副屏 Spec（方案 §5.2（2））。
//
// 迁移口径：
//   - 只读长文档（/help、/status、/sessions、/plans detail、/functions、
//     /timeline、/collab、/hotkeys、/agents panel full、/agent transcript）
//     在统一出口改为 ScreenDocument；正文复用各命令既有文档/行构建函数，
//     保证字符级一致；
//   - 短确认/错误/单行状态（reload 回执、参数错误、导航回执）仍留在主屏
//     内联单元格（§5.1 ≤3 行判据）；
//   - 非统一会话（plain/JSON/headless）与 legacy 开关回退路径不经过本文件。
//
// 所有 Spec 的 Trigger 固定为 "command"：当前批次没有 hotkey 触发的长文档。

// chatScreenDocumentSpec 构造只读副屏 Spec；批次 3 统一入口。
func chatScreenDocumentSpec(id, title string, doc render.Document) chatScreenSpec {
	return chatScreenSpec{
		ID:      id,
		Title:   title,
		Kind:    chatScreenDocument,
		Doc:     doc,
		Trigger: "command",
	}
}

// chatScreenDocResult 把只读 Spec 包装为命令结果（不提交内联单元格）。
func chatScreenDocResult(spec chatScreenSpec) CommandResult {
	return CommandResult{Screen: &spec, Action: CommandContinue}
}

// chatScreenTextDoc 把既有文本投影转为副屏文档（保持逐行一致）。
func chatScreenTextDoc(text string) render.Document {
	return textLinesDocument(strings.Split(text, "\n"))
}

// chatScreenHelpSpec 映射 /help、/?：正文与 legacy/plain 出口同源
// （buildChatSlashHelpDocument）。
func chatScreenHelpSpec() chatScreenSpec {
	return chatScreenDocumentSpec("help.screen", "命令帮助", buildChatSlashHelpDocument())
}

// chatScreenStatusSpec 映射 /status：多段只读快照（buildChatStatusDocument）。
func chatScreenStatusSpec(session *ChatSession) chatScreenSpec {
	return chatScreenDocumentSpec("status.screen", "会话状态", buildChatStatusDocument(session))
}

// chatScreenSessionsSpec 映射 /sessions：条目随数量线性增长的历史会话列表。
func chatScreenSessionsSpec(doc render.Document) chatScreenSpec {
	return chatScreenDocumentSpec("sessions.screen", "历史会话", doc)
}

// chatScreenPlansDetailSpec 映射 /plans <id>：归档计划详情与最新正文。
func chatScreenPlansDetailSpec(doc render.Document) chatScreenSpec {
	return chatScreenDocumentSpec("plans.detail", "计划详情", doc)
}

// chatScreenFunctionsSpec 映射 /functions、/catalog：函数目录与暴露预览
// （title 区分两个变体）。
func chatScreenFunctionsSpec(title string, doc render.Document) chatScreenSpec {
	return chatScreenDocumentSpec("functions.screen", title, doc)
}

// chatScreenHotkeysSpec 映射 /hotkeys：静态键位表（典型分页器内容）。
func chatScreenHotkeysSpec() chatScreenSpec {
	return chatScreenDocumentSpec("hotkeys.screen", "快捷键", textLinesDocument(buildChatHotkeysLines(nil)))
}

// chatScreenTimelineSpec 映射 /timeline：带 limit/filter 的团队事件流。
func chatScreenTimelineSpec(session *ChatSession, command string) chatScreenSpec {
	return chatScreenDocumentSpec("timeline.screen", "协作时间线",
		chatScreenTextDoc(chatTimelineCommandText(session, command)))
}

// chatScreenCollabSpec 映射 /collab：邮箱快照（含 follow 窗口行）。
func chatScreenCollabSpec(session *ChatSession, command string) chatScreenSpec {
	return chatScreenDocumentSpec("collab.screen", "协作邮箱",
		chatScreenTextDoc(chatCollabCommandText(session, command)))
}

// chatScreenAgentPanelSpec 映射 /agents panel full|follow：完整详情天然分页
// （limit 默认 200、上限 2000）。lines 由调用方按既有面板渲染函数生成。
func chatScreenAgentPanelSpec(lines []string) chatScreenSpec {
	return chatScreenDocumentSpec("agents.panel", "Agent 面板", textLinesDocument(lines))
}

// chatScreenAgentTranscriptSpec 映射 /agent transcript：子会话只读 transcript。
func chatScreenAgentTranscriptSpec(lines []string) chatScreenSpec {
	return chatScreenDocumentSpec("agent.transcript", "Agent Transcript", textLinesDocument(lines))
}

// ---------------------------------------------------------------------------
// 批次 3 尾批：只读 list/status 变体（方案 §5.2（2）末行）
//
// 这些命令此前只在同命令内区分「只读内联 / 交互副屏」两种形态；尾批把只读
// 变体也投影为 ScreenDocument，消除通道分裂。JSON / plain / legacy 出口
// 仍走各自既有实现，正文与副屏同源。
// ---------------------------------------------------------------------------

// chatScreenModelStatusSpec 映射 /model status、/provider status，以及无 picker
// 表面时 bare /model、/provider 的降级状态页。
func chatScreenModelStatusSpec(variant modelCommandVariant, text string) chatScreenSpec {
	id, title := "model.status", "模型状态"
	if variant == modelCommandVariantProvider {
		id, title = "provider.status", "Provider 状态"
	}
	return chatScreenDocumentSpec(id, title, chatScreenTextDoc(text))
}

// chatScreenThemeReadOnlySpec 映射 /theme status|list|preview（variant 为
// "status"/"list"/"preview"），正文复用既有主题文档构建函数。
func chatScreenThemeReadOnlySpec(variant string, doc render.Document) chatScreenSpec {
	switch variant {
	case "list":
		return chatScreenDocumentSpec("theme.list", "主题列表", doc)
	case "preview":
		return chatScreenDocumentSpec("theme.preview", "主题预览", doc)
	default:
		return chatScreenDocumentSpec("theme.status", "主题状态", doc)
	}
}

// chatScreenSkillsListSpec 映射 /skills list|ls|status，以及 picker 不可用时的
// 全量目录降级页（两者正文同为 buildChatSkillCatalogDocument）。
func chatScreenSkillsListSpec(doc render.Document) chatScreenSpec {
	return chatScreenDocumentSpec("skills.list", "技能列表", doc)
}

// chatScreenMCPReadOnlySpec 映射 /mcp list|ls|status：服务器清单与逐服务器
// 状态是有限只读页面；启停/热重载回执仍留在主屏内联单元格。
func chatScreenMCPReadOnlySpec(id, title, text string) chatScreenSpec {
	return chatScreenDocumentSpec(id, title, chatScreenTextDoc(text))
}

// chatScreenProfileReadOnlySpec 映射 /profile status|list|show|diff：只读报告
// 走副屏；use/reload/off/save/import 等写入类回执保持内联。
func chatScreenProfileReadOnlySpec(id, title, text string) chatScreenSpec {
	return chatScreenDocumentSpec(id, title, chatScreenTextDoc(text))
}
