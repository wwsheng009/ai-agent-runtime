package commands

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/acp"
	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
)

// acpSessionListPageSize caps one session/list page. Clients page with the
// returned nextCursor; a small page keeps the history panel responsive even
// when the store holds thousands of sessions.
const acpSessionListPageSize = 50

// sessionStoreLocked returns the durable session store used by session/list
// and session/delete. The manager is opened lazily and cached on the host so a
// client can browse history before creating its first session. Callers must
// hold h.mu.
func (h *acpSessionHost) sessionStoreLocked() (*runtimechat.SessionManager, string, error) {
	if h == nil {
		return nil, "", fmt.Errorf("acp host is nil")
	}
	if h.storeMgr != nil {
		return h.storeMgr, h.storeUserID, nil
	}
	opts := h.opts
	if opts == nil || opts.ExecOptions == nil {
		return nil, "", fmt.Errorf("agent stdio options are nil")
	}
	runtimeConfig, runtimeConfigPath := loadChatPersistenceRuntimeConfig(h.cfg, nil)
	manager, userID, _, err := newChatSessionManagerWithRuntimeConfig(
		strings.TrimSpace(opts.SessionDir),
		runtimeConfig,
		runtimeConfigPath,
		opts.SessionUser,
	)
	if err != nil {
		return nil, "", fmt.Errorf("open session store: %w", err)
	}
	h.storeMgr = manager
	h.storeUserID = userID
	return manager, userID, nil
}

// ListSessions implements acp.SessionLister (session/list).
//
// Live in-memory sessions are merged with the durable store so a client that
// created a session in this process but has not prompted yet still sees it.
func (h *acpSessionHost) ListSessions(ctx context.Context, req acp.SessionListRequest) (acp.SessionListResponse, error) {
	if h == nil {
		return acp.SessionListResponse{}, fmt.Errorf("acp host is nil")
	}
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return acp.SessionListResponse{}, fmt.Errorf("acp host is closed")
	}
	manager, userID, err := h.sessionStoreLocked()
	live := make(map[string]*acpHostSession, len(h.sess))
	for id, s := range h.sess {
		live[id] = s
	}
	h.mu.Unlock()
	if err != nil {
		return acp.SessionListResponse{}, err
	}
	if ctx == nil {
		ctx = context.Background()
	}

	summaries := make([]acp.SessionSummary, 0, len(live))
	seen := make(map[string]struct{}, len(live))
	if manager != nil {
		sessions, listErr := manager.List(ctx, userID)
		if listErr != nil {
			return acp.SessionListResponse{}, fmt.Errorf("list sessions: %w", listErr)
		}
		for _, session := range sessions {
			if session == nil {
				continue
			}
			id := strings.TrimSpace(session.ID)
			if id == "" {
				continue
			}
			if !acpSessionMatchesCwd(session, req.Cwd) {
				continue
			}
			summaries = append(summaries, acpSessionSummaryForStore(session))
			seen[id] = struct{}{}
		}
	}
	// Merge live sessions the store listing did not return (e.g. a store that
	// was opened after the session was created, or a filtered-out row).
	for id, hostSess := range live {
		if _, ok := seen[id]; ok {
			continue
		}
		if hostSess == nil || hostSess.chat == nil {
			continue
		}
		if !acpLiveSessionMatchesCwd(hostSess.chat, req.Cwd) {
			continue
		}
		summaries = append(summaries, acpSessionSummaryForChat(id, hostSess.chat))
	}

	sort.SliceStable(summaries, func(i, j int) bool {
		return summaries[i].UpdatedAt > summaries[j].UpdatedAt
	})

	start, err := parseACPSessionCursor(req.Cursor)
	if err != nil {
		return acp.SessionListResponse{}, acp.InvalidParams(err)
	}
	if start > len(summaries) {
		start = len(summaries)
	}
	end := start + acpSessionListPageSize
	if end > len(summaries) {
		end = len(summaries)
	}
	resp := acp.SessionListResponse{Sessions: summaries[start:end]}
	if end < len(summaries) {
		resp.NextCursor = strconv.Itoa(end)
	}
	return resp, nil
}

