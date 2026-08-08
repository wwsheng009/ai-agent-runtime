// Package boundary 提供 transcript 边界与事件块显示 chrome 的叶子级
// 共享常量/助手（不依赖 ui 包，避免 ui/scene → ui 循环依赖）。
package boundary

import "strings"

// AssistantStreamMarkerText 是 assistant 事件块的统一首行标识。
// 事件流（assistant 正文 / 工具运行 / 工具完成）各自带标识，正文块与
// 工具块共用 "• "，便于滚动回看时快速区分事件边界。
const AssistantStreamMarkerText = "• "

// AssistantStreamMarker 返回 assistant 事件块的统一首行标识。
func AssistantStreamMarker() string {
	return AssistantStreamMarkerText
}

// AssistantContentIndent 返回 assistant 块续行的缩进：等于标识的显示
// 宽度（"• " 为 2 列），保证续行与首行内容对齐。
func AssistantContentIndent() string {
	return "  "
}

// FormatAssistantBlockChrome 把纯文本 assistant 块渲染为带统一 chrome 的
// 事件块：首行 "• " 标识，后续行按标识宽度缩进。markdown 正文不走此路径
// （保持 markdown 结构原样，仅由 IndentAssistantContent 统一缩进）。
func FormatAssistantBlockChrome(content string) string {
	if content == "" {
		return ""
	}
	indent := AssistantContentIndent()
	lines := strings.Split(content, "\n")
	for i, line := range lines {
		if i == 0 {
			lines[i] = AssistantStreamMarker() + line
		} else {
			lines[i] = indent + line
		}
	}
	return strings.Join(lines, "\n")
}

// StripAssistantBlockChrome 移除 FormatAssistantBlockChrome 添加的首行标识
// 与续行缩进，返回原始内容与首行 chrome 字节数。调用方用剥离后的内容推导
// 语义边界（如流式稳定前缀），保证偏移始终是原始内容坐标而不是展示文本坐标。
func StripAssistantBlockChrome(content string) (string, int) {
	if content == "" || !strings.HasPrefix(content, AssistantStreamMarker()) {
		return content, 0
	}
	indent := AssistantContentIndent()
	lines := strings.Split(content, "\n")
	for i, line := range lines {
		if i == 0 {
			lines[i] = strings.TrimPrefix(line, AssistantStreamMarker())
		} else {
			lines[i] = strings.TrimPrefix(line, indent)
		}
	}
	return strings.Join(lines, "\n"), len(AssistantStreamMarker())
}

// IndentAssistantContent 给每行加上 assistant 块缩进（markdown 等结构化
// 内容只缩进、不加首行标识）。
func IndentAssistantContent(content string) string {
	if content == "" {
		return ""
	}
	indent := AssistantContentIndent()
	lines := strings.Split(content, "\n")
	for i, line := range lines {
		lines[i] = indent + line
	}
	return strings.Join(lines, "\n")
}
