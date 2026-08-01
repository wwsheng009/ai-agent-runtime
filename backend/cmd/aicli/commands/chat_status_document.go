package commands

import (
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/render"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/style"
)

// buildChatStatusDocument renders the /status box as a renderer-neutral
// document so the structured command path can commit it as one retained
// command cell. It shares the exact box layout with the legacy path
// (buildChatStatusBoxLines) and never touches the terminal itself: owned
// interactive dispatch renders the document through the interaction
// coordinator, while plain/JSON projections use their own renderer.
func buildChatStatusDocument(session *ChatSession) render.Document {
	if session == nil {
		return render.SingleLineDoc(render.RoleSpan("错误: 当前没有活动会话", string(style.RoleError)))
	}
	lines := buildChatStatusBoxLines(session, resolveChatStatusBoxContentWidth())
	docLines := make([]render.Line, 0, len(lines))
	for _, line := range lines {
		docLines = append(docLines, render.Line{Spans: []render.Span{render.TextSpan(line)}})
	}
	return render.LinesDoc(docLines...)
}
