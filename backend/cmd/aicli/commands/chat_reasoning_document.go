package commands

import (
	"fmt"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/render"
	runtimetypes "github.com/wwsheng009/ai-agent-runtime/internal/types"
)

func buildChatReasoningStatusDocument(session *ChatSession) render.Document {
	if chatReasoningOutputEnabled(session) {
		return render.SingleLineDoc(render.TextSpan("当前 reasoning: on"))
	}
	return render.SingleLineDoc(render.TextSpan("当前 reasoning: off"))
}

func buildChatReasoningEffortStatusDocument(session *ChatSession) render.Document {
	if session == nil {
		return buildChatPlainTextCommandDocument("错误: 当前没有活动会话")
	}
	reasoning := runtimetypes.NormalizeReasoningEffort(session.ReasoningEffort)
	if reasoning == "" {
		reasoning = "(无)"
	}
	lines := []string{fmt.Sprintf("当前 reasoning_effort: %s", reasoning)}
	catalog := reasoningEffortCatalogForModel(session.Provider, effectiveRuntimeModel(session))
	if len(catalog.options) == 0 {
		lines = append(lines, "可选 reasoning_effort: (未声明)")
	} else {
		lines = append(lines, "可选 reasoning_effort: "+strings.Join(catalog.options, ", "))
	}
	return textLinesDocument(lines)
}
