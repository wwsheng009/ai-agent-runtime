package runtimeapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wwsheng009/ai-agent-runtime/internal/memorystore"
)

func TestMemoryG5EntityAndFeedbackAPI(t *testing.T) {
	workspace := t.TempDir()
	_, router := newMemoryTestRouter(t, workspace)
	seed, err := memorystore.New(memorystore.Config{ProjectRoot: workspace, Mode: "on"})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, seed.Close()) })
	note, err := seed.Append(memorystore.AppendNoteOptions{
		Text: "E_INVALID_ARGUMENT occurs in backend/internal/api/memory.go v1.2.3",
	})
	require.NoError(t, err)

	entitiesReq := httptest.NewRequest(http.MethodGet, "/api/runtime/memory/entities", nil)
	entitiesRec := httptest.NewRecorder()
	router.ServeHTTP(entitiesRec, entitiesReq)
	require.Equal(t, http.StatusOK, entitiesRec.Code, entitiesRec.Body.String())
	var entityList struct {
		Items []memorystore.MemoryEntity `json:"items"`
		Count int                        `json:"count"`
	}
	require.NoError(t, json.NewDecoder(entitiesRec.Body).Decode(&entityList))
	require.GreaterOrEqual(t, entityList.Count, 3)

	recordEntitiesReq := httptest.NewRequest(http.MethodGet,
		"/api/runtime/memory/records/"+note.ID+"/entities", nil)
	recordEntitiesRec := httptest.NewRecorder()
	router.ServeHTTP(recordEntitiesRec, recordEntitiesReq)
	require.Equal(t, http.StatusOK, recordEntitiesRec.Code, recordEntitiesRec.Body.String())

	feedbackReq := httptest.NewRequest(http.MethodPost,
		"/api/runtime/memory/records/"+note.ID+"/feedback",
		strings.NewReader(`{"kind":"helpful","comment":"used successfully","session_id":"g5-session"}`))
	feedbackReq.Header.Set("Content-Type", "application/json")
	feedbackRec := httptest.NewRecorder()
	router.ServeHTTP(feedbackRec, feedbackReq)
	require.Equal(t, http.StatusOK, feedbackRec.Code, feedbackRec.Body.String())
	var feedback memorystore.MemoryFeedback
	require.NoError(t, json.NewDecoder(feedbackRec.Body).Decode(&feedback))
	require.Equal(t, memorystore.FeedbackHelpful, feedback.Kind)

	listFeedbackReq := httptest.NewRequest(http.MethodGet,
		"/api/runtime/memory/records/"+note.ID+"/feedback", nil)
	listFeedbackReq.Header.Set("X-Skills-Admin-Token", "memory-admin")
	listFeedbackRec := httptest.NewRecorder()
	router.ServeHTTP(listFeedbackRec, listFeedbackReq)
	require.Equal(t, http.StatusOK, listFeedbackRec.Code, listFeedbackRec.Body.String())
	var feedbackList struct {
		Items []memorystore.MemoryFeedback `json:"items"`
		Count int                          `json:"count"`
	}
	require.NoError(t, json.NewDecoder(listFeedbackRec.Body).Decode(&feedbackList))
	require.Equal(t, 1, feedbackList.Count)
}
