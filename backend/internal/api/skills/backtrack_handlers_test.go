package skills

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/mux"
	"github.com/stretchr/testify/require"
	runtimeagent "github.com/wwsheng009/ai-agent-runtime/internal/agent"
	"github.com/wwsheng009/ai-agent-runtime/internal/chat"
	"github.com/wwsheng009/ai-agent-runtime/internal/skill"
	runtimetypes "github.com/wwsheng009/ai-agent-runtime/internal/types"
)

func newBacktrackHandlerFixture(t *testing.T, history []runtimetypes.Message) (*Handler, *mux.Router, *chat.Session, chat.SessionStorage, *chat.SessionActor, *chat.InMemoryRuntimeStore) {
	t.Helper()
	ctx := context.Background()
	storage := chat.NewInMemoryStorage()
	sessionManager := chat.NewSessionManager(storage, nil)
	session, err := sessionManager.Create(ctx, "backtrack-api-user")
	require.NoError(t, err)
	for _, msg := range history {
		session.AddMessage(msg)
	}
	require.NoError(t, storage.Update(ctx, session))

	apiAgent := runtimeagent.NewAgent(&runtimeagent.Config{
		Name:  "backtrack-api-test",
		Model: "test-model",
	}, nil)

	runtimeStore := chat.NewInMemoryRuntimeStore(64)
	actor, err := chat.NewSessionActor(session.ID, chat.SessionActorConfig{
		Agent:        apiAgent,
		SessionStore: storage,
		StateStore:   runtimeStore,
		EventStore:   runtimeStore,
	})
	require.NoError(t, err)
	t.Cleanup(func() { actor.Stop() })

	handler := NewHandler(skill.NewRegistry(nil), nil, nil)
	handler.SetSessionManager(sessionManager)
	handler.sessionHub = chat.NewSessionHub(func(sessionID string) (*chat.SessionActor, error) {
		require.Equal(t, session.ID, sessionID)
		return actor, nil
	})
	_, err = handler.sessionHub.GetOrCreate(session.ID)
	require.NoError(t, err)

	router := mux.NewRouter()
	handler.RegisterRoutes(router)
	return handler, router, session, storage, actor, runtimeStore
}