// DeleteSession implements acp.SessionDeleter (session/delete): the live
// session is detached first, then the durable record is removed.
func (h *acpSessionHost) DeleteSession(ctx context.Context, req acp.SessionDeleteRequest) error {
	sessionID := strings.TrimSpace(req.SessionID)
	if sessionID == "" {
		return acp.InvalidParams(fmt.Errorf("sessionId is required"))
	}
	if h == nil {
		return fmt.Errorf("acp host is nil")
	}
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return fmt.Errorf("acp host is closed")
	}
	live := h.sess[sessionID]
	if live != nil {
		h.closeSessionLocked(live)
		delete(h.sess, sessionID)
	}
	manager, _, err := h.sessionStoreLocked()
	h.mu.Unlock()
	if err != nil {
		return err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if manager != nil {
		if _, getErr := manager.Get(ctx, sessionID); getErr == nil {
			if delErr := manager.Delete(ctx, sessionID); delErr != nil {
				return fmt.Errorf("delete session %q: %w", sessionID, delErr)
			}
			return nil
		}
	}
	if live != nil {
		// A live-only session (never persisted) is fully removed by detaching.
		return nil
	}
	return fmt.Errorf("session %q not found", sessionID)
}

// CloseSession implements acp.SessionCloser (session/close). Closing a session
// that is not attached to this process is a no-op: close is idempotent and
// must not fail a client that is shutting down its own bookkeeping.
func (h *acpSessionHost) CloseSession(ctx context.Context, req acp.SessionCloseRequest) error {
	_ = ctx
	sessionID := strings.TrimSpace(req.SessionID)
	if sessionID == "" {
		return acp.InvalidParams(fmt.Errorf("sessionId is required"))
	}
	if h == nil {
		return nil
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return nil
	}
	if live := h.sess[sessionID]; live != nil {
		h.closeSessionLocked(live)
		delete(h.sess, sessionID)
	}
	return nil
}

func parseACPSessionCursor(cursor string) (int, error) {
	trimmed := strings.TrimSpace(cursor)
	if trimmed == "" {
		return 0, nil
	}
	offset, err := strconv.Atoi(trimmed)
	if err != nil || offset < 0 {
		return 0, fmt.Errorf("invalid cursor %q", cursor)
	}
	return offset, nil
}

// acpResolveSessionWorkspace picks the workspace recorded for a session:
// the client-provided cwd when present, otherwise the process working
// directory (the ACP client launches the agent inside the workspace, so this
// keeps session/list?cwd=<workspace> meaningful for sessions created without
// an explicit cwd).
func acpResolveSessionWorkspace(cwd string) string {
	if trimmed := strings.TrimSpace(cwd); trimmed != "" {
		return trimmed
	}
	if wd, err := os.Getwd(); err == nil {
		return strings.TrimSpace(wd)
	}
	return ""
}

// recordACPSessionWorkspace stores the workspace in the session metadata so a
// later session/list can narrow by cwd. Failures are ignored: recording is a
// convenience and must never fail session creation.
func recordACPSessionWorkspace(ctx context.Context, hostSess *acpHostSession, workspace string) {
	if hostSess == nil || strings.TrimSpace(workspace) == "" {
		return
	}
	chat := hostSess.chat
	if chat == nil {
		return
	}
	if chat.RuntimeSession != nil {
		if chat.RuntimeSession.Metadata.Context == nil {
			chat.RuntimeSession.Metadata.Context = map[string]interface{}{}
		}
		chat.RuntimeSession.Metadata.Context["cwd"] = workspace
	}
	if chat.SessionManager == nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	_ = chat.SessionManager.UpdateContext(ctx, hostSess.id, "cwd", workspace)
}

// acpSessionCwd extracts the workspace recorded for a durable session.
// Older sessions may not carry one; an empty result means "unknown".
func acpSessionCwd(session *runtimechat.Session) string {
	if session == nil {
		return ""
	}
	for _, key := range []string{"cwd", "workspace", "working_dir", "workingDirectory"} {
		if value, ok := session.Metadata.Context[key]; ok {
			if text, ok := value.(string); ok && strings.TrimSpace(text) != "" {
				return strings.TrimSpace(text)
			}
		}
	}
	return ""
}

func acpSessionMatchesCwd(session *runtimechat.Session, cwd string) bool {
	want := strings.TrimSpace(cwd)
	if want == "" {
		return true
	}
	have := acpSessionCwd(session)
	if have == "" {
		// Unknown workspace: do not hide the session from an unfiltered
		// history panel just because it predates cwd recording.
		return true
	}
	return acpSamePath(have, want)
}

