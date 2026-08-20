package commands

import (
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/style"
)

// These helpers keep string-focused assertions readable while ensuring tests
// exercise the same StatusLineModel -> Document projection as production.

// parseSurfaceStatusForTest is the test-fixture-only explicit string -> status
// mapping. Production code no longer parses state strings for semantics
// (structured chatSurfaceStatus carries the meaning); this table exists purely
// so legacy string-style test assertions stay readable. New state families
// must be added here AND to the structured consumers.
func parseSurfaceStatusForTest(state string) chatSurfaceStatus {
	switch strings.ToLower(strings.TrimSpace(state)) {
	case "ready", "completed", "failed":
		return chatSurfaceStatus{kind: chatSurfaceStatusIdle}
	case "waiting":
		return chatSurfaceStatus{kind: chatSurfaceStatusWaiting}
	case "thinking":
		return chatSurfaceStatus{kind: chatSurfaceStatusThinking}
	case "streaming":
		return chatSurfaceStatus{kind: chatSurfaceStatusStreaming}
	case "planning":
		return chatSurfaceStatus{kind: chatSurfaceStatusPlanning}
	case "stopping":
		return chatSurfaceStatus{kind: chatSurfaceStatusStopping}
	case "awaiting approval":
		return chatSurfaceStatus{kind: chatSurfaceStatusApproval}
	case "awaiting answer":
		return chatSurfaceStatus{kind: chatSurfaceStatusAnswer}
	case "retrying":
		return chatSurfaceStatus{kind: chatSurfaceStatusRetrying}
	}
	trimmed := strings.TrimSpace(state)
	if strings.HasPrefix(strings.ToLower(trimmed), "tool ") {
		return chatSurfaceStatus{kind: chatSurfaceStatusTool, detail: strings.TrimSpace(trimmed[len("tool "):])}
	}
	if strings.HasPrefix(strings.ToLower(trimmed), "retrying ") {
		return chatSurfaceStatus{kind: chatSurfaceStatusRetrying, detail: strings.TrimSpace(trimmed[len("retrying "):])}
	}
	return chatSurfaceStatus{kind: chatSurfaceStatusNotice, detail: trimmed}
}

func buildChatSurfaceStatusLine(session *ChatSession, state string) string {
	return buildChatSurfaceStatusLineForWidth(session, state, ui.GetTerminalWidth())
}

func buildChatSurfaceStatusLineForWidth(session *ChatSession, state string, width int) string {
	status := parseSurfaceStatusForTest(state)
	return buildChatSurfaceStatusLineForWidthAndInputMode(session, status, width, chatInputModeForSurfaceState(status))
}

func buildChatSurfaceStatusLineForWidthAndInputMode(session *ChatSession, s chatSurfaceStatus, width int, inputMode chatInputMode) string {
	model := buildChatSurfaceStatusModelForWidthAndInputMode(session, s, width, inputMode)
	return style.StatusLineDocument(model, width).PlainText()
}