func TestListSessionTurns(t *testing.T) {
	_, router, session, _, _, _ := newBacktrackHandlerFixture(t, []runtimetypes.Message{
		*runtimetypes.NewUserMessage("first"),
		*runtimetypes.NewAssistantMessage("a1"),
		*runtimetypes.NewUserMessage("second"),
	})

	req := httptest.NewRequest(http.MethodGet, "/api/runtime/sessions/"+session.ID+"/turns", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	var resp struct {
		SessionID string `json:"session_id"`
		Count     int    `json:"count"`
		Turns     []struct {
			Index        int    `json:"index"`
			MessageIndex int    `json:"message_index"`
			Preview      string `json:"preview"`
		} `json:"turns"`
	}
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
	require.Equal(t, session.ID, resp.SessionID)
	require.Equal(t, 2, resp.Count)
	require.Len(t, resp.Turns, 2)
	require.Equal(t, "first", resp.Turns[0].Preview)
	require.Equal(t, "second", resp.Turns[1].Preview)
	require.Equal(t, 0, resp.Turns[0].MessageIndex)
	require.Equal(t, 2, resp.Turns[1].MessageIndex)
}

func TestPreviewSessionBacktrackDoesNotMutate(t *testing.T) {
	ctx := context.Background()
	_, router, session, storage, _, _ := newBacktrackHandlerFixture(t, []runtimetypes.Message{
		*runtimetypes.NewUserMessage("first"),
		*runtimetypes.NewAssistantMessage("a1"),
		*runtimetypes.NewUserMessage("second"),
		*runtimetypes.NewAssistantMessage("a2"),
	})

	body := `{"user_turn_index":1,"mode":"conversation"}`
	req := httptest.NewRequest(http.MethodPost, "/api/runtime/sessions/"+session.ID+"/backtrack/preview", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	var resp struct {
		OK     bool `json:"ok"`
		Result struct {
			PreviewOnly             bool   `json:"preview_only"`
			TruncatedToMessageCount int    `json:"truncated_to_message_count"`
			RemovedMessageCount     int    `json:"removed_message_count"`
			ComposerPrompt          string `json:"composer_prompt"`
		} `json:"result"`
	}
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
	require.True(t, resp.OK)
	require.True(t, resp.Result.PreviewOnly)
	require.Equal(t, 2, resp.Result.TruncatedToMessageCount)
	require.Equal(t, 2, resp.Result.RemovedMessageCount)
	require.Equal(t, "second", resp.Result.ComposerPrompt)

	updated, err := storage.Load(ctx, session.ID)
	require.NoError(t, err)
	require.Len(t, updated.GetMessages(), 4)
}

func TestApplySessionBacktrackTruncatesHistory(t *testing.T) {
	ctx := context.Background()
	_, router, session, storage, _, _ := newBacktrackHandlerFixture(t, []runtimetypes.Message{
		*runtimetypes.NewUserMessage("first"),
		*runtimetypes.NewAssistantMessage("a1"),
		*runtimetypes.NewUserMessage("second"),
		*runtimetypes.NewAssistantMessage("a2"),
		*runtimetypes.NewUserMessage("third"),
		*runtimetypes.NewAssistantMessage("a3"),
	})

	body := `{"user_turn_index":1,"mode":"conversation"}`
	req := httptest.NewRequest(http.MethodPost, "/api/runtime/sessions/"+session.ID+"/backtrack", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	var resp struct {
		OK     bool `json:"ok"`
		Result struct {
			PreviewOnly             bool     `json:"preview_only"`
			TruncatedToMessageCount int      `json:"truncated_to_message_count"`
			RemovedMessageCount     int      `json:"removed_message_count"`
			RemovedUserTurns        int      `json:"removed_user_turns"`
			ComposerPrompt          string   `json:"composer_prompt"`
			EventsEmitted           []string `json:"events_emitted"`
		} `json:"result"`
	}
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
	require.True(t, resp.OK)
	require.False(t, resp.Result.PreviewOnly)
	require.Equal(t, 2, resp.Result.TruncatedToMessageCount)
	require.Equal(t, 4, resp.Result.RemovedMessageCount)
	require.Equal(t, 2, resp.Result.RemovedUserTurns)
	require.Equal(t, "second", resp.Result.ComposerPrompt)
	require.Contains(t, resp.Result.EventsEmitted, chat.EventBacktrackStarted)
	require.Contains(t, resp.Result.EventsEmitted, chat.EventBacktrackFinished)

	updated, err := storage.Load(ctx, session.ID)
	require.NoError(t, err)
	messages := updated.GetMessages()
	require.Len(t, messages, 2)
	require.Equal(t, "first", messages[0].Content)
	require.Equal(t, "a1", messages[1].Content)
}

func TestListSessionBacktrackAuditAfterApply(t *testing.T) {
	ctx := context.Background()
	_, router, session, storage, _, _ := newBacktrackHandlerFixture(t, []runtimetypes.Message{
		*runtimetypes.NewUserMessage("first"),
		*runtimetypes.NewAssistantMessage("a1"),
		*runtimetypes.NewUserMessage("second"),
		*runtimetypes.NewAssistantMessage("a2"),
	})

	// Empty audit before any backtrack.
	req := httptest.NewRequest(http.MethodGet, "/api/runtime/sessions/"+session.ID+"/backtrack/audit", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
	var empty struct {
		SessionID string `json:"session_id"`
		Count     int    `json:"count"`
		Entries   []any  `json:"entries"`
	}
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&empty))
	require.Equal(t, session.ID, empty.SessionID)
	require.Equal(t, 0, empty.Count)
	require.NotNil(t, empty.Entries)
	require.Len(t, empty.Entries, 0)

	body := `{"user_turn_index":1,"mode":"conversation"}`
	applyReq := httptest.NewRequest(http.MethodPost, "/api/runtime/sessions/"+session.ID+"/backtrack", strings.NewReader(body))
	applyReq.Header.Set("Content-Type", "application/json")
	applyRec := httptest.NewRecorder()
	router.ServeHTTP(applyRec, applyReq)
	require.Equal(t, http.StatusOK, applyRec.Code)

	auditReq := httptest.NewRequest(http.MethodGet, "/api/runtime/sessions/"+session.ID+"/backtrack/audit", nil)
	auditRec := httptest.NewRecorder()
	router.ServeHTTP(auditRec, auditReq)
	require.Equal(t, http.StatusOK, auditRec.Code, "body=%s", auditRec.Body.String())

	var audit struct {
		SessionID string `json:"session_id"`
		Count     int    `json:"count"`
		Entries   []struct {
			ID                  string `json:"id"`
			UserTurnIndex       int    `json:"user_turn_index"`
			RemovedMessageCount int    `json:"removed_message_count"`
			AnchorPreview       string `json:"anchor_preview"`
			Mode                string `json:"mode"`
		} `json:"entries"`
	}
	require.NoError(t, json.NewDecoder(auditRec.Body).Decode(&audit))
	require.Equal(t, session.ID, audit.SessionID)
	require.Equal(t, 1, audit.Count)
	require.Len(t, audit.Entries, 1)
	require.NotEmpty(t, audit.Entries[0].ID)
	require.Equal(t, 1, audit.Entries[0].UserTurnIndex)
	require.Equal(t, 2, audit.Entries[0].RemovedMessageCount)
	require.Equal(t, "second", audit.Entries[0].AnchorPreview)
	require.Equal(t, "conversation", audit.Entries[0].Mode)

	// History remains truncated; audit is independent of full bodies.
	updated, err := storage.Load(ctx, session.ID)
	require.NoError(t, err)
	require.Len(t, updated.GetMessages(), 2)
	require.Len(t, chat.ListBacktrackTombstones(updated), 1)
}

func TestApplySessionBacktrackRejectsMissingSelector(t *testing.T) {
	_, router, session, _, _, _ := newBacktrackHandlerFixture(t, []runtimetypes.Message{
		*runtimetypes.NewUserMessage("first"),
	})

	req := httptest.NewRequest(http.MethodPost, "/api/runtime/sessions/"+session.ID+"/backtrack", strings.NewReader(`{"mode":"conversation"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusBadRequest, rec.Code, "body=%s", rec.Body.String())
}

func TestApplySessionBacktrackRejectsBusySession(t *testing.T) {
	ctx := context.Background()
	storage := chat.NewInMemoryStorage()
	sessionManager := chat.NewSessionManager(storage, nil)
	session, err := sessionManager.Create(ctx, "backtrack-busy-user")
	require.NoError(t, err)
	session.AddMessage(*runtimetypes.NewUserMessage("first"))
	session.AddMessage(*runtimetypes.NewAssistantMessage("a1"))
	session.AddMessage(*runtimetypes.NewUserMessage("second"))
	require.NoError(t, storage.Update(ctx, session))

	apiAgent := runtimeagent.NewAgent(&runtimeagent.Config{
		Name:  "backtrack-busy-test",
		Model: "test-model",
	}, nil)
	runtimeStore := chat.NewInMemoryRuntimeStore(64)
	// Seed busy state before actor construction so loadState picks it up.
	require.NoError(t, runtimeStore.SaveState(ctx, &chat.RuntimeState{
		SessionID:     session.ID,
		Status:        chat.SessionRunning,
		CurrentTurnID: "busy",
		UpdatedAt:     time.Now().UTC(),
	}))

	actor, err := chat.NewSessionActor(session.ID, chat.SessionActorConfig{
		Agent:        apiAgent,
		SessionStore: storage,
		StateStore:   runtimeStore,
		EventStore:   runtimeStore,
	})
	require.NoError(t, err)
	t.Cleanup(func() { actor.Stop() })

	handler := NewHandler(skill.NewRegistry(nil), nil, nil)
	handler.SetSessionManager(sessionManager)
	handler.sessionHub = chat.NewSessionHub(func(sessionID string) (*chat.SessionActor, error) {
		require.Equal(t, session.ID, sessionID)
		return actor, nil
	})
	_, err = handler.sessionHub.GetOrCreate(session.ID)
	require.NoError(t, err)

	router := mux.NewRouter()
	handler.RegisterRoutes(router)

	body := `{"user_turn_index":1}`
	req := httptest.NewRequest(http.MethodPost, "/api/runtime/sessions/"+session.ID+"/backtrack", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	// Busy session is a conflict with in-flight work, not a malformed request.
	require.Equal(t, http.StatusConflict, rec.Code, "body=%s", rec.Body.String())
}
