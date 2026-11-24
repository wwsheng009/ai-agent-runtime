package runtimeapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wwsheng009/ai-agent-runtime/internal/memorystore"
)

func TestMemoryG4EpisodeDreamPatchApplyAndRollbackAPI(t *testing.T) {
	workspace := t.TempDir()
	_, router := newMemoryTestRouter(t, workspace)
	seed, err := memorystore.New(memorystore.Config{ProjectRoot: workspace, Mode: "on"})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, seed.Close()) })

	var episodes []memorystore.Episode
	for _, taskID := range []string{"api-task-a", "api-task-b"} {
		episode, createErr := seed.CreateEpisode(t.Context(), memorystore.EpisodeInput{
			TaskID: taskID, SessionID: "api-dream-session",
			Problem: "API episode problem " + taskID, Result: "API episode result",
			Outcome:        memorystore.EpisodeOutcomeSuccess,
			SourceEventIDs: []string{"api-event-" + taskID}, EndedAt: time.Now().UTC(),
		})
		require.NoError(t, createErr)
		episodes = append(episodes, episode)
	}

	listReq := httptest.NewRequest(http.MethodGet, "/api/runtime/memory/episodes", nil)
	listRec := httptest.NewRecorder()
	router.ServeHTTP(listRec, listReq)
	require.Equal(t, http.StatusOK, listRec.Code, listRec.Body.String())
	var episodeList struct {
		Items []memorystore.Episode `json:"items"`
		Count int                   `json:"count"`
	}
	require.NoError(t, json.NewDecoder(listRec.Body).Decode(&episodeList))
	require.Equal(t, 2, episodeList.Count)

	getReq := httptest.NewRequest(http.MethodGet, "/api/runtime/memory/episodes/"+episodes[0].ID, nil)
	getRec := httptest.NewRecorder()
	router.ServeHTTP(getRec, getReq)
	require.Equal(t, http.StatusOK, getRec.Code, getRec.Body.String())

	promoteReq := httptest.NewRequest(http.MethodPost, "/api/runtime/memory/episodes/"+episodes[0].ID+"/promote", nil)
	promoteReq.Header.Set("X-Skills-Admin-Token", "memory-admin")
	promoteRec := httptest.NewRecorder()
	router.ServeHTTP(promoteRec, promoteReq)
	require.Equal(t, http.StatusOK, promoteRec.Code, promoteRec.Body.String())

	dreamReq := httptest.NewRequest(http.MethodPost, "/api/runtime/memory/dreams/run",
		strings.NewReader(`{"limit":10}`))
	dreamReq.Header.Set("Content-Type", "application/json")
	dreamReq.Header.Set("X-Skills-Admin-Token", "memory-admin")
	dreamRec := httptest.NewRecorder()
	router.ServeHTTP(dreamRec, dreamReq)
	require.Equal(t, http.StatusOK, dreamRec.Code, dreamRec.Body.String())
	var run memorystore.DreamRun
	require.NoError(t, json.NewDecoder(dreamRec.Body).Decode(&run))
	require.Equal(t, memorystore.DreamRunSucceeded, run.Status)
	require.Equal(t, 1, run.PatchCount)

	patchListReq := httptest.NewRequest(http.MethodGet, "/api/runtime/memory/patches?status=proposed", nil)
	patchListReq.Header.Set("X-Skills-Admin-Token", "memory-admin")
	patchListRec := httptest.NewRecorder()
	router.ServeHTTP(patchListRec, patchListReq)
	require.Equal(t, http.StatusOK, patchListRec.Code, patchListRec.Body.String())
	var patchList struct {
		Items []memorystore.MemoryPatch `json:"items"`
		Count int                       `json:"count"`
	}
	require.NoError(t, json.NewDecoder(patchListRec.Body).Decode(&patchList))
	require.Equal(t, 1, patchList.Count)
	patch := patchList.Items[0]

	approveReq := httptest.NewRequest(http.MethodPost, "/api/runtime/memory/patches/"+patch.ID+"/approve",
		strings.NewReader(`{"note":"reviewed in API test"}`))
	approveReq.Header.Set("Content-Type", "application/json")
	approveReq.Header.Set("X-Skills-Admin-Token", "memory-admin")
	approveRec := httptest.NewRecorder()
	router.ServeHTTP(approveRec, approveReq)
	require.Equal(t, http.StatusOK, approveRec.Code, approveRec.Body.String())

	applyReq := httptest.NewRequest(http.MethodPost, "/api/runtime/memory/patches/"+patch.ID+"/apply", nil)
	applyReq.Header.Set("X-Skills-Admin-Token", "memory-admin")
	applyRec := httptest.NewRecorder()
	router.ServeHTTP(applyRec, applyReq)
	require.Equal(t, http.StatusOK, applyRec.Code, applyRec.Body.String())
	var applied memorystore.MemoryPatch
	require.NoError(t, json.NewDecoder(applyRec.Body).Decode(&applied))
	require.Equal(t, memorystore.PatchStatusApplied, applied.Status)
	require.Len(t, applied.AppliedRecordIDs, 1)

	recordReq := httptest.NewRequest(http.MethodGet, "/api/runtime/memory/records/"+applied.AppliedRecordIDs[0], nil)
	recordRec := httptest.NewRecorder()
	router.ServeHTTP(recordRec, recordReq)
	require.Equal(t, http.StatusOK, recordRec.Code, recordRec.Body.String())

	rollbackReq := httptest.NewRequest(http.MethodPost, "/api/runtime/memory/patches/"+patch.ID+"/rollback",
		strings.NewReader(`{"reason":"API rollback"}`))
	rollbackReq.Header.Set("Content-Type", "application/json")
	rollbackReq.Header.Set("X-Skills-Admin-Token", "memory-admin")
	rollbackRec := httptest.NewRecorder()
	router.ServeHTTP(rollbackRec, rollbackReq)
	require.Equal(t, http.StatusOK, rollbackRec.Code, rollbackRec.Body.String())
	var rolledBack memorystore.MemoryPatch
	require.NoError(t, json.NewDecoder(rollbackRec.Body).Decode(&rolledBack))
	require.Equal(t, memorystore.PatchStatusRolledBack, rolledBack.Status)

	dreamsReq := httptest.NewRequest(http.MethodGet, "/api/runtime/memory/dreams", nil)
	dreamsReq.Header.Set("X-Skills-Admin-Token", "memory-admin")
	dreamsRec := httptest.NewRecorder()
	router.ServeHTTP(dreamsRec, dreamsReq)
	require.Equal(t, http.StatusOK, dreamsRec.Code, dreamsRec.Body.String())
}
