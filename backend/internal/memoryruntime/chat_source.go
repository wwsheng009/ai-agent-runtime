package memoryruntime

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

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

var (
	_ TurnCaptureReceiptSource                  = (*ChatSessionSource)(nil)
	_ SessionTurnCaptureReceiptSource           = (*ChatSessionSource)(nil)
	_ SessionTurnCaptureReceiptGenerationSource = (*ChatSessionSource)(nil)
)

func (s *ChatSessionSource) receiptStore() (chat.SessionStorageTurnCaptureReceiptStore, error) {
	if s == nil || s.Manager == nil || s.Manager.GetStorage() == nil {
		return nil, fmt.Errorf("memoryruntime: chat session storage is nil")
	}
	store, ok := s.Manager.GetStorage().(chat.SessionStorageTurnCaptureReceiptStore)
	if !ok {
		return nil, fmt.Errorf("memoryruntime: chat session storage has no turn capture receipt store")
	}
	return store, nil
}

func (s *ChatSessionSource) ListTurnCaptureReceipts(ctx context.Context, limit int) ([]chat.TurnCaptureReceipt, error) {
	store, err := s.receiptStore()
	if err != nil {
		return nil, err
	}
	return store.ListTurnCaptureReceipts(ctx, limit)
}

func (s *ChatSessionSource) ListTurnCaptureReceiptsForSession(ctx context.Context, sessionID string, limit int) ([]chat.TurnCaptureReceipt, error) {
	store, err := s.receiptStore()
	if err != nil {
		return nil, err
	}
	return store.ListTurnCaptureReceiptsForSession(ctx, sessionID, limit)
}

func (s *ChatSessionSource) ListMaterializedTurnCaptureReceipts(ctx context.Context, limit int) ([]chat.TurnCaptureReceipt, error) {
	store, err := s.receiptStore()
	if err != nil {
		return nil, err
	}
	return store.ListMaterializedTurnCaptureReceipts(ctx, limit)
}

func (s *ChatSessionSource) ListMaterializedTurnCaptureReceiptsPage(
	ctx context.Context,
	after *chat.TurnCaptureReceipt,
	limit int,
) ([]chat.TurnCaptureReceipt, error) {
	store, err := s.receiptStore()
	if err != nil {
		return nil, err
	}
	return store.ListMaterializedTurnCaptureReceiptsPage(ctx, after, limit)
}

func (s *ChatSessionSource) ListMaterializedTurnCaptureReceiptsForSession(ctx context.Context, sessionID string, limit int) ([]chat.TurnCaptureReceipt, error) {
	store, err := s.receiptStore()
	if err != nil {
		return nil, err
	}
	return store.ListMaterializedTurnCaptureReceiptsForSession(ctx, sessionID, limit)
}

func (s *ChatSessionSource) ListMaterializedTurnCaptureReceiptsForSessionPage(
	ctx context.Context,
	sessionID string,
	after *chat.TurnCaptureReceipt,
	limit int,
) ([]chat.TurnCaptureReceipt, error) {
	store, err := s.receiptStore()
	if err != nil {
		return nil, err
	}
	return store.ListMaterializedTurnCaptureReceiptsForSessionPage(ctx, sessionID, after, limit)
}

func (s *ChatSessionSource) ListMaterializedTurnCaptureReceiptsForSessionGenerationPage(
	ctx context.Context,
	sessionID string,
	generation int64,
	after *chat.TurnCaptureReceipt,
	limit int,
) ([]chat.TurnCaptureReceipt, error) {
	store, err := s.receiptStore()
	if err != nil {
		return nil, err
	}
	generationStore, ok := store.(chat.SessionStorageMaterializedTurnCaptureReceiptGenerationPageStore)
	if !ok {
		return nil, fmt.Errorf("memoryruntime: chat session storage has no generation-scoped materialized capture receipt page")
	}
	return generationStore.ListMaterializedTurnCaptureReceiptsForSessionGenerationPage(
		ctx, sessionID, generation, after, limit,
	)
}

func (s *ChatSessionSource) HasTurnCaptureReceiptsForSession(ctx context.Context, sessionID string) (bool, error) {
	store, err := s.receiptStore()
	if err != nil {
		return false, err
	}
	return store.HasTurnCaptureReceiptsForSession(ctx, sessionID)
}

func (s *ChatSessionSource) ListTurnCaptureReceiptsForSessionGeneration(
	ctx context.Context, sessionID string, generation int64, limit int,
) ([]chat.TurnCaptureReceipt, error) {
	store, err := s.receiptStore()
	if err != nil {
		return nil, err
	}
	generationStore, ok := store.(chat.SessionStorageTurnCaptureReceiptGenerationStore)
	if !ok {
		return nil, fmt.Errorf("memoryruntime: chat session storage has no generation-scoped capture receipt store")
	}
	return generationStore.ListTurnCaptureReceiptsForSessionGeneration(ctx, sessionID, generation, limit)
}

func (s *ChatSessionSource) HasTurnCaptureReceiptsForSessionGeneration(
	ctx context.Context, sessionID string, generation int64,
) (bool, error) {
	store, err := s.receiptStore()
	if err != nil {
		return false, err
	}
	generationStore, ok := store.(chat.SessionStorageTurnCaptureReceiptGenerationStore)
	if !ok {
		return false, fmt.Errorf("memoryruntime: chat session storage has no generation-scoped capture receipt store")
	}
	return generationStore.HasTurnCaptureReceiptsForSessionGeneration(ctx, sessionID, generation)
}

