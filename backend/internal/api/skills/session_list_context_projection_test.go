package skills

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wwsheng009/ai-agent-runtime/internal/chat"
	"github.com/wwsheng009/ai-agent-runtime/internal/sessionmeta"
)

// seedListRowInternalContext writes the internal bookkeeping keys a real session
// accumulates (frozen prompt, environment snapshot, tool aliases) plus the keys
// the list UI actually consumes.
func seedListRowInternalContext(t *testing.T, handler *Handler, sessionID string) {
	t.Helper()
	ctx := context.Background()
	internal := map[string]interface{}{
		sessionmeta.SystemPromptFrozen:            strings.Repeat("system-prompt-", 4096),
		sessionmeta.EnvironmentCapabilityGuidance: strings.Repeat("guidance-", 128),
		sessionmeta.EnvironmentContextBlock:       "<environment_context>…</environment_context>",
		"tool_handle_aliases":                     map[string]interface{}{"shell": "tool_1"},
		"compact_generation":                      2,
	}
	for key, value := range internal {
		require.NoError(t, handler.sessionManager.SetContext(ctx, sessionID, key, value))
	}
	for key, value := range map[string]interface{}{
		sessionmeta.ReasoningEffort: "high",
		"fork_parent_session_id":    "session-parent",
		"fork_origin_title":         "父会话",
	} {
		require.NoError(t, handler.sessionManager.SetContext(ctx, sessionID, key, value))
	}
}

func listSessionsViaAPI(t *testing.T, router http.Handler) []map[string]interface{} {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/sessions", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
	var payload struct {
		Sessions []map[string]interface{} `json:"sessions"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &payload))
	return payload.Sessions
}

func requireTrimmedListContext(t *testing.T, row map[string]interface{}, dir string) {
	t.Helper()
	metadata, ok := row["metadata"].(map[string]interface{})
	require.True(t, ok)
	contextMap, ok := metadata["context"].(map[string]interface{})
	require.True(t, ok)

	// Keys the list UI consumes must survive the projection.
	require.Equal(t, filepath.Clean(dir), contextMap[sessionmeta.WorkspacePath])
	require.Equal(t, "high", contextMap[sessionmeta.ReasoningEffort])
	require.Equal(t, "session-parent", contextMap["fork_parent_session_id"])
	require.Equal(t, "父会话", contextMap["fork_origin_title"])

	// Internal bookkeeping must not be serialized into list rows.
	for _, dropped := range []string{
		sessionmeta.SystemPromptFrozen,
		sessionmeta.EnvironmentCapabilityGuidance,
		sessionmeta.EnvironmentContextBlock,
		"tool_handle_aliases",
		"compact_generation",
	} {
		_, present := contextMap[dropped]
		require.Falsef(t, present, "internal context key %q must not leak into the session list payload", dropped)
	}
}

func TestListSessionsTrimsInternalContextWithoutMutatingLiveSession(t *testing.T) {
	handler, router, _ := newSessionBindingTestHandler(t)
	dir := t.TempDir()

	code, payload := createSessionViaAPI(t, router, map[string]string{
		"title":          "fat-row",
		"workspace_path": dir,
	})
	require.Equal(t, http.StatusCreated, code)
	sessionPayload, ok := payload["session"].(map[string]interface{})
	require.True(t, ok)
	sessionID, ok := sessionPayload["id"].(string)
	require.True(t, ok)

	seedListRowInternalContext(t, handler, sessionID)

	rows := listSessionsViaAPI(t, router)
	require.Len(t, rows, 1)
	requireTrimmedListContext(t, rows[0], dir)

	// The projection must be a copy: the live session keeps its full context so
	// prompt-cache anchors and runtime bookkeeping stay intact.
	live, err := handler.sessionManager.GetSession(context.Background(), sessionID)
	require.NoError(t, err)
	require.NotEmpty(t, live.Metadata.Context[sessionmeta.SystemPromptFrozen])
	require.Equal(t, "<environment_context>…</environment_context>", live.Metadata.Context[sessionmeta.EnvironmentContextBlock])
	require.Equal(t, map[string]interface{}{"shell": "tool_1"}, live.Metadata.Context["tool_handle_aliases"])
}

func TestSearchSessionsTrimsInternalContext(t *testing.T) {
	handler, router, _ := newSessionBindingTestHandler(t)
	router.HandleFunc("/sessions/search", handler.SearchSessions).Methods(http.MethodPost)
	dir := t.TempDir()

	code, payload := createSessionViaAPI(t, router, map[string]string{
		"title":          "search-row",
		"workspace_path": dir,
	})
	require.Equal(t, http.StatusCreated, code)
	sessionPayload, ok := payload["session"].(map[string]interface{})
	require.True(t, ok)
	sessionID, ok := sessionPayload["id"].(string)
	require.True(t, ok)

	seedListRowInternalContext(t, handler, sessionID)

	req := httptest.NewRequest(http.MethodPost, "/sessions/search", bytes.NewReader([]byte(`{}`)))
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
	require.NotContains(t, rec.Body.String(), "system-prompt-")

	var searchPayload struct {
		Sessions []map[string]interface{} `json:"sessions"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &searchPayload))
	require.Len(t, searchPayload.Sessions, 1)
	requireTrimmedListContext(t, searchPayload.Sessions[0], dir)
}

func TestProjectSessionsForListIsNonDestructive(t *testing.T) {
	require.Nil(t, projectSessionsForList(nil))

	bare := chat.NewSession("user-bare")
	require.Same(t, bare, projectSessionsForList([]*chat.Session{bare})[0],
		"sessions without context must be passed through unchanged")

	fat := chat.NewSession("user-fat")
	fat.SetContext(sessionmeta.SystemPromptFrozen, "frozen")
	fat.SetContext(sessionmeta.WorkspacePath, "D:/repo")
	fat.SetContext("fork_source_message_id", "msg_1")

	projected := projectSessionsForList([]*chat.Session{nil, fat})
	require.Len(t, projected, 2)
	require.Nil(t, projected[0])
	require.NotSame(t, fat, projected[1])
	require.Equal(t, map[string]interface{}{
		sessionmeta.WorkspacePath: "D:/repo",
		"fork_source_message_id":  "msg_1",
	}, projected[1].Metadata.Context)

	// Original map keeps every key.
	require.Len(t, fat.Metadata.Context, 3)
	require.Equal(t, "frozen", fat.Metadata.Context[sessionmeta.SystemPromptFrozen])
}
