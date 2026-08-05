package commands

import (
	"fmt"
	"strings"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/render"
	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
)

// buildChatPlainTextCommandDocument turns a legacy finite text result into a
// renderer-neutral command document. It intentionally preserves empty rows:
// slash help and JSON reports use them as part of their existing layout.
func buildChatPlainTextCommandDocument(text string) render.Document {
	if text == "" {
		return render.Document{}
	}
	return textLinesDocument(strings.Split(text, "\n"))
}

func buildChatSlashHelpDocument() render.Document {
	return textLinesDocument(buildChatSlashHelpLines())
}

func buildChatFunctionDescriptorDocument(session *ChatSession, name string, jsonOutput bool) render.Document {
	return buildChatPlainTextCommandDocument(formatFunctionDescriptor(session, name, jsonOutput))
}

func buildChatFunctionCatalogDocument(session *ChatSession, jsonOutput bool) render.Document {
	return buildChatPlainTextCommandDocument(formatFunctionCatalogSummary(session, jsonOutput))
}

func buildChatFunctionExposureDocument(session *ChatSession, prompt string, jsonOutput bool) render.Document {
	return buildChatPlainTextCommandDocument(formatFunctionExposurePreview(session, prompt, jsonOutput))
}

// buildChatSessionSummariesDocument is the non-terminal projection of
// printChatSessionSummaries. The latter remains for startup/plain callers;
// interactive slash commands must use this document so their list cannot
// bypass the primary terminal transaction.
func buildChatSessionSummariesDocument(manager *runtimechat.SessionManager, userID, currentID string, filter ChatSessionListFilter) (render.Document, error) {
	if manager == nil {
		return render.Document{}, fmt.Errorf("会话管理未启用")
	}

	sessions, err := listFilteredChatSessionsExcluding(manager, userID, filter, currentID)
	if err != nil {
		return render.Document{}, err
	}

	if len(sessions) == 0 {
		if strings.TrimSpace(currentID) != "" {
			return render.SingleLineDoc(render.TextSpan("暂无其他历史会话")), nil
		}
		return render.SingleLineDoc(render.TextSpan("暂无可用会话")), nil
	}

	lines := make([]string, 0, len(sessions)*3+1)
	if strings.TrimSpace(currentID) != "" {
		lines = append(lines, "历史会话:")
	} else {
		lines = append(lines, "可用会话:")
	}
	now := timeNowForChatSessionSummary()
	for _, item := range sessions {
		if item == nil {
			continue
		}
		lines = append(lines, clampSessionSummaryLines(renderRuntimeSessionSummaryLines(item, now), ui.GetTerminalWidth())...)
	}
	return textLinesDocument(lines), nil
}

// timeNowForChatSessionSummary is isolated so the document builder retains the
// exact recency formatting policy of the old terminal renderer while staying
// simple to exercise in tests.
var timeNowForChatSessionSummary = func() time.Time { return time.Now() }

func buildChatNewSessionDocument(session *ChatSession) render.Document {
	if session == nil || session.RuntimeSession == nil {
		return render.SingleLineDoc(render.TextSpan("错误: 创建新会话失败"))
	}
	var builder chatDebugDocumentBuilder
	builder.heading("已创建新会话")
	appendChatLoadSessionMeta(&builder, session)
	return builder.document()
}