func acpLiveSessionMatchesCwd(chat *ChatSession, cwd string) bool {
	want := strings.TrimSpace(cwd)
	if want == "" || chat == nil {
		return true
	}
	// Only an explicit runtime session workspace is authoritative; sessions
	// that never recorded one stay visible (unknown workspace).
	if chat.RuntimeSession != nil {
		if workspace := acpSessionCwd(chat.RuntimeSession); workspace != "" {
			return acpSamePath(workspace, want)
		}
	}
	return true
}

func acpSamePath(a, b string) bool {
	normalize := func(value string) string {
		value = strings.TrimSpace(value)
		value = strings.ReplaceAll(value, "\\", "/")
		value = strings.TrimRight(value, "/")
		return strings.ToLower(value)
	}
	return normalize(a) == normalize(b)
}

func acpSessionSummaryForStore(session *runtimechat.Session) acp.SessionSummary {
	summary := acp.SessionSummary{
		SessionID: strings.TrimSpace(session.ID),
		Cwd:       acpSessionCwd(session),
		Title:     strings.TrimSpace(session.Metadata.Title),
	}
	if !session.UpdatedAt.IsZero() {
		summary.UpdatedAt = session.UpdatedAt.UTC().Format(time.RFC3339)
	}
	return summary
}

func acpSessionSummaryForChat(sessionID string, chat *ChatSession) acp.SessionSummary {
	summary := acp.SessionSummary{SessionID: strings.TrimSpace(sessionID)}
	if chat == nil {
		return summary
	}
	if chat.RuntimeSession != nil {
		return acpSessionSummaryForStore(chat.RuntimeSession)
	}
	return summary
}

// acpAvailableCommands is the static slash-command catalog advertised through
// available_commands_update. Only commands a headless ACP client can actually
// trigger are listed.
func acpAvailableCommands() []acp.AvailableCommand {
	return []acp.AvailableCommand{
		{Name: "help", Description: "显示可用命令与帮助信息"},
		{Name: "status", Description: "显示当前会话、模型与 token 状态"},
		{Name: "clear", Description: "清空当前会话上下文"},
		{Name: "compact", Description: "压缩当前会话上下文", Input: &acp.AvailableCommandInput{Hint: "[保留轮数]"}},
		{Name: "model", Description: "切换模型", Input: &acp.AvailableCommandInput{Hint: "<model-id>"}},
		{Name: "mode", Description: "切换权限模式（default/accept_edits/plan/bypass_permissions）", Input: &acp.AvailableCommandInput{Hint: "<mode>"}},
	}
}

// emitACPSessionCatalog pushes the command catalog plus a first session_info
// snapshot. Failures are ignored: the catalog is advisory and must never fail
// session creation.
func emitACPSessionCatalog(emit acp.Emitter, sessionID string, chat *ChatSession) {
	if emit == nil {
		return
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return
	}
	_ = emit.SessionUpdate(sessionID, acp.AvailableCommandsUpdate(acpAvailableCommands()))
	emitACPSessionInfo(emit, sessionID, chat)
}

// emitACPSessionInfo reports the current session title/updatedAt so the client
// history panel stays in sync after auto-title generation.
func emitACPSessionInfo(emit acp.Emitter, sessionID string, chat *ChatSession) {
	if emit == nil {
		return
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return
	}
	title := ""
	updatedAt := time.Now().UTC().Format(time.RFC3339)
	if chat != nil && chat.RuntimeSession != nil {
		if trimmed := strings.TrimSpace(chat.RuntimeSession.Metadata.Title); trimmed != "" {
			title = trimmed
		}
		if !chat.RuntimeSession.UpdatedAt.IsZero() {
			updatedAt = chat.RuntimeSession.UpdatedAt.UTC().Format(time.RFC3339)
		}
	}
	_ = emit.SessionUpdate(sessionID, acp.SessionInfoUpdate(title, updatedAt))
}

// emitACPSessionUsage reports context-window consumption after a turn.
// The update is skipped when the window size is unknown: the spec types size as
// required, and a bogus 0 would make clients render a broken meter.
func emitACPSessionUsage(emit acp.Emitter, sessionID string, chat *ChatSession) {
	if emit == nil || chat == nil {
		return
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return
	}
	size := chat.ContextWindowTokenCount
	if size <= 0 {
		return
	}
	used := chat.ContextTokenCount
	if used < 0 {
		used = 0
	}
	_ = emit.SessionUpdate(sessionID, acp.UsageUpdate(int64(used), int64(size)))
}