func (s *ChatSessionSource) GetTurnCaptureReceipt(ctx context.Context, id string) (chat.TurnCaptureReceipt, error) {
	store, err := s.receiptStore()
	if err != nil {
		return chat.TurnCaptureReceipt{}, err
	}
	return store.GetTurnCaptureReceipt(ctx, id)
}

func (s *ChatSessionSource) ValidateTurnCaptureReceipt(ctx context.Context, receipt chat.TurnCaptureReceipt) error {
	store, err := s.receiptStore()
	if err != nil {
		return err
	}
	return store.ValidateTurnCaptureReceipt(ctx, receipt)
}

func (s *ChatSessionSource) MarkTurnCaptureReceiptMaterialized(ctx context.Context, id string) error {
	store, err := s.receiptStore()
	if err != nil {
		return err
	}
	return store.MarkTurnCaptureReceiptMaterialized(ctx, id)
}

func (s *ChatSessionSource) DeferTurnCaptureReceipt(ctx context.Context, id, reason string, retryAt time.Time) error {
	store, err := s.receiptStore()
	if err != nil {
		return err
	}
	return store.DeferTurnCaptureReceipt(ctx, id, reason, retryAt)
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

// ReadTranscriptWindow reads the first bounded page of a canonical range, never
// its tail. Runtime.RunSessionWindow advances across all required subwindows.
// Large histories require a pager; the legacy loader is only for small stores.
func (s *ChatSessionSource) ReadTranscriptWindow(ctx context.Context, session SessionRef, startSeq, endSeq, maxMessages int) (Transcript, error) {
	if s == nil || s.Manager == nil {
		return Transcript{}, fmt.Errorf("memoryruntime: chat session manager is nil")
	}
	if startSeq <= 0 {
		startSeq = 1
	}
	if endSeq < startSeq {
		return Transcript{}, fmt.Errorf("memoryruntime: invalid transcript window %d-%d", startSeq, endSeq)
	}
	if maxMessages <= 0 {
		maxMessages = 200
	}
	if endSeq-startSeq >= maxMessages {
		endSeq = startSeq + maxMessages - 1
	}
	storage := s.Manager.GetStorage()
	if storage == nil {
		return Transcript{}, fmt.Errorf("memoryruntime: chat session storage is nil")
	}

	type seqMessage struct {
		seq     int
		message types.Message
	}
	collected := make([]seqMessage, 0)
	seen := make(map[int]struct{})
	appendMessage := func(seq int, message types.Message) {
		if seq < startSeq || seq > endSeq {
			return
		}
		if _, ok := seen[seq]; ok {
			return
		}
		seen[seq] = struct{}{}
		collected = append(collected, seqMessage{seq: seq, message: message})
	}

	if pager, ok := storage.(chat.SessionStorageHistoryPager); ok {
		beforeSeq := endSeq + 1
		remaining := endSeq - startSeq + 1
		if remaining > maxMessages {
			remaining = maxMessages
		}
		for beforeSeq > startSeq && len(collected) < remaining {
			limit := remaining - len(collected)
			page, err := pager.GetMessagePage(ctx, session.ID, beforeSeq, limit)
			if err != nil {
				return Transcript{}, err
			}
			if page == nil || len(page.Messages) == 0 {
				break
			}
			firstSeq := page.FirstSeq
			if firstSeq <= 0 {
				firstSeq = beforeSeq - len(page.Messages)
			}
			for index, message := range page.Messages {
				appendMessage(firstSeq+index, message)
			}
			if firstSeq <= startSeq || firstSeq >= beforeSeq {
				break
			}
			beforeSeq = firstSeq
		}
	} else {
		if session.CanonicalCount > maxMessages {
			return Transcript{}, fmt.Errorf("memoryruntime: bounded canonical window reader unavailable for session %s", session.ID)
		}
		loaded, err := storage.Load(ctx, session.ID)
		if err != nil {
			return Transcript{}, err
		}
		firstSeq := loaded.CanonicalMessageCount - len(loaded.History) + 1
		if firstSeq <= 0 {
			firstSeq = 1
		}
		for index, message := range loaded.History {
			appendMessage(firstSeq+index, message)
		}
	}

	sort.SliceStable(collected, func(i, j int) bool { return collected[i].seq < collected[j].seq })
	messages := make([]TranscriptMessage, 0, len(collected))
	for _, item := range collected {
		id := strings.TrimSpace(types.MessageID(item.message))
		if id == "" {
			id = fmt.Sprintf("msg-%d", item.seq)
		}
		content := item.message.Content
		toolName := strings.TrimSpace(item.message.Metadata.GetString("tool_name", ""))
		truncated := false
		if strings.EqualFold(strings.TrimSpace(item.message.Role), "tool") {
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
			ID: id, Seq: item.seq, Role: strings.TrimSpace(item.message.Role),
			Content: content, ToolName: toolName,
			ToolCallID: strings.TrimSpace(item.message.ToolCallID), Truncated: truncated,
		})
	}
	transcript := Transcript{
		SessionID: session.ID, WorkspacePath: session.WorkspacePath,
		UpdatedAt: session.LastActivityAt, Revision: int64(endSeq), Messages: messages,
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
		ID: session.ID, UserID: session.UserID, SessionGeneration: session.SessionGeneration, WorkspacePath: workspace,
		State: string(session.State), IsSubagent: parentSessionID != "" || agentType != "",
		Ephemeral: ephemeral, UpdatedAt: session.UpdatedAt,
		LastActivityAt: session.ActivityAt(), CanonicalCount: session.CanonicalMessageCount,
	}
}
