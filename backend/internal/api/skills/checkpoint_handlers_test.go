package skills

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	runtimeagent "github.com/wwsheng009/ai-agent-runtime/internal/agent"
	"github.com/wwsheng009/ai-agent-runtime/internal/artifact"
	"github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimecheckpoint "github.com/wwsheng009/ai-agent-runtime/internal/checkpoint"
	"github.com/wwsheng009/ai-agent-runtime/internal/skill"
	runtimetypes "github.com/wwsheng009/ai-agent-runtime/internal/types"
	"github.com/gorilla/mux"
	"github.com/stretchr/testify/require"
)

func TestRestoreSessionCheckpointConversationRewritesSessionHistory(t *testing.T) {
	ctx := context.Background()
	storage := chat.NewInMemoryStorage()
	sessionManager := chat.NewSessionManager(storage, nil)
	session, err := sessionManager.Create(ctx, "user-1")
	require.NoError(t, err)
	session.AddMessage(*runtimetypes.NewUserMessage("first"))
	session.AddMessage(*runtimetypes.NewAssistantMessage("second"))
	session.AddMessage(*runtimetypes.NewUserMessage("third"))
	require.NoError(t, storage.Update(ctx, session))

	artifactStore, err := artifact.NewStore(nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = artifactStore.Close() })

	apiAgent := runtimeagent.NewAgent(&runtimeagent.Config{
		Name:  "checkpoint-handler-test",
		Model: "test-model",
	}, nil)
	checkpointMgr := runtimecheckpoint.NewManager(artifactStore, nil)
	apiAgent.SetCheckpointManager(checkpointMgr)

	pending := &runtimecheckpoint.PendingCheckpoint{
		SessionID:    session.ID,
		ToolName:     "execute_shell_command",
		ToolCallID:   "tool_1",
		MessageCount: 2,
		Conversation: []runtimetypes.Message{
			*runtimetypes.NewUserMessage("first"),
			*runtimetypes.NewAssistantMessage("second"),
		},
	}
	checkpointID, err := checkpointMgr.AfterMutation(ctx, pending, nil, "")
	require.NoError(t, err)

	runtimeStore := chat.NewInMemoryRuntimeStore(64)
	actor, err := chat.NewSessionActor(session.ID, chat.SessionActorConfig{
		Agent:        apiAgent,
		SessionStore: storage,
		StateStore:   runtimeStore,
		EventStore:   runtimeStore,
	})
	require.NoError(t, err)

	handler := NewHandler(skill.NewRegistry(nil), nil, nil)
	handler.SetSessionManager(sessionManager)
	handler.sessionHub = chat.NewSessionHub(func(sessionID string) (*chat.SessionActor, error) {
		require.Equal(t, session.ID, sessionID)
		return actor, nil
	})

	router := mux.NewRouter()
	handler.RegisterRoutes(router)

	req := httptest.NewRequest(http.MethodPost, "/api/skills/sessions/"+session.ID+"/checkpoints/"+checkpointID+"/restore", strings.NewReader(`{"mode":"conversation"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	var resp struct {
		OK     bool                            `json:"ok"`
		Result runtimecheckpoint.RestoreResult `json:"result"`
	}
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
	require.True(t, resp.OK)
	require.True(t, resp.Result.ConversationChanged)
	require.True(t, resp.Result.ConversationExact)
	require.Len(t, resp.Result.ConversationMessages, 2)

	updated, err := storage.Load(ctx, session.ID)
	require.NoError(t, err)
	require.NotNil(t, updated)
	messages := updated.GetMessages()
	require.Len(t, messages, 2)
	require.Equal(t, "first", messages[0].Content)
	require.Equal(t, "second", messages[1].Content)
	require.Zero(t, updated.HeadOffset)
}

func TestPreviewSessionCheckpointConversationReportsExactSnapshot(t *testing.T) {
	ctx := context.Background()
	storage := chat.NewInMemoryStorage()
	sessionManager := chat.NewSessionManager(storage, nil)
	session, err := sessionManager.Create(ctx, "user-1")
	require.NoError(t, err)

	artifactStore, err := artifact.NewStore(nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = artifactStore.Close() })

	apiAgent := runtimeagent.NewAgent(&runtimeagent.Config{
		Name:  "checkpoint-preview-handler-test",
		Model: "test-model",
	}, nil)
	checkpointMgr := runtimecheckpoint.NewManager(artifactStore, nil)
	apiAgent.SetCheckpointManager(checkpointMgr)

	pending := &runtimecheckpoint.PendingCheckpoint{
		SessionID:    session.ID,
		ToolName:     "execute_shell_command",
		ToolCallID:   "tool_1",
		MessageCount: 1,
		Conversation: []runtimetypes.Message{
			*runtimetypes.NewUserMessage("first"),
		},
	}
	checkpointID, err := checkpointMgr.AfterMutation(ctx, pending, nil, "")
	require.NoError(t, err)

	runtimeStore := chat.NewInMemoryRuntimeStore(64)
	actor, err := chat.NewSessionActor(session.ID, chat.SessionActorConfig{
		Agent:        apiAgent,
		SessionStore: storage,
		StateStore:   runtimeStore,
		EventStore:   runtimeStore,
	})
	require.NoError(t, err)

	handler := NewHandler(skill.NewRegistry(nil), nil, nil)
	handler.SetSessionManager(sessionManager)
	handler.sessionHub = chat.NewSessionHub(func(sessionID string) (*chat.SessionActor, error) {
		require.Equal(t, session.ID, sessionID)
		return actor, nil
	})

	router := mux.NewRouter()
	handler.RegisterRoutes(router)

	req := httptest.NewRequest(http.MethodPost, "/api/skills/sessions/"+session.ID+"/checkpoints/"+checkpointID+"/preview", strings.NewReader(`{"mode":"conversation"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	var resp struct {
		Result runtimecheckpoint.RestoreResult `json:"result"`
	}
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
	require.True(t, resp.Result.ConversationChanged)
	require.True(t, resp.Result.ConversationExact)
	require.Contains(t, resp.Result.Preview, "conversation: restore 1 message(s)")
}

func TestPreviewSessionCheckpointConversationIncludesProvenance(t *testing.T) {
	ctx := context.Background()
	storage := chat.NewInMemoryStorage()
	sessionManager := chat.NewSessionManager(storage, nil)
	session, err := sessionManager.Create(ctx, "user-1")
	require.NoError(t, err)

	artifactStore, err := artifact.NewStore(nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = artifactStore.Close() })

	apiAgent := runtimeagent.NewAgent(&runtimeagent.Config{
		Name:  "checkpoint-preview-provenance-test",
		Model: "test-model",
	}, nil)
	checkpointMgr := runtimecheckpoint.NewManager(artifactStore, nil)
	apiAgent.SetCheckpointManager(checkpointMgr)

	pending := &runtimecheckpoint.PendingCheckpoint{
		SessionID:    session.ID,
		ToolName:     "execute_shell_command",
		ToolCallID:   "tool_1",
		MessageCount: 1,
		Conversation: []runtimetypes.Message{
			*runtimetypes.NewUserMessage("first"),
		},
	}
	checkpointID, err := checkpointMgr.AfterMutation(ctx, pending, nil, "")
	require.NoError(t, err)

	stored, err := artifactStore.GetCheckpoint(ctx, checkpointID)
	require.NoError(t, err)
	require.NotNil(t, stored)
	stored.Metadata["source_refs"] = []string{
		"profile-resource:memory:E:/profiles/dev/agents/tester/memory/memory.json",
		"profile-resource:notes:E:/profiles/dev/agents/tester/context/notes.md",
	}
	require.NoError(t, artifactStore.UpdateCheckpoint(ctx, *stored))

	runtimeStore := chat.NewInMemoryRuntimeStore(64)
	actor, err := chat.NewSessionActor(session.ID, chat.SessionActorConfig{
		Agent:        apiAgent,
		SessionStore: storage,
		StateStore:   runtimeStore,
		EventStore:   runtimeStore,
	})
	require.NoError(t, err)

	handler := NewHandler(skill.NewRegistry(nil), nil, nil)
	handler.SetSessionManager(sessionManager)
	handler.sessionHub = chat.NewSessionHub(func(sessionID string) (*chat.SessionActor, error) {
		require.Equal(t, session.ID, sessionID)
		return actor, nil
	})

	router := mux.NewRouter()
	handler.RegisterRoutes(router)

	req := httptest.NewRequest(http.MethodPost, "/api/skills/sessions/"+session.ID+"/checkpoints/"+checkpointID+"/preview", strings.NewReader(`{"mode":"conversation"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	var resp struct {
		Result runtimecheckpoint.RestoreResult `json:"result"`
	}
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
	require.Len(t, resp.Result.Provenance.ProfileResourceRefs, 2)
	require.Equal(t, 2, resp.Result.Provenance.ProfileResourceCount)
	require.Equal(t, 1, resp.Result.Provenance.ProfileResourceKinds["memory"])
	require.Equal(t, 1, resp.Result.Provenance.ProfileResourceKinds["notes"])
	require.Contains(t, resp.Result.Provenance.ProfileResourceLabels, "memory:memory.json")
	require.Contains(t, resp.Result.Provenance.ProfileResourceLabels, "notes:notes.md")
	require.Contains(t, strings.Join(resp.Result.Preview, "\n"), "provenance: profile resources memory=1, notes=1")
}

func TestListSessionCheckpointsIncludesConversationExactSummary(t *testing.T) {
	ctx := context.Background()
	storage := chat.NewInMemoryStorage()
	sessionManager := chat.NewSessionManager(storage, nil)
	session, err := sessionManager.Create(ctx, "user-1")
	require.NoError(t, err)

	apiAgent := runtimeagent.NewAgent(&runtimeagent.Config{
		Name:  "checkpoint-list-handler-test",
		Model: "test-model",
	}, nil)
	artifactStore := apiAgent.GetArtifactStore()
	require.NotNil(t, artifactStore)
	checkpointMgr := runtimecheckpoint.NewManager(artifactStore, nil)
	apiAgent.SetCheckpointManager(checkpointMgr)

	pending := &runtimecheckpoint.PendingCheckpoint{
		SessionID:    session.ID,
		ToolName:     "execute_shell_command",
		ToolCallID:   "tool_1",
		MessageCount: 1,
		Conversation: []runtimetypes.Message{
			*runtimetypes.NewUserMessage("first"),
		},
	}
	_, err = checkpointMgr.AfterMutation(ctx, pending, nil, "")
	require.NoError(t, err)

	runtimeStore := chat.NewInMemoryRuntimeStore(64)
	actor, err := chat.NewSessionActor(session.ID, chat.SessionActorConfig{
		Agent:        apiAgent,
		SessionStore: storage,
		StateStore:   runtimeStore,
		EventStore:   runtimeStore,
	})
	require.NoError(t, err)

	handler := NewHandler(skill.NewRegistry(nil), nil, nil)
	handler.SetSessionManager(sessionManager)
	handler.sessionHub = chat.NewSessionHub(func(sessionID string) (*chat.SessionActor, error) {
		require.Equal(t, session.ID, sessionID)
		return actor, nil
	})

	router := mux.NewRouter()
	handler.RegisterRoutes(router)

	req := httptest.NewRequest(http.MethodGet, "/api/skills/sessions/"+session.ID+"/checkpoints", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	var resp struct {
		Checkpoints []struct {
			ID                       string `json:"id"`
			ConversationExact        bool   `json:"conversation_exact"`
			ConversationMessageCount int    `json:"conversation_message_count"`
		} `json:"checkpoints"`
	}
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
	require.Len(t, resp.Checkpoints, 1)
	require.True(t, resp.Checkpoints[0].ConversationExact)
	require.Equal(t, 1, resp.Checkpoints[0].ConversationMessageCount)
}

func TestListSessionCheckpointsIncludesProvenanceSummary(t *testing.T) {
	ctx := context.Background()
	storage := chat.NewInMemoryStorage()
	sessionManager := chat.NewSessionManager(storage, nil)
	session, err := sessionManager.Create(ctx, "user-1")
	require.NoError(t, err)

	apiAgent := runtimeagent.NewAgent(&runtimeagent.Config{
		Name:  "checkpoint-list-provenance-test",
		Model: "test-model",
	}, nil)
	artifactStore := apiAgent.GetArtifactStore()
	require.NotNil(t, artifactStore)
	checkpointMgr := runtimecheckpoint.NewManager(artifactStore, nil)
	apiAgent.SetCheckpointManager(checkpointMgr)

	pending := &runtimecheckpoint.PendingCheckpoint{
		SessionID:    session.ID,
		ToolName:     "execute_shell_command",
		ToolCallID:   "tool_1",
		MessageCount: 1,
		Conversation: []runtimetypes.Message{
			*runtimetypes.NewUserMessage("first"),
		},
	}
	checkpointID, err := checkpointMgr.AfterMutation(ctx, pending, nil, "")
	require.NoError(t, err)

	stored, err := artifactStore.GetCheckpoint(ctx, checkpointID)
	require.NoError(t, err)
	require.NotNil(t, stored)
	stored.Metadata["source_refs"] = []string{
		"profile-resource:memory:E:/profiles/dev/agents/tester/memory/memory.json",
	}
	require.NoError(t, artifactStore.UpdateCheckpoint(ctx, *stored))

	runtimeStore := chat.NewInMemoryRuntimeStore(64)
	actor, err := chat.NewSessionActor(session.ID, chat.SessionActorConfig{
		Agent:        apiAgent,
		SessionStore: storage,
		StateStore:   runtimeStore,
		EventStore:   runtimeStore,
	})
	require.NoError(t, err)

	handler := NewHandler(skill.NewRegistry(nil), nil, nil)
	handler.SetSessionManager(sessionManager)
	handler.sessionHub = chat.NewSessionHub(func(sessionID string) (*chat.SessionActor, error) {
		require.Equal(t, session.ID, sessionID)
		return actor, nil
	})

	router := mux.NewRouter()
	handler.RegisterRoutes(router)

	req := httptest.NewRequest(http.MethodGet, "/api/skills/sessions/"+session.ID+"/checkpoints", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	var resp struct {
		Checkpoints []struct {
			ID         string                              `json:"id"`
			Provenance runtimecheckpoint.ProvenanceSummary `json:"provenance"`
		} `json:"checkpoints"`
	}
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
	require.Len(t, resp.Checkpoints, 1)
	require.Equal(t, checkpointID, resp.Checkpoints[0].ID)
	require.Len(t, resp.Checkpoints[0].Provenance.ProfileResourceRefs, 1)
	require.Equal(t, 1, resp.Checkpoints[0].Provenance.ProfileResourceCount)
	require.Equal(t, 1, resp.Checkpoints[0].Provenance.ProfileResourceKinds["memory"])
	require.Contains(t, resp.Checkpoints[0].Provenance.ProfileResourceLabels, "memory:memory.json")
}

