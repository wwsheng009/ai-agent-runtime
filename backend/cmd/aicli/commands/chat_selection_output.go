package commands

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/render"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/style"
)

type chatTextPart struct {
	text string
	role style.Role
	bold bool
}

func chatPart(text string, role style.Role) chatTextPart {
	return chatTextPart{text: text, role: role}
}

func chatBoldPart(text string, role style.Role) chatTextPart {
	return chatTextPart{text: text, role: role, bold: true}
}

// chatSelectionDiagnosticClaim 是 legacy 选择输出的收口，只用于
// printChatSelection{Line,Prompt,Warning}：登记中的交互式会话存在时绝不写裸
// stdout/stderr，而是投递动态栏（瞬时整行）；返回 false 时调用方保持原字节
// 输出。当前生产调用链上这些输出只在 presenter attach 之前（启动选择）或
// legacy 无 surface 分支可达（P0 台账 §3 item 5 侦察），这里是防止未来误接线
// 的兜底防线。
//
// 注意不要挂到 writeChatParts 这类通用行写：printChatSessionInfoRow 等会话
// 信息行也走它，劫持会改变调试/恢复输出的归属与绘制时序。
func chatSelectionDiagnosticClaim(text string) bool {
	text = strings.TrimSpace(ui.SanitizeTerminalText(text))
	if text == "" {
		return false
	}
	return NotifyChatDiagnostic(text)
}

func writeChatParts(writer io.Writer, newline bool, parts ...chatTextPart) {
	if writer == nil {
		return
	}
	spans := make([]render.Span, 0, len(parts))
	for _, part := range parts {
		spans = append(spans, render.Span{
			Text: ui.SanitizeTerminalText(part.text),
			Style: render.Style{
				Role: string(part.role),
				Bold: part.bold,
			},
		})
	}
	text := ui.RenderDocumentANSI(render.SingleLineDoc(spans...))
	if newline {
		_, _ = ui.WriteTerminalLine(writer, text)
	} else {
		_, _ = ui.WriteTerminalText(writer, text)
	}
}

func printChatSelectionParts(parts ...chatTextPart) {
	writeChatParts(os.Stderr, true, parts...)
}

func printChatSelectionMutedSuffix(primary string, muted ...string) {
	writeChatMutedSuffix(os.Stderr, primary, muted...)
}

func writeChatMutedSuffix(writer io.Writer, primary string, muted ...string) {
	parts := []chatTextPart{chatPart(primary, style.RoleTextPrimary)}
	for _, text := range muted {
		parts = append(parts, chatPart(text, style.RoleTextMuted))
	}
	writeChatParts(writer, true, parts...)
}

func printChatSelectionSection(title string) {
	title = strings.TrimSpace(title)
	if title == "" {
		return
	}

	printChatSelectionBlankLine()
	separator := ui.NewSeparator().SetTitle(fmt.Sprintf(" %s ", title)).Build()
	printChatSelectionLine("%s", separator)
	printChatSelectionBlankLine()
}

func printChatSelectionBlankLine() {
	_, _ = ui.WriteTerminalLine(os.Stderr, "")
}

func printChatSelectionLine(format string, args ...interface{}) {
	formatted := fmt.Sprintf(format, args...)
	spans := render.ANSIToSpans(formatted)
	for i := range spans {
		if spans[i].Style.Role == "" && !spans[i].Style.Foreground.IsSet() &&
			!spans[i].Style.Background.IsSet() && !spans[i].Style.Bold &&
			!spans[i].Style.Dim && !spans[i].Style.Italic &&
			!spans[i].Style.Underline && !spans[i].Style.Reverse {
			spans[i].Style.Role = string(style.RoleTextPrimary)
		}
	}
	rendered := ui.RenderDocumentANSI(render.SingleLineDoc(spans...))
	if chatSelectionDiagnosticClaim(rendered) {
		return
	}
	_, _ = ui.WriteTerminalLine(os.Stderr, rendered)
}

func printChatSelectionPrompt(format string, args ...interface{}) {
	message := fmt.Sprintf(format, args...)
	if chatSelectionDiagnosticClaim(message) {
		return
	}
	writeChatParts(os.Stderr, false, chatBoldPart(message, style.RoleUser))
}

func printChatSelectionWarning(format string, args ...interface{}) {
	if chatSelectionDiagnosticClaim(fmt.Sprintf(format, args...)) {
		return
	}
	ui.PrintWarningTo(os.Stderr, format, args...)
}
