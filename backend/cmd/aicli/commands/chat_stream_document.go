package commands

import (
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/render"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/style"
)

// This file owns the renderer-neutral documents for the structured /stream
// path. It never touches the terminal: owned interactive dispatch renders the
// documents through the interaction coordinator, while plain/JSON projections
// use their own renderer. Parse errors and nil-session errors stay on the
// legacy path so the message remains visible in every mode.

// buildChatStreamStatusDocument renders the /stream status view, mirroring the
// legacy printStreamCommandStatus lines: current mode plus the configured
// default (when a config file records one).
func buildChatStreamStatusDocument(session *ChatSession) render.Document {
	if session == nil {
		return render.SingleLineDoc(render.RoleSpan("错误: 当前没有活动会话", string(style.RoleError)))
	}
	lines := make([]string, 0, 2)
	if session.Stream {
		lines = append(lines, "当前输出模式: 流式 (stream)")
	} else {
		lines = append(lines, "当前输出模式: 普通 (normal)")
	}
	if session.Config != nil && session.Config.AICLI != nil && session.Config.AICLI.Chat != nil && session.Config.AICLI.Chat.Stream != nil {
		if *session.Config.AICLI.Chat.Stream {
			lines = append(lines, "配置默认: stream")
		} else {
			lines = append(lines, "配置默认: normal")
		}
	} else {
		lines = append(lines, "配置默认: (未设置)")
	}
	return textLinesDocument(lines)
}

// buildChatStreamToggleDocument renders the /stream toggle/set confirmation,
// mirroring the legacy "提示: 已切换到..." line.
func buildChatStreamToggleDocument(session *ChatSession) render.Document {
	if session != nil && session.Stream {
		return render.SingleLineDoc(render.TextSpan("提示: 已切换到流式模式"))
	}
	return render.SingleLineDoc(render.TextSpan("提示: 已切换到普通模式"))
}
