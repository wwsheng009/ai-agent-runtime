package commands

import (
	"fmt"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/render"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/style"
	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
)

// buildChatLoadDocument renders the /load success confirmation (会话已加载 +
// current session summary) as a renderer-neutral document so the structured
// command path commits it as one retained command cell. It shares the exact
// meta layout with the legacy printCurrentRuntimeSession rows
// (chatSessionMetaLabelWidth alignment) and never touches the terminal itself:
// owned interactive dispatch renders the document through the interaction
// coordinator, while plain/JSON projections use their own renderer.
//
// Transcript replay is deliberately NOT part of this document: replay emits
// one cell per message through the replay renderer, so dispatch triggers it
// after committing this confirmation cell (CommandResult.ReplayHistory).
func buildChatLoadDocument(session *ChatSession) render.Document {
	if session == nil {
		return render.SingleLineDoc(render.RoleSpan("错误: 当前没有活动会话", string(style.RoleError)))
	}
	if session.RuntimeSession == nil {
		return render.SingleLineDoc(render.RoleSpan("错误: 会话加载失败", string(style.RoleError)))
	}
	var builder chatDebugDocumentBuilder
	builder.heading("会话已加载")
	appendChatLoadSessionMeta(&builder, session)
	return builder.document()
}

// buildChatResumeDocument is the no-terminal projection of a successful
// non-picker /resume. Like /load it keeps the confirmation separate from the
// subsequently replayed history cells, but retains the resume-specific title
// and conversation summary of the compatibility command.
func buildChatResumeDocument(session *ChatSession) render.Document {
	if session == nil {
		return render.SingleLineDoc(render.RoleSpan("错误: 当前没有活动会话", string(style.RoleError)))
	}
	if session.RuntimeSession == nil {
		return render.SingleLineDoc(render.RoleSpan("错误: 会话恢复失败", string(style.RoleError)))
	}
	turnCount, messageCount := runtimeSessionConversationCounts(session.RuntimeSession)
	title := runtimeResumeSessionTitle(session.RuntimeSession)
	var heading string
	if generation := runtimeSessionCompactGeneration(session.RuntimeSession); generation > 0 {
		heading = fmt.Sprintf("已恢复历史会话: %s（compact #%d · %d轮/%d条消息）", title, generation, turnCount, messageCount)
	} else {
		heading = fmt.Sprintf("已恢复历史会话: %s（%d轮/%d条消息）", title, turnCount, messageCount)
	}
	var builder chatDebugDocumentBuilder
	builder.heading(heading)
	appendChatLoadSessionMeta(&builder, session)
	return builder.document()
}

// buildChatCurrentSessionDocument is the semantic projection for /session.
// It shares the metadata rows with /load so the two session entry points
// cannot drift while the unified renderer owns the terminal.
func buildChatCurrentSessionDocument(session *ChatSession) render.Document {
	if session == nil || session.RuntimeSession == nil {
		return render.SingleLineDoc(render.RoleSpan("当前没有持久化会话", string(style.RoleError)))
	}
	var builder chatDebugDocumentBuilder
	builder.heading("当前会话")
	appendChatLoadSessionMeta(&builder, session)
	return builder.document()
}

func appendChatLoadSessionMeta(builder *chatDebugDocumentBuilder, session *ChatSession) {
	if session == nil || session.RuntimeSession == nil {
		return
	}
	preview := session.RuntimeSession.BuildPreview()
	if preview != nil {
		builder.meta("Session:", fmt.Sprintf("%s [%s]", preview.ID, preview.State))
	}
	if sessionPath := currentRuntimeSessionPath(session); sessionPath != "" {
		builder.meta("Session File:", sessionPath)
	}
	if store := currentRuntimeSessionStoreSummary(session); store != "" {
		builder.meta("Session Store:", store)
	}
	if logPath := currentChatLogFile(session); logPath != "" {
		builder.meta("Chat Log File:", logPath)
	}
	if debugPath := currentDebugLogFile(session); debugPath != "" {
		builder.meta("Debug Log File:", debugPath)
	}
	if artifactDir := currentRuntimeHTTPArtifactDir(session); artifactDir != "" {
		builder.meta("HTTP Artifact Dir:", artifactDir)
	}
	if artifactDir := currentLocalShellArtifactDir(session); artifactDir != "" {
		builder.meta("Shell Artifact Dir:", artifactDir)
	}
	if session.runtimeHTTPCapture != nil {
		snapshot := session.runtimeHTTPCapture.Snapshot()
		if snapshot.RequestArtifactPath != "" {
			builder.meta("Last HTTP Req:", resolveAbsoluteChatPath(snapshot.RequestArtifactPath))
		}
		if snapshot.ResponseArtifactPath != "" {
			builder.meta("Last HTTP Resp:", resolveAbsoluteChatPath(snapshot.ResponseArtifactPath))
		}
	}
	if path := currentLastLocalShellArtifactPath(session); path != "" {
		builder.meta("Last Shell Out:", path)
	}
	if preview != nil && preview.Title != "" {
		builder.meta("Title:", preview.Title)
	}
	appendChatLoadCompactLineage(builder, session)
	if preview != nil && preview.MessageCount > 0 {
		builder.meta("History:", fmt.Sprintf("%d messages", preview.MessageCount))
	}
}

// appendChatLoadCompactLineage mirrors the printChatSessionCompactLineage rows
// so /load, /session, and resume success surface generation + root title +
// root id consistently.
func appendChatLoadCompactLineage(builder *chatDebugDocumentBuilder, session *ChatSession) {
	if session == nil || session.RuntimeSession == nil {
		return
	}
	runtimeSession := session.RuntimeSession
	generation := runtimeSessionCompactGeneration(runtimeSession)
	if generation <= 0 {
		return
	}
	builder.meta("Compact Gen:", fmt.Sprintf("#%d", generation))
	if rootTitle := strings.TrimSpace(runtimeSessionContextString(runtimeSession, runtimechat.ContextCompactRootTitle)); rootTitle != "" {
		builder.meta("Compact Root:", rootTitle)
	}
	if rootID := strings.TrimSpace(runtimeSessionContextString(runtimeSession, runtimechat.ContextCompactRootSessionID)); rootID != "" {
		builder.meta("Compact Root ID:", rootID)
	}
}
