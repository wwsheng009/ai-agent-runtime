package memoryruntime

import (
	"context"
	"fmt"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/agentcontrol"
	"github.com/wwsheng009/ai-agent-runtime/internal/chat"
	"github.com/wwsheng009/ai-agent-runtime/internal/sessionmeta"
	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

// ChatSessionSource adapts the canonical chat session store to the M2 write
// path. It is read-only: transcript loading never mutates a session.
type ChatSessionSource struct {
	Manager            *chat.SessionManager
	UserID             string
	MaxToolOutputRunes int
}

func (s *ChatSessionSource) ListSessions(ctx context.Context, workspace string, limit int) ([]SessionRef, error) {
	if s == nil || s.Manager == nil {
		return nil, fmt.Errorf("memoryruntime: chat session manager is nil")
	}
	storage := s.Manager.GetStorage()
	if storage == nil {
		return nil, fmt.Errorf("memoryruntime: chat session storage is nil")
	}
	var sessions []*chat.Session
	var err error
	if all, ok := storage.(chat.SessionStorageAllLister); ok {
		sessions, err = all.ListAll(ctx, limit, 0)
	} else if strings.TrimSpace(s.UserID) != "" {
		sessions, err = s.Manager.List(ctx, s.UserID)
	} else {
		return nil, fmt.Errorf("memoryruntime: chat storage does not support ListAll")
	}
	if err != nil {
		return nil, err
	}
	refs := make([]SessionRef, 0, len(sessions))
	for _, session := range sessions {
		if session == nil {
			continue
		}
		ref := s.toSessionRef(session)
		if strings.TrimSpace(workspace) != "" && ref.WorkspacePath != "" && !sameWorkspace(workspace, ref.WorkspacePath) {
			continue
		}
		refs = append(refs, ref)
	}
	return refs, nil
}

func (s *ChatSessionSource) ReadTranscript(ctx context.Context, session SessionRef, maxMessages int) (Transcript, error) {
	if s == nil || s.Manager == nil {
		return Transcript{}, fmt.Errorf("memoryruntime: chat session manager is nil")
	}
	if maxMessages <= 0 {
		maxMessages = 200
	}
	storage := s.Manager.GetStorage()
	if storage == nil {
		return Transcript{}, fmt.Errorf("memoryruntime: chat session storage is nil")
	}
	page, err := readCanonicalPage(ctx, storage, session.ID, maxMessages)
	if err != nil {
		return Transcript{}, err
	}
	if page == nil {
		return Transcript{}, nil
	}
	messages := make([]TranscriptMessage, 0, len(page.Messages))
	firstSeq := page.FirstSeq
	if firstSeq <= 0 {
		firstSeq = 1
	}
	for index, message := range page.Messages {
		seq := firstSeq + index
		id := strings.TrimSpace(types.MessageID(message))
		if id == "" {
			id = fmt.Sprintf("msg-%d", seq)
		}
		content := message.Content
		toolName := strings.TrimSpace(message.Metadata.GetString("tool_name", ""))
		truncated := false
		if strings.EqualFold(strings.TrimSpace(message.Role), "tool") {
			limit := s.MaxToolOutputRunes
			if limit <= 0 {
				limit = 800
			}
			if runes := []rune(content); len(runes) > limit {
				content = string(runes[:limit]) + "\n[tool output truncated]"
				truncated = true
			}
		}
		messages = append(messages, TranscriptMessage{
			ID: id, Seq: seq, Role: strings.TrimSpace(message.Role), Content: content,
			ToolName: toolName, ToolCallID: strings.TrimSpace(message.ToolCallID), Truncated: truncated,
		})
	}
	transcript := Transcript{
		SessionID:     session.ID,
		WorkspacePath: session.WorkspacePath,
		UpdatedAt:     session.LastActivityAt,
		Revision:      int64(page.Total),
		Messages:      messages,
	}
	if transcript.UpdatedAt.IsZero() {
		transcript.UpdatedAt = session.UpdatedAt
	}
	transcript.Hash = TranscriptHash(messages)
	return transcript, nil
}

// readCanonicalPage reads the canonical transcript without invoking
// SessionManager's identity backfill, which can persist updates. Memory
// extraction must remain read-only.
func readCanonicalPage(ctx context.Context, storage chat.SessionStorage, sessionID string, limit int) (*chat.SessionHistoryPage, error) {
	if pager, ok := storage.(chat.SessionStorageHistoryPager); ok {
		return pager.GetMessagePage(ctx, sessionID, 0, limit)
	}
	if reader, ok := storage.(chat.SessionStorageRecentHistoryReader); ok {
		messages, err := reader.GetRecentMessages(ctx, sessionID, limit)
		if err != nil {
			return nil, err
		}
		total := len(messages)
		if counter, ok := storage.(chat.SessionStorageMessageCounter); ok {
			if count, countErr := counter.MessageCount(ctx, sessionID); countErr == nil {
				total = count
			}
		}
		return &chat.SessionHistoryPage{
			Messages: messages, Total: total, FirstSeq: total - len(messages) + 1,
			LastSeq: total,
		}, nil
	}
	session, err := storage.Load(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	history := session.History
	if len(history) > limit {
		history = history[len(history)-limit:]
	}
	first := session.CanonicalMessageCount - len(history) + 1
	if first <= 0 {
		first = 1
	}
	return &chat.SessionHistoryPage{
		Messages: history, Total: session.CanonicalMessageCount,
		FirstSeq: first, LastSeq: first + len(history) - 1,
	}, nil
}

func (s *ChatSessionSource) toSessionRef(session *chat.Session) SessionRef {
	workspace := agentcontrol.ContextString(session, sessionmeta.WorkspacePath)
	parentSessionID := agentcontrol.ContextString(session, agentcontrol.SessionContextParentSessionID)
	agentType := agentcontrol.ContextString(session, agentcontrol.SessionContextAgentType)
	ephemeral := false
	for _, key := range []string{"ephemeral", "temporary", "transient"} {
		if value, ok := session.GetContext(key); ok {
			switch typed := value.(type) {
			case bool:
				ephemeral = ephemeral || typed
			case string:
				lower := strings.ToLower(strings.TrimSpace(typed))
				ephemeral = ephemeral || lower == "true" || lower == "1" || lower == "yes"
			}
		}
	}
	return SessionRef{
		ID: session.ID, UserID: session.UserID, WorkspacePath: workspace,
		State: string(session.State), IsSubagent: parentSessionID != "" || agentType != "",
		Ephemeral: ephemeral, UpdatedAt: session.UpdatedAt,
		LastActivityAt: session.ActivityAt(), CanonicalCount: session.CanonicalMessageCount,
	}
}
