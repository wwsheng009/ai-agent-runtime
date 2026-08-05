package commands

import (
	"fmt"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/render"
)

// buildChatFastStatusDocument is the renderer-neutral projection of /fast
// status. It intentionally describes an unsupported protocol instead of
// attempting to open a picker or write a transient terminal warning.
func buildChatFastStatusDocument(session *ChatSession) render.Document {
	if session == nil {
		return buildChatPlainTextCommandDocument("错误: 当前没有活动会话")
	}
	lines := make([]string, 0, 3)
	if !chatSessionSupportsFastMode(session) {
		protocol := strings.TrimSpace(session.Provider.GetProtocol())
		if protocol == "" {
			protocol = "(unknown)"
		}
		lines = append(lines, fmt.Sprintf("当前协议: %s（Fast 仅对 codex 生效）", protocol))
	}
	if session.FastMode {
		lines = append(lines, "当前 Fast 模式: on (priority)")
	} else {
		lines = append(lines, "当前 Fast 模式: off")
	}
	if session.Config != nil && session.Config.AICLI != nil && session.Config.AICLI.Chat != nil && session.Config.AICLI.Chat.FastMode != nil {
		if *session.Config.AICLI.Chat.FastMode {
			lines = append(lines, "配置默认: on")
		} else {
			lines = append(lines, "配置默认: off")
		}
	} else {
		lines = append(lines, "配置默认: (未设置)")
	}
	return textLinesDocument(lines)
}

func buildChatFastToggleDocument(session *ChatSession) render.Document {
	if session != nil && session.FastMode {
		return render.SingleLineDoc(render.TextSpan("提示: 已开启 Fast 模式（service_tier=priority）"))
	}
	return render.SingleLineDoc(render.TextSpan("提示: 已关闭 Fast 模式"))
}
