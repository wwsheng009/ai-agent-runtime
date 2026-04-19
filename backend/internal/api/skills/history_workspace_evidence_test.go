package skills

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gorilla/mux"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wwsheng009/ai-agent-runtime/internal/chat"
	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

func TestPersistChatTurnExposesWorkspaceEvidenceInHistory(t *testing.T) {
	ctx := context.Background()
	sessionManager := chat.NewSessionManager(chat.NewInMemoryStorage(), nil)
	t.Cleanup(sessionManager.Stop)

	session, err := sessionManager.CreateSession(ctx, "history-user")
	require.NoError(t, err)

	handler := &Handler{sessionManager: sessionManager}
	metadata := types.NewMetadata()
	metadata["workspace_related_artifact_ids"] = []string{"persisted-agent-chat-response-agent-route"}
	metadata["workspace_related_artifacts"] = []map[string]interface{}{
		{
			"id":       "persisted-agent-chat-response-agent-route",
			"name":     "agent-chat-response-agent-route.json",
			"path":     "runtime/agent-chat-response-agent-route.json",
			"summary":  "Final response payload persisted with the assistant history.",
			"kind":     "json",
			"language": "json",
			"content": map[string]interface{}{
				"source": "agent_route",
				"kind":   "agent",
				"status": "completed",
			},
		},
	}

	require.NoError(t, handler.persistChatTurn(ctx, session, "hello", "world", metadata))

	req := httptest.NewRequest(http.MethodGet, "/api/runtime/sessions/"+session.ID+"/history", nil)
	req = mux.SetURLVars(req, map[string]string{"id": session.ID})
	rec := httptest.NewRecorder()

	handler.GetSessionHistory(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)

	var payload struct {
		SessionID string          `json:"session_id"`
		History   []types.Message `json:"history"`
		Count     int             `json:"count"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &payload))
	require.Equal(t, session.ID, payload.SessionID)
	require.Len(t, payload.History, 2)
	require.Equal(t, 2, payload.Count)

	assistant := payload.History[1]
	assert.Equal(t, "assistant", assistant.Role)
	assert.Equal(t, "world", assistant.Content)
	assert.Equal(
		t,
		[]interface{}{"persisted-agent-chat-response-agent-route"},
		assistant.Metadata["workspace_related_artifact_ids"],
	)

	rawArtifacts, ok := assistant.Metadata["workspace_related_artifacts"].([]interface{})
	require.True(t, ok)
	require.Len(t, rawArtifacts, 1)

	artifact, ok := rawArtifacts[0].(map[string]interface{})
	require.True(t, ok)
	assert.Equal(t, "agent-chat-response-agent-route.json", artifact["name"])
	assert.Equal(t, "runtime/agent-chat-response-agent-route.json", artifact["path"])
	assert.Equal(t, "json", artifact["kind"])
	assert.Equal(t, "json", artifact["language"])
	assert.Equal(t, "Final response payload persisted with the assistant history.", artifact["summary"])
}
