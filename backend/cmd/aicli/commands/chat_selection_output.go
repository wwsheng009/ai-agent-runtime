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
	_, _ = ui.WriteTerminalLine(os.Stderr, ui.RenderDocumentANSI(render.SingleLineDoc(spans...)))
}

func printChatSelectionPrompt(format string, args ...interface{}) {
	writeChatParts(os.Stderr, false, chatBoldPart(fmt.Sprintf(format, args...), style.RoleUser))
}

func printChatSelectionWarning(format string, args ...interface{}) {
	ui.PrintWarningTo(os.Stderr, format, args...)
}
