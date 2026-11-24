package runtimeapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/mux"
	"github.com/stretchr/testify/require"

	runtimecfg "github.com/wwsheng009/ai-agent-runtime/internal/config"
	"github.com/wwsheng009/ai-agent-runtime/internal/memoryruntime"
	"github.com/wwsheng009/ai-agent-runtime/internal/memorystore"
)

func newMemoryTestRouter(t *testing.T, workspace string) (*Handler, *mux.Router) {
	t.Helper()
	handler, router := newHarnessTestRouter(t)
	cfg := runtimecfg.DefaultRuntimeConfig()
	cfg.Workspace.Root = workspace
	cfg.Memory.Mode = "on"
	cfg.Memory.Injection.Enabled = true
	handler.SetRuntimeConfig(cfg, "")
	handler.SetAdminAuthRequired(true)
	handler.SetAdminToken("memory-admin")
	return handler, router
}

func TestMemoryAPIRecordsLifecycleAndStatus(t *testing.T) {
	workspace := t.TempDir()
	_, router := newMemoryTestRouter(t, workspace)

	statusReq := httptest.NewRequest(http.MethodGet, "/api/runtime/memory/status", nil)
	statusRec := httptest.NewRecorder()
	router.ServeHTTP(statusRec, statusReq)
	require.Equal(t, http.StatusOK, statusRec.Code, statusRec.Body.String())
	var status memoryStatusResponse
	require.NoError(t, json.NewDecoder(statusRec.Body).Decode(&status))
	require.Equal(t, "on", status.Mode)
	require.False(t, status.Fallback)
	require.True(t, status.GenerationEnabled)
	require.Equal(t, "both", status.GenerationTrigger)
	require.True(t, status.ConsolidationEnabled)
	require.True(t, status.ManualEnabled)
	require.True(t, status.TurnDigestEnabled)
	require.False(t, status.MemoryToolsEnabled)
	require.Equal(t, "tools_disabled", status.MemoryToolsReason)
	require.NotNil(t, status.TurnDigests)
	require.NotNil(t, status.TurnWindows)
	require.GreaterOrEqual(t, status.SchemaVersion, 6)
	require.False(t, status.MemoryRuntimePresent)
	require.Equal(t, "unavailable", status.MemoryRuntimeState)
	require.Equal(t, "runtime_not_initialized", status.MemoryRuntimeReason)
	require.Nil(t, status.DigestRuntime)

	body := `{"text":"prefer apply_patch for multi-hunk edits","kind":"workflow","tags":["workflow"],"session_id":"s1"}`
	createReq := httptest.NewRequest(http.MethodPost, "/api/runtime/memory/records", strings.NewReader(body))
	createReq.Header.Set("Content-Type", "application/json")
	createReq.Header.Set("X-Skills-Admin-Token", "memory-admin")
	createRec := httptest.NewRecorder()
	router.ServeHTTP(createRec, createReq)
	require.Equal(t, http.StatusOK, createRec.Code, createRec.Body.String())
	var created memoryRecordDTO
	require.NoError(t, json.NewDecoder(createRec.Body).Decode(&created))
	require.NotEmpty(t, created.ID)
	require.Equal(t, "workflow", created.Kind)
	require.True(t, created.Pinned)
	require.NotEmpty(t, created.ValidFrom)

	listReq := httptest.NewRequest(http.MethodGet, "/api/runtime/memory/records?status=active", nil)
	listRec := httptest.NewRecorder()
	router.ServeHTTP(listRec, listReq)
	require.Equal(t, http.StatusOK, listRec.Code, listRec.Body.String())
	var listed memoryRecordListResponse
	require.NoError(t, json.NewDecoder(listRec.Body).Decode(&listed))
	require.Equal(t, 1, listed.Count)
	require.Equal(t, created.ID, listed.Items[0].ID)

	getReq := httptest.NewRequest(http.MethodGet, "/api/runtime/memory/records/"+created.ID, nil)
	getRec := httptest.NewRecorder()
	router.ServeHTTP(getRec, getReq)
	require.Equal(t, http.StatusOK, getRec.Code, getRec.Body.String())
	var fetched memoryRecordDTO
	require.NoError(t, json.NewDecoder(getRec.Body).Decode(&fetched))
	require.Equal(t, created.ID, fetched.ID)
	require.Contains(t, fetched.Text, "apply_patch")

	patchReq := httptest.NewRequest(http.MethodPatch, "/api/runtime/memory/records/"+created.ID, strings.NewReader(`{"pinned":false}`))
	patchReq.Header.Set("Content-Type", "application/json")
	patchReq.Header.Set("X-Skills-Admin-Token", "memory-admin")
	patchRec := httptest.NewRecorder()
	router.ServeHTTP(patchRec, patchReq)
	require.Equal(t, http.StatusOK, patchRec.Code, patchRec.Body.String())

	deleteViaPatch := httptest.NewRequest(http.MethodPatch, "/api/runtime/memory/records/"+created.ID, strings.NewReader(`{"status":"deleted"}`))
	deleteViaPatch.Header.Set("Content-Type", "application/json")
	deleteViaPatch.Header.Set("X-Skills-Admin-Token", "memory-admin")
	deleteViaPatchRec := httptest.NewRecorder()
	router.ServeHTTP(deleteViaPatchRec, deleteViaPatch)
	require.Equal(t, http.StatusBadRequest, deleteViaPatchRec.Code, deleteViaPatchRec.Body.String())

	delReq := httptest.NewRequest(http.MethodDelete, "/api/runtime/memory/records/"+created.ID, nil)
	delReq.Header.Set("X-Skills-Admin-Token", "memory-admin")
	delRec := httptest.NewRecorder()
	router.ServeHTTP(delRec, delReq)
	require.Equal(t, http.StatusOK, delRec.Code, delRec.Body.String())

	listReq = httptest.NewRequest(http.MethodGet, "/api/runtime/memory/records?status=active", nil)
	listRec = httptest.NewRecorder()
	router.ServeHTTP(listRec, listReq)
	require.Equal(t, http.StatusOK, listRec.Code, listRec.Body.String())
	require.NoError(t, json.NewDecoder(listRec.Body).Decode(&listed))
	require.Equal(t, 0, listed.Count)
}

func TestMemoryAPIMultiValueKindTagFilters(t *testing.T) {
	workspace := t.TempDir()
	_, router := newMemoryTestRouter(t, workspace)

	createRecord := func(text, kind string, tags []string) string {
		t.Helper()
		body, err := json.Marshal(map[string]interface{}{
			"text": text, "kind": kind, "tags": tags, "session_id": "multi-value-session",
		})
		require.NoError(t, err)
		req := httptest.NewRequest(http.MethodPost, "/api/runtime/memory/records", strings.NewReader(string(body)))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Skills-Admin-Token", "memory-admin")
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		var created memoryRecordDTO
		require.NoError(t, json.NewDecoder(rec.Body).Decode(&created))
		require.NotEmpty(t, created.ID)
		return created.ID
	}

	decisionBoth := createRecord("multi-value decision with both tags", "decision", []string{"alpha", "beta"})
	factBoth := createRecord("multi-value fact with both tags", "fact", []string{"alpha", "beta"})
	decisionAlpha := createRecord("multi-value decision with alpha only", "decision", []string{"alpha"})

	list := func(values url.Values) []string {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, "/api/runtime/memory/records?"+values.Encode(), nil)
		req.Header.Set("X-Skills-Admin-Token", "memory-admin")
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		var response memoryRecordListResponse
		require.NoError(t, json.NewDecoder(rec.Body).Decode(&response))
		ids := make([]string, 0, len(response.Items))
		for _, item := range response.Items {
			ids = append(ids, item.ID)
		}
		return ids
	}

	repeated := url.Values{}
	repeated.Add("kind", "decision")
	repeated.Add("kind", "fact")
	repeated.Add("tag", "alpha")
	repeated.Add("tag", "beta")
	require.ElementsMatch(t, []string{decisionBoth, factBoth}, list(repeated))

	commaSeparated := url.Values{}
	commaSeparated.Set("kind", "decision,fact")
	commaSeparated.Set("tag", "alpha")
	require.ElementsMatch(t, []string{decisionBoth, factBoth, decisionAlpha}, list(commaSeparated))

	search := url.Values{}
	search.Set("q", "multi-value")
	search.Add("kind", "fact")
	search.Add("tag", "beta")
	require.ElementsMatch(t, []string{factBoth}, list(search))

	store, err := memorystore.New(memorystore.Config{ProjectRoot: workspace, Mode: "on"})
	require.NoError(t, err)
	candidate, err := store.InsertCandidate(context.Background(), memorystore.Candidate{
		SessionID: "multi-value-session", Kind: "fact", Text: "multi-value candidate",
		Tags: []string{"alpha", "beta"}, Confidence: 0.9,
		SourceHash: "multi-value-candidate", SourceRevision: 1,
	})
	require.NoError(t, err)
	require.NoError(t, store.Close())

	statuses := url.Values{}
	statuses.Add("status", "active")
	statuses.Add("status", "candidate")
	require.ElementsMatch(t, []string{decisionBoth, factBoth, decisionAlpha, candidate.ID}, list(statuses))
}

func TestMemoryAPICoreReadUsesReviewedActiveCoreAndHidesInvalidationDetails(t *testing.T) {
	workspace := t.TempDir()
	handler, router := newMemoryTestRouter(t, workspace)

	emptyReq := httptest.NewRequest(http.MethodGet, "/api/runtime/memory/core", nil)
	emptyRec := httptest.NewRecorder()
	router.ServeHTTP(emptyRec, emptyReq)
	require.Equal(t, http.StatusOK, emptyRec.Code, emptyRec.Body.String())
	var empty memoryCoreResponse
	require.NoError(t, json.NewDecoder(emptyRec.Body).Decode(&empty))
	require.Equal(t, "not_found", empty.Status)
	require.Nil(t, empty.Core)

	store, err := memorystore.New(memorystore.Config{
		ProjectRoot: workspace,
		Mode:        "on",
	})
	require.NoError(t, err)
	source, err := store.Append(memorystore.AppendNoteOptions{
		Text: "API Core evidence",
		Kind: "decision",
	})
	require.NoError(t, err)
	candidate, err := store.CreateCoreCandidate(context.Background(), memorystore.CoreMemoryCandidateOptions{
		Content:   "API-visible reviewed Core.",
		SourceIDs: []string{source.ID},
	})
	require.NoError(t, err)
	_, err = store.ActivateCore(context.Background(), candidate.ID, "admin:test")
	require.NoError(t, err)
	require.NoError(t, store.Close())

	activeReq := httptest.NewRequest(http.MethodGet, "/api/runtime/memory/core", nil)
	activeRec := httptest.NewRecorder()
	router.ServeHTTP(activeRec, activeReq)
	require.Equal(t, http.StatusOK, activeRec.Code, activeRec.Body.String())
	var active memoryCoreResponse
	require.NoError(t, json.NewDecoder(activeRec.Body).Decode(&active))
	require.Equal(t, "active", active.Status)
	require.NotNil(t, active.Core)
	require.Equal(t, candidate.ID, active.Core.ID)
	require.Equal(t, "API-visible reviewed Core.", active.Core.Content)

	store, err = memorystore.New(memorystore.Config{
		ProjectRoot: workspace,
		Mode:        "on",
	})
	require.NoError(t, err)
	expired := memorystore.StatusExpired
	_, err = store.UpdateRecord(context.Background(), source.ID, memorystore.RecordPatch{Status: &expired})
	require.NoError(t, err)
	require.NoError(t, store.Close())

	invalidReq := httptest.NewRequest(http.MethodGet, "/api/runtime/memory/core", nil)
	invalidRec := httptest.NewRecorder()
	router.ServeHTTP(invalidRec, invalidReq)
	require.Equal(t, http.StatusConflict, invalidRec.Code, invalidRec.Body.String())
	require.NotContains(t, invalidRec.Body.String(), source.ID)
	require.NotContains(t, invalidRec.Body.String(), "API Core evidence")
	require.Contains(t, invalidRec.Body.String(), "evidence is no longer eligible")

	offConfig := runtimecfg.DefaultRuntimeConfig()
	offConfig.Workspace.Root = workspace
	offConfig.Memory.Mode = "off"
	handler.SetRuntimeConfig(offConfig, "")
	offReq := httptest.NewRequest(http.MethodGet, "/api/runtime/memory/core", nil)
	offRec := httptest.NewRecorder()
	router.ServeHTTP(offRec, offReq)
	require.Equal(t, http.StatusConflict, offRec.Code, offRec.Body.String())
}

func TestMemoryAPICoreUsageIsAdminReadOnlyAndSeparate(t *testing.T) {
	workspace := t.TempDir()
	_, router := newMemoryTestRouter(t, workspace)
	store, err := memorystore.New(memorystore.Config{
		ProjectRoot: workspace, Mode: "on",
	})
	require.NoError(t, err)
	source, err := store.Append(memorystore.AppendNoteOptions{
		Text: "Core usage API evidence",
	})
	require.NoError(t, err)
	candidate, err := store.CreateCoreCandidate(context.Background(), memorystore.CoreMemoryCandidateOptions{
		Content:   "The Core usage API exposes only the dedicated ledger.",
		SourceIDs: []string{source.ID},
	})
	require.NoError(t, err)
	core, err := store.ActivateCore(context.Background(), candidate.ID, "admin:test")
	require.NoError(t, err)
	_, err = store.RecordCoreExposure(context.Background(), memorystore.CoreExposure{
		CoreIDs: []string{core.ID}, SessionID: "session-api-core",
		TurnID: "turn-api-core", UseKind: memorystore.UsageInjectedOnly,
	})
	require.NoError(t, err)
	require.NoError(t, store.Close())

	unauthorized := httptest.NewRequest(http.MethodGet,
		"/api/runtime/memory/core/usage?core_id="+core.ID, nil)
	unauthorizedRec := httptest.NewRecorder()
	router.ServeHTTP(unauthorizedRec, unauthorized)
	require.Equal(t, http.StatusForbidden, unauthorizedRec.Code, unauthorizedRec.Body.String())

	req := httptest.NewRequest(http.MethodGet,
		"/api/runtime/memory/core/usage?core_id="+core.ID+"&session_id=session-api-core", nil)
	req.Header.Set("X-Skills-Admin-Token", "memory-admin")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var response memoryCoreUsageListResponse
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&response))
	require.Equal(t, 1, response.Count)
	require.Len(t, response.Items, 1)
	require.Equal(t, core.ID, response.Items[0].CoreID)
	require.Equal(t, memorystore.UsageInjectedOnly, response.Items[0].UseKind)
}

func TestMemoryAPICoreReviewLifecycleIsAdminGated(t *testing.T) {
	workspace := t.TempDir()
	_, router := newMemoryTestRouter(t, workspace)

	store, err := memorystore.New(memorystore.Config{
		ProjectRoot: workspace,
		Mode:        "on",
	})
	require.NoError(t, err)
	source, err := store.Append(memorystore.AppendNoteOptions{Text: "stable evidence"})
	require.NoError(t, err)
	secondSource, err := store.Append(memorystore.AppendNoteOptions{Text: "evidence that will expire"})
	require.NoError(t, err)
	candidate, err := store.CreateCoreCandidate(context.Background(), memorystore.CoreMemoryCandidateOptions{
		Content:   "Review this project invariant before activation.",
		SourceIDs: []string{source.ID},
	})
	require.NoError(t, err)
	second, err := store.CreateCoreCandidate(context.Background(), memorystore.CoreMemoryCandidateOptions{
		Content:   "This candidate must fail closed after its evidence expires.",
		SourceIDs: []string{secondSource.ID},
	})
	require.NoError(t, err)
	require.NoError(t, store.Close())

	unauthorized := httptest.NewRequest(http.MethodGet, "/api/runtime/memory/cores?status=candidate", nil)
	unauthorizedRec := httptest.NewRecorder()
	router.ServeHTTP(unauthorizedRec, unauthorized)
	require.Equal(t, http.StatusForbidden, unauthorizedRec.Code, unauthorizedRec.Body.String())

	listReq := httptest.NewRequest(http.MethodGet, "/api/runtime/memory/cores?status=candidate", nil)
	listReq.Header.Set("X-Skills-Admin-Token", "memory-admin")
	listRec := httptest.NewRecorder()
	router.ServeHTTP(listRec, listReq)
	require.Equal(t, http.StatusOK, listRec.Code, listRec.Body.String())
	var listed memoryCoreListResponse
	require.NoError(t, json.NewDecoder(listRec.Body).Decode(&listed))
	require.Equal(t, memorystore.CoreStatusCandidate, listed.Status)
	require.Equal(t, 2, listed.Count)

	activateReq := httptest.NewRequest(http.MethodPost,
		"/api/runtime/memory/cores/"+candidate.ID+"/activate",
		strings.NewReader(`{"approved_by":"attacker-supplied"}`))
	activateReq.Header.Set("Content-Type", "application/json")
	activateReq.Header.Set("X-Skills-Admin-Token", "memory-admin")
	activateRec := httptest.NewRecorder()
	router.ServeHTTP(activateRec, activateReq)
	require.Equal(t, http.StatusOK, activateRec.Code, activateRec.Body.String())
	var activated memorystore.CoreMemory
	require.NoError(t, json.NewDecoder(activateRec.Body).Decode(&activated))
	require.Equal(t, candidate.ID, activated.ID)
	require.Equal(t, memorystore.CoreStatusActive, activated.Status)
	require.Equal(t, "admin:runtime-api", activated.ApprovedBy)

	store, err = memorystore.New(memorystore.Config{
		ProjectRoot: workspace,
		Mode:        "on",
	})
	require.NoError(t, err)
	expired := memorystore.StatusExpired
	_, err = store.UpdateRecord(context.Background(), secondSource.ID,
		memorystore.RecordPatch{Status: &expired})
	require.NoError(t, err)
	require.NoError(t, store.Close())

	rejectedActivate := httptest.NewRequest(http.MethodPost,
		"/api/runtime/memory/cores/"+second.ID+"/activate", nil)
	rejectedActivate.Header.Set("X-Skills-Admin-Token", "memory-admin")
	rejectedRec := httptest.NewRecorder()
	router.ServeHTTP(rejectedRec, rejectedActivate)
	require.Equal(t, http.StatusConflict, rejectedRec.Code, rejectedRec.Body.String())
	require.NotContains(t, rejectedRec.Body.String(), secondSource.ID)

	badArchiveReq := httptest.NewRequest(http.MethodPost,
		"/api/runtime/memory/cores/"+second.ID+"/archive",
		strings.NewReader(`{"reason":"operator sk-123456789012345678901234"}`))
	badArchiveReq.Header.Set("Content-Type", "application/json")
	badArchiveReq.Header.Set("X-Skills-Admin-Token", "memory-admin")
	badArchiveRec := httptest.NewRecorder()
	router.ServeHTTP(badArchiveRec, badArchiveReq)
	require.Equal(t, http.StatusBadRequest, badArchiveRec.Code, badArchiveRec.Body.String())
	require.Contains(t, badArchiveRec.Body.String(), "archive reason is not allowed")

	archiveReq := httptest.NewRequest(http.MethodPost,
		"/api/runtime/memory/cores/"+second.ID+"/archive",
		strings.NewReader(`{"reason":"evidence expired during review"}`))
	archiveReq.Header.Set("Content-Type", "application/json")
	archiveReq.Header.Set("X-Skills-Admin-Token", "memory-admin")
	archiveRec := httptest.NewRecorder()
	router.ServeHTTP(archiveRec, archiveReq)
	require.Equal(t, http.StatusOK, archiveRec.Code, archiveRec.Body.String())

	listReq = httptest.NewRequest(http.MethodGet, "/api/runtime/memory/cores?status=candidate", nil)
	listReq.Header.Set("X-Skills-Admin-Token", "memory-admin")
	listRec = httptest.NewRecorder()
	router.ServeHTTP(listRec, listReq)
	require.Equal(t, http.StatusOK, listRec.Code, listRec.Body.String())
	require.NoError(t, json.NewDecoder(listRec.Body).Decode(&listed))
	require.Equal(t, 0, listed.Count)

	archiveActiveReq := httptest.NewRequest(http.MethodPost,
		"/api/runtime/memory/cores/"+candidate.ID+"/archive",
		strings.NewReader(`{"reason":"operator retired this core"}`))
	archiveActiveReq.Header.Set("Content-Type", "application/json")
	archiveActiveReq.Header.Set("X-Skills-Admin-Token", "memory-admin")
	archiveActiveRec := httptest.NewRecorder()
	router.ServeHTTP(archiveActiveRec, archiveActiveReq)
	require.Equal(t, http.StatusOK, archiveActiveRec.Code, archiveActiveRec.Body.String())

	coreReq := httptest.NewRequest(http.MethodGet, "/api/runtime/memory/core", nil)
	coreRec := httptest.NewRecorder()
	router.ServeHTTP(coreRec, coreReq)
	require.Equal(t, http.StatusOK, coreRec.Code, coreRec.Body.String())
	var current memoryCoreResponse
	require.NoError(t, json.NewDecoder(coreRec.Body).Decode(&current))
	require.Equal(t, "not_found", current.Status)
}

func TestMemoryAPICoreRebuildIsAdminGatedAndLeavesCandidatePending(t *testing.T) {
	workspace := t.TempDir()
	_, router := newMemoryTestRouter(t, workspace)

	store, err := memorystore.New(memorystore.Config{
		ProjectRoot: workspace,
		Mode:        "on",
	})
	require.NoError(t, err)
	pinned := true
	source, err := store.Append(memorystore.AppendNoteOptions{
		Text: "The runtime API rebuild creates reviewable candidates only.",
		Kind: "decision", Pinned: &pinned,
	})
	require.NoError(t, err)
	require.NoError(t, store.Close())

	unauthorized := httptest.NewRequest(http.MethodPost, "/api/runtime/memory/core/rebuild", strings.NewReader(`{}`))
	unauthorizedRec := httptest.NewRecorder()
	router.ServeHTTP(unauthorizedRec, unauthorized)
	require.Equal(t, http.StatusForbidden, unauthorizedRec.Code, unauthorizedRec.Body.String())

	rebuildReq := httptest.NewRequest(http.MethodPost, "/api/runtime/memory/core/rebuild", strings.NewReader(`{}`))
	rebuildReq.Header.Set("Content-Type", "application/json")
	rebuildReq.Header.Set("X-Skills-Admin-Token", "memory-admin")
	rebuildRec := httptest.NewRecorder()
	router.ServeHTTP(rebuildRec, rebuildReq)
	require.Equal(t, http.StatusOK, rebuildRec.Code, rebuildRec.Body.String())
	var rebuilt memoryCoreRebuildResponse
	require.NoError(t, json.NewDecoder(rebuildRec.Body).Decode(&rebuilt))
	require.Equal(t, workspace, rebuilt.WorkspacePath)
	require.Equal(t, memorystore.JobStatusSucceeded, rebuilt.JobStatus)
	require.Equal(t, 1, rebuilt.SourceCount)
	require.NotNil(t, rebuilt.Candidate)
	require.Equal(t, memorystore.CoreStatusCandidate, rebuilt.Candidate.Status)
	require.Contains(t, rebuilt.Candidate.SourceIDs, source.ID)

	listReq := httptest.NewRequest(http.MethodGet, "/api/runtime/memory/cores?status=candidate", nil)
	listReq.Header.Set("X-Skills-Admin-Token", "memory-admin")
	listRec := httptest.NewRecorder()
	router.ServeHTTP(listRec, listReq)
	require.Equal(t, http.StatusOK, listRec.Code, listRec.Body.String())
	var listed memoryCoreListResponse
	require.NoError(t, json.NewDecoder(listRec.Body).Decode(&listed))
	require.Len(t, listed.Items, 1)
	require.Equal(t, rebuilt.Candidate.ID, listed.Items[0].ID)

	activeReq := httptest.NewRequest(http.MethodGet, "/api/runtime/memory/core", nil)
	activeRec := httptest.NewRecorder()
	router.ServeHTTP(activeRec, activeReq)
	require.Equal(t, http.StatusOK, activeRec.Code, activeRec.Body.String())
	var active memoryCoreResponse
	require.NoError(t, json.NewDecoder(activeRec.Body).Decode(&active))
	require.Equal(t, "not_found", active.Status, "rebuild must not promote its candidate")
	require.Nil(t, active.Core)
}

func TestMemoryAPICoreRebuildRejectsOversizedTrailingDataAndWorkspaceMismatch(t *testing.T) {
	tests := []struct {
		name       string
		body       string
		wantStatus int
	}{
		{
			name:       "oversized trailing whitespace",
			body:       `{"workspace_path":""}` + strings.Repeat(" ", 5*1024),
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "multiple json documents",
			body:       `{"workspace_path":""} {}`,
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "workspace mismatch",
			body:       `{"workspace_path":"../outside-workspace"}`,
			wantStatus: http.StatusBadRequest,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			workspace := t.TempDir()
			_, router := newMemoryTestRouter(t, workspace)

			request := httptest.NewRequest(
				http.MethodPost, "/api/runtime/memory/core/rebuild", strings.NewReader(tt.body),
			)
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set("X-Skills-Admin-Token", "memory-admin")
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)

			require.Equal(t, tt.wantStatus, response.Code, response.Body.String())
			_, err := os.Stat(filepath.Join(workspace, ".aicli", "memory", "memory.db"))
			require.ErrorIs(t, err, os.ErrNotExist, "rejected requests must not open or create the store")
		})
	}
}

func TestMemoryAPICoreRebuildModeOffDoesNotCreateDatabase(t *testing.T) {
	workspace := t.TempDir()
	handler, router := newMemoryTestRouter(t, workspace)
	config := runtimecfg.DefaultRuntimeConfig()
	config.Workspace.Root = workspace
	config.Memory.Mode = "off"
	handler.SetRuntimeConfig(config, "")

	request := httptest.NewRequest(http.MethodPost, "/api/runtime/memory/core/rebuild", strings.NewReader(`{}`))
	request.Header.Set("X-Skills-Admin-Token", "memory-admin")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)

	require.Equal(t, http.StatusConflict, response.Code, response.Body.String())
	_, err := os.Stat(filepath.Join(workspace, ".aicli", "memory", "memory.db"))
	require.ErrorIs(t, err, os.ErrNotExist)
}

func TestMemoryAPICoreRebuildWithoutSafeSourcesPreservesActiveCore(t *testing.T) {
	workspace := t.TempDir()
	_, router := newMemoryTestRouter(t, workspace)

	store, err := memorystore.New(memorystore.Config{ProjectRoot: workspace, Mode: "on"})
	require.NoError(t, err)
	pinned := false
	source, err := store.Append(memorystore.AppendNoteOptions{
		Text: "This reviewed Core remains active when rebuild has no eligible inputs.",
		Kind: "decision", Pinned: &pinned,
	})
	require.NoError(t, err)
	active, err := store.CreateCoreCandidate(context.Background(), memorystore.CoreMemoryCandidateOptions{
		Content:   "Preserve this active Core.",
		SourceIDs: []string{source.ID},
	})
	require.NoError(t, err)
	active, err = store.ActivateCore(context.Background(), active.ID, "admin:test")
	require.NoError(t, err)
	require.NoError(t, store.Close())

	rebuildRequest := httptest.NewRequest(
		http.MethodPost, "/api/runtime/memory/core/rebuild", strings.NewReader(`{}`),
	)
	rebuildRequest.Header.Set("Content-Type", "application/json")
	rebuildRequest.Header.Set("X-Skills-Admin-Token", "memory-admin")
	rebuildResponse := httptest.NewRecorder()
	router.ServeHTTP(rebuildResponse, rebuildRequest)
	require.Equal(t, http.StatusOK, rebuildResponse.Code, rebuildResponse.Body.String())

	var rebuilt memoryCoreRebuildResponse
	require.NoError(t, json.NewDecoder(rebuildResponse.Body).Decode(&rebuilt))
	require.Equal(t, memorystore.JobStatusSucceeded, rebuilt.JobStatus)
	require.Equal(t, "no_safe_sources", rebuilt.Skipped)
	require.Nil(t, rebuilt.Candidate)

	coreRequest := httptest.NewRequest(http.MethodGet, "/api/runtime/memory/core", nil)
	coreResponse := httptest.NewRecorder()
	router.ServeHTTP(coreResponse, coreRequest)
	require.Equal(t, http.StatusOK, coreResponse.Code, coreResponse.Body.String())
	var current memoryCoreResponse
	require.NoError(t, json.NewDecoder(coreResponse.Body).Decode(&current))
	require.Equal(t, memorystore.CoreStatusActive, current.Status)
	require.NotNil(t, current.Core)
	require.Equal(t, active.ID, current.Core.ID)
	require.Equal(t, active.Content, current.Core.Content)
}

func TestMemoryAPIRecordVersionsAndRollback(t *testing.T) {
	workspace := t.TempDir()
	_, router := newMemoryTestRouter(t, workspace)

	createReq := httptest.NewRequest(http.MethodPost, "/api/runtime/memory/records",
		strings.NewReader(`{"text":"version one","kind":"fact"}`))
	createReq.Header.Set("Content-Type", "application/json")
	createReq.Header.Set("X-Skills-Admin-Token", "memory-admin")
	createRec := httptest.NewRecorder()
	router.ServeHTTP(createRec, createReq)
	require.Equal(t, http.StatusOK, createRec.Code, createRec.Body.String())
	var created memoryRecordDTO
	require.NoError(t, json.NewDecoder(createRec.Body).Decode(&created))

	updatedText := `{"title":"version two title"}`
	patchReq := httptest.NewRequest(http.MethodPatch,
		"/api/runtime/memory/records/"+created.ID, strings.NewReader(updatedText))
	patchReq.Header.Set("Content-Type", "application/json")
	patchReq.Header.Set("X-Skills-Admin-Token", "memory-admin")
	patchRec := httptest.NewRecorder()
	router.ServeHTTP(patchRec, patchReq)
	require.Equal(t, http.StatusOK, patchRec.Code, patchRec.Body.String())
	var patched memoryRecordDTO
	require.NoError(t, json.NewDecoder(patchRec.Body).Decode(&patched))
	require.Equal(t, int64(2), patched.Revision)

	unauthorized := httptest.NewRequest(http.MethodGet,
		"/api/runtime/memory/records/"+created.ID+"/versions", nil)
	unauthorizedRec := httptest.NewRecorder()
	router.ServeHTTP(unauthorizedRec, unauthorized)
	require.Equal(t, http.StatusForbidden, unauthorizedRec.Code, unauthorizedRec.Body.String())

	historyReq := httptest.NewRequest(http.MethodGet,
		"/api/runtime/memory/records/"+created.ID+"/versions", nil)
	historyReq.Header.Set("X-Skills-Admin-Token", "memory-admin")
	historyRec := httptest.NewRecorder()
	router.ServeHTTP(historyRec, historyReq)
	require.Equal(t, http.StatusOK, historyRec.Code, historyRec.Body.String())
	var history memoryRecordVersionsResponse
	require.NoError(t, json.NewDecoder(historyRec.Body).Decode(&history))
	require.Equal(t, 2, history.Count)
	require.Equal(t, "version one", history.Items[0].Record.Text)
	require.Equal(t, "version two title", history.Items[1].Record.Title)

	boundedHistoryReq := httptest.NewRequest(http.MethodGet,
		"/api/runtime/memory/records/"+created.ID+"/versions?limit=1", nil)
	boundedHistoryReq.Header.Set("X-Skills-Admin-Token", "memory-admin")
	boundedHistoryRec := httptest.NewRecorder()
	router.ServeHTTP(boundedHistoryRec, boundedHistoryReq)
	require.Equal(t, http.StatusOK, boundedHistoryRec.Code, boundedHistoryRec.Body.String())
	var boundedHistory memoryRecordVersionsResponse
	require.NoError(t, json.NewDecoder(boundedHistoryRec.Body).Decode(&boundedHistory))
	require.Len(t, boundedHistory.Items, 1)
	require.True(t, boundedHistory.HasMore)
	require.Equal(t, int64(2), boundedHistory.Items[0].Version,
		"bounded API history returns the newest window in ascending order")

	rollbackReq := httptest.NewRequest(http.MethodPost,
		"/api/runtime/memory/records/"+created.ID+"/rollback",
		strings.NewReader(`{"target_version":1,"expected_revision":2,"reason":"restore known-good text"}`))
	rollbackReq.Header.Set("Content-Type", "application/json")
	rollbackReq.Header.Set("X-Skills-Admin-Token", "memory-admin")
	rollbackRec := httptest.NewRecorder()
	router.ServeHTTP(rollbackRec, rollbackReq)
	require.Equal(t, http.StatusOK, rollbackRec.Code, rollbackRec.Body.String())
	var rolledBack memoryRecordDTO
	require.NoError(t, json.NewDecoder(rollbackRec.Body).Decode(&rolledBack))
	require.Equal(t, "version one", rolledBack.Text)
	require.Equal(t, "", rolledBack.Title)
	require.Equal(t, int64(3), rolledBack.Revision)

	staleRollbackReq := httptest.NewRequest(http.MethodPost,
		"/api/runtime/memory/records/"+created.ID+"/rollback",
		strings.NewReader(`{"target_version":2,"expected_revision":2,"reason":"stale request"}`))
	staleRollbackReq.Header.Set("Content-Type", "application/json")
	staleRollbackReq.Header.Set("X-Skills-Admin-Token", "memory-admin")
	staleRollbackRec := httptest.NewRecorder()
	router.ServeHTTP(staleRollbackRec, staleRollbackReq)
	require.Equal(t, http.StatusConflict, staleRollbackRec.Code, staleRollbackRec.Body.String())
}

func TestMemoryAPIRollbackAuthorizationWorkspaceAndValidation(t *testing.T) {
	workspace := t.TempDir()
	otherWorkspace := t.TempDir()
	_, router := newMemoryTestRouter(t, workspace)

	createReq := httptest.NewRequest(http.MethodPost, "/api/runtime/memory/records",
		strings.NewReader(`{"text":"rollback validation record","kind":"fact"}`))
	createReq.Header.Set("Content-Type", "application/json")
	createReq.Header.Set("X-Skills-Admin-Token", "memory-admin")
	createRec := httptest.NewRecorder()
	router.ServeHTTP(createRec, createReq)
	require.Equal(t, http.StatusOK, createRec.Code, createRec.Body.String())
	var created memoryRecordDTO
	require.NoError(t, json.NewDecoder(createRec.Body).Decode(&created))

	// Both history and rollback are operator surfaces. Reject before revealing
	// whether a record/version exists or parsing a caller-controlled body.
	unauthorizedHistory := httptest.NewRequest(http.MethodGet,
		"/api/runtime/memory/records/"+created.ID+"/versions", nil)
	unauthorizedHistoryRec := httptest.NewRecorder()
	router.ServeHTTP(unauthorizedHistoryRec, unauthorizedHistory)
	require.Equal(t, http.StatusForbidden, unauthorizedHistoryRec.Code)

	unauthorizedRollback := httptest.NewRequest(http.MethodPost,
		"/api/runtime/memory/records/"+created.ID+"/rollback", strings.NewReader("{malformed"))
	unauthorizedRollbackRec := httptest.NewRecorder()
	router.ServeHTTP(unauthorizedRollbackRec, unauthorizedRollback)
	require.Equal(t, http.StatusForbidden, unauthorizedRollbackRec.Code)

	wrongWorkspaceHistory := httptest.NewRequest(http.MethodGet,
		"/api/runtime/memory/records/"+created.ID+"/versions?workspace_path="+url.QueryEscape(otherWorkspace), nil)
	wrongWorkspaceHistory.Header.Set("X-Skills-Admin-Token", "memory-admin")
	wrongWorkspaceHistoryRec := httptest.NewRecorder()
	router.ServeHTTP(wrongWorkspaceHistoryRec, wrongWorkspaceHistory)
	require.Equal(t, http.StatusNotFound, wrongWorkspaceHistoryRec.Code)

	requestRollback := func(body string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost,
			"/api/runtime/memory/records/"+created.ID+"/rollback", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Skills-Admin-Token", "memory-admin")
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		return rec
	}

	require.Equal(t, http.StatusBadRequest, requestRollback("{malformed").Code)
	require.Equal(t, http.StatusBadRequest, requestRollback(
		`{"target_version":1,"reason":"missing expected revision"}`).Code)
	require.Equal(t, http.StatusBadRequest, requestRollback(
		`{"target_version":1,"expected_revision":1}`).Code)
	require.Equal(t, http.StatusNotFound, requestRollback(
		`{"target_version":99,"expected_revision":1,"reason":"missing target"}`).Code)

	wrongWorkspaceBody, err := json.Marshal(memoryRecordRollbackRequest{
		WorkspacePath: otherWorkspace, TargetVersion: 1, ExpectedRevision: 1, Reason: "wrong workspace",
	})
	require.NoError(t, err)
	wrongWorkspaceRollback := httptest.NewRequest(http.MethodPost,
		"/api/runtime/memory/records/"+created.ID+"/rollback", strings.NewReader(string(wrongWorkspaceBody)))
	wrongWorkspaceRollback.Header.Set("Content-Type", "application/json")
	wrongWorkspaceRollback.Header.Set("X-Skills-Admin-Token", "memory-admin")
	wrongWorkspaceRollbackRec := httptest.NewRecorder()
	router.ServeHTTP(wrongWorkspaceRollbackRec, wrongWorkspaceRollback)
	require.Equal(t, http.StatusNotFound, wrongWorkspaceRollbackRec.Code)

	// Rejections above must not have created a new revision.
	historyReq := httptest.NewRequest(http.MethodGet,
		"/api/runtime/memory/records/"+created.ID+"/versions", nil)
	historyReq.Header.Set("X-Skills-Admin-Token", "memory-admin")
	historyRec := httptest.NewRecorder()
	router.ServeHTTP(historyRec, historyReq)
	require.Equal(t, http.StatusOK, historyRec.Code, historyRec.Body.String())
	var history memoryRecordVersionsResponse
	require.NoError(t, json.NewDecoder(historyRec.Body).Decode(&history))
	require.Len(t, history.Items, 1)
}

func TestMemoryAPIRecordAsOfRequiresAdminAndReturnsHistoricalSnapshot(t *testing.T) {
	workspace := t.TempDir()
	_, router := newMemoryTestRouter(t, workspace)

	createReq := httptest.NewRequest(http.MethodPost, "/api/runtime/memory/records",
		strings.NewReader(`{"text":"API historical first","kind":"fact","title":"first title"}`))
	createReq.Header.Set("Content-Type", "application/json")
	createReq.Header.Set("X-Skills-Admin-Token", "memory-admin")
	createRec := httptest.NewRecorder()
	router.ServeHTTP(createRec, createReq)
	require.Equal(t, http.StatusOK, createRec.Code, createRec.Body.String())
	var created memoryRecordDTO
	require.NoError(t, json.NewDecoder(createRec.Body).Decode(&created))

	historyReq := httptest.NewRequest(http.MethodGet,
		"/api/runtime/memory/records/"+created.ID+"/versions", nil)
	historyReq.Header.Set("X-Skills-Admin-Token", "memory-admin")
	historyRec := httptest.NewRecorder()
	router.ServeHTTP(historyRec, historyReq)
	require.Equal(t, http.StatusOK, historyRec.Code, historyRec.Body.String())
	var history memoryRecordVersionsResponse
	require.NoError(t, json.NewDecoder(historyRec.Body).Decode(&history))
	require.Len(t, history.Items, 1)
	firstVersionTime, err := time.Parse(time.RFC3339Nano, history.Items[0].CreatedAt)
	require.NoError(t, err)

	patchReq := httptest.NewRequest(http.MethodPatch, "/api/runtime/memory/records/"+created.ID,
		strings.NewReader(`{"text":"API historical second","title":"second title"}`))
	patchReq.Header.Set("Content-Type", "application/json")
	patchReq.Header.Set("X-Skills-Admin-Token", "memory-admin")
	patchRec := httptest.NewRecorder()
	router.ServeHTTP(patchRec, patchReq)
	require.Equal(t, http.StatusOK, patchRec.Code, patchRec.Body.String())

	asOfValue := firstVersionTime.UTC().Format(time.RFC3339Nano)
	unauthorized := httptest.NewRequest(http.MethodGet,
		"/api/runtime/memory/records?as_of="+url.QueryEscape(asOfValue), nil)
	unauthorizedRec := httptest.NewRecorder()
	router.ServeHTTP(unauthorizedRec, unauthorized)
	require.Equal(t, http.StatusForbidden, unauthorizedRec.Code, unauthorizedRec.Body.String())

	malformed := httptest.NewRequest(http.MethodGet,
		"/api/runtime/memory/records?as_of=not-a-timestamp", nil)
	malformed.Header.Set("X-Skills-Admin-Token", "memory-admin")
	malformedRec := httptest.NewRecorder()
	router.ServeHTTP(malformedRec, malformed)
	require.Equal(t, http.StatusBadRequest, malformedRec.Code, malformedRec.Body.String())

	zeroTime := httptest.NewRequest(http.MethodGet,
		"/api/runtime/memory/records?as_of="+url.QueryEscape("0001-01-01T00:00:00Z"), nil)
	zeroTime.Header.Set("X-Skills-Admin-Token", "memory-admin")
	zeroTimeRec := httptest.NewRecorder()
	router.ServeHTTP(zeroTimeRec, zeroTime)
	require.Equal(t, http.StatusBadRequest, zeroTimeRec.Code, zeroTimeRec.Body.String())

	historical := httptest.NewRequest(http.MethodGet,
		"/api/runtime/memory/records?as_of="+url.QueryEscape(asOfValue), nil)
	historical.Header.Set("X-Skills-Admin-Token", "memory-admin")
	historicalRec := httptest.NewRecorder()
	router.ServeHTTP(historicalRec, historical)
	require.Equal(t, http.StatusOK, historicalRec.Code, historicalRec.Body.String())
	var historicalResponse memoryRecordListResponse
	require.NoError(t, json.NewDecoder(historicalRec.Body).Decode(&historicalResponse))
	require.Equal(t, asOfValue, historicalResponse.AsOf)
	require.Equal(t, 1, historicalResponse.Count, "%+v", historicalResponse)
	require.Equal(t, "API historical first", historicalResponse.Items[0].Text)
	require.Equal(t, "first title", historicalResponse.Items[0].Title)

	current := httptest.NewRequest(http.MethodGet, "/api/runtime/memory/records", nil)
	currentRec := httptest.NewRecorder()
	router.ServeHTTP(currentRec, current)
	require.Equal(t, http.StatusOK, currentRec.Code, currentRec.Body.String())
	var currentResponse memoryRecordListResponse
	require.NoError(t, json.NewDecoder(currentRec.Body).Decode(&currentResponse))
	require.Equal(t, 1, currentResponse.Count)
	require.Equal(t, "API historical second", currentResponse.Items[0].Text)
}

func TestMemoryAPIRecordMetadataAndArchivedVisibility(t *testing.T) {
	workspace := t.TempDir()
	_, router := newMemoryTestRouter(t, workspace)

	createReq := httptest.NewRequest(http.MethodPost, "/api/runtime/memory/records", strings.NewReader(
		`{"text":"API metadata lifecycle","kind":"fact","title":"API title","summary":"API summary","canonical_id":"claim:api-metadata"}`,
	))
	createReq.Header.Set("Content-Type", "application/json")
	createReq.Header.Set("X-Skills-Admin-Token", "memory-admin")
	createRec := httptest.NewRecorder()
	router.ServeHTTP(createRec, createReq)
	require.Equal(t, http.StatusOK, createRec.Code, createRec.Body.String())
	var created memoryRecordDTO
	require.NoError(t, json.NewDecoder(createRec.Body).Decode(&created))
	require.Equal(t, "API title", created.Title)
	require.Equal(t, "API summary", created.Summary)
	require.Equal(t, "claim:api-metadata", created.CanonicalID)
	require.False(t, created.Archived)

	patchReq := httptest.NewRequest(http.MethodPatch, "/api/runtime/memory/records/"+created.ID,
		strings.NewReader(`{"title":"Archived API title","summary":"Archived API summary","canonical_id":"claim:api-metadata:v2","archived":true}`))
	patchReq.Header.Set("Content-Type", "application/json")
	patchReq.Header.Set("X-Skills-Admin-Token", "memory-admin")
	patchRec := httptest.NewRecorder()
	router.ServeHTTP(patchRec, patchReq)
	require.Equal(t, http.StatusOK, patchRec.Code, patchRec.Body.String())
	var patched memoryRecordDTO
	require.NoError(t, json.NewDecoder(patchRec.Body).Decode(&patched))
	require.Equal(t, "Archived API title", patched.Title)
	require.Equal(t, "Archived API summary", patched.Summary)
	require.Equal(t, "claim:api-metadata:v2", patched.CanonicalID)
	require.True(t, patched.Archived)

	defaultListReq := httptest.NewRequest(http.MethodGet, "/api/runtime/memory/records?status=active", nil)
	defaultListRec := httptest.NewRecorder()
	router.ServeHTTP(defaultListRec, defaultListReq)
	require.Equal(t, http.StatusOK, defaultListRec.Code, defaultListRec.Body.String())
	var defaultList memoryRecordListResponse
	require.NoError(t, json.NewDecoder(defaultListRec.Body).Decode(&defaultList))
	require.Zero(t, defaultList.Count)

	nonAdminArchivedReq := httptest.NewRequest(http.MethodGet,
		"/api/runtime/memory/records?status=active&include_archived=true", nil)
	nonAdminArchivedRec := httptest.NewRecorder()
	router.ServeHTTP(nonAdminArchivedRec, nonAdminArchivedReq)
	require.Equal(t, http.StatusForbidden, nonAdminArchivedRec.Code, nonAdminArchivedRec.Body.String())

	adminArchivedReq := httptest.NewRequest(http.MethodGet,
		"/api/runtime/memory/records?status=active&include_archived=true", nil)
	adminArchivedReq.Header.Set("X-Skills-Admin-Token", "memory-admin")
	adminArchivedRec := httptest.NewRecorder()
	router.ServeHTTP(adminArchivedRec, adminArchivedReq)
	require.Equal(t, http.StatusOK, adminArchivedRec.Code, adminArchivedRec.Body.String())
	var adminList memoryRecordListResponse
	require.NoError(t, json.NewDecoder(adminArchivedRec.Body).Decode(&adminList))
	require.Equal(t, 1, adminList.Count)
	require.Equal(t, patched.CanonicalID, adminList.Items[0].CanonicalID)
	require.True(t, adminList.Items[0].Archived)

	nonAdminGetReq := httptest.NewRequest(http.MethodGet, "/api/runtime/memory/records/"+created.ID, nil)
	nonAdminGetRec := httptest.NewRecorder()
	router.ServeHTTP(nonAdminGetRec, nonAdminGetReq)
	require.Equal(t, http.StatusForbidden, nonAdminGetRec.Code, nonAdminGetRec.Body.String())

	adminGetReq := httptest.NewRequest(http.MethodGet, "/api/runtime/memory/records/"+created.ID, nil)
	adminGetReq.Header.Set("X-Skills-Admin-Token", "memory-admin")
	adminGetRec := httptest.NewRecorder()
	router.ServeHTTP(adminGetRec, adminGetReq)
	require.Equal(t, http.StatusOK, adminGetRec.Code, adminGetRec.Body.String())
	var fetched memoryRecordDTO
	require.NoError(t, json.NewDecoder(adminGetRec.Body).Decode(&fetched))
	require.Equal(t, patched.Title, fetched.Title)
	require.Equal(t, patched.Summary, fetched.Summary)
	require.Equal(t, patched.CanonicalID, fetched.CanonicalID)
	require.True(t, fetched.Archived)
}

func TestMemoryAPIRecordsSourceKindFilter(t *testing.T) {
	workspace := t.TempDir()
	_, router := newMemoryTestRouter(t, workspace)
	store, err := memorystore.New(memorystore.Config{ProjectRoot: workspace, Mode: "on"})
	require.NoError(t, err)
	manual, err := store.Append(memorystore.AppendNoteOptions{Text: "shared manual record"})
	require.NoError(t, err)
	candidate, err := store.InsertCandidate(context.Background(), memorystore.Candidate{
		SessionID: "api-auto-session", Kind: "fact", Text: "shared extracted record",
		Confidence: 0.9, SourceHash: "api-auto-source", SourceRevision: 1,
	})
	require.NoError(t, err)
	active := memorystore.StatusActive
	automatic, err := store.UpdateRecord(context.Background(), candidate.ID, memorystore.RecordPatch{Status: &active})
	require.NoError(t, err)
	require.NoError(t, store.Close())

	listReq := httptest.NewRequest(http.MethodGet, "/api/runtime/memory/records?status=active", nil)
	listRec := httptest.NewRecorder()
	router.ServeHTTP(listRec, listReq)
	require.Equal(t, http.StatusOK, listRec.Code, listRec.Body.String())
	var all memoryRecordListResponse
	require.NoError(t, json.NewDecoder(listRec.Body).Decode(&all))
	require.Equal(t, 2, all.Count)

	filteredReq := httptest.NewRequest(
		http.MethodGet, "/api/runtime/memory/records?status=active&source_kind=SESSION_EXTRACT", nil,
	)
	filteredRec := httptest.NewRecorder()
	router.ServeHTTP(filteredRec, filteredReq)
	require.Equal(t, http.StatusOK, filteredRec.Code, filteredRec.Body.String())
	var filtered memoryRecordListResponse
	require.NoError(t, json.NewDecoder(filteredRec.Body).Decode(&filtered))
	require.Equal(t, 1, filtered.Count)
	require.Equal(t, automatic.ID, filtered.Items[0].ID)
	require.NotEqual(t, manual.ID, filtered.Items[0].ID)
}

func TestMemoryStatusReportsRuntimeDigestStats(t *testing.T) {
	workspace := t.TempDir()
	handler, router := newMemoryTestRouter(t, workspace)
	store, err := memorystore.New(memorystore.Config{ProjectRoot: workspace, Mode: "on"})
	require.NoError(t, err)
	rt := memoryruntime.New(store, nil, nil, nil, memoryruntime.Config{
		Mode: "on", Workspace: workspace, ProjectKey: store.ProjectKey(),
	})
	handler.SetMemoryRuntime(rt)
	t.Cleanup(func() { _ = handler.CloseMemoryRuntime() })

	req := httptest.NewRequest(http.MethodGet, "/api/runtime/memory/status", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	var status memoryStatusResponse
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&status))
	require.True(t, status.MemoryRuntimePresent)
	require.Equal(t, workspace, status.MemoryRuntimeWorkspace)
	require.NotNil(t, status.DigestRuntime)
	require.Zero(t, status.DigestRuntime.Written)
	require.Zero(t, status.DigestRuntime.Failed)
}

func TestMemoryStatusReportsStartupFailureOnlyToAdmin(t *testing.T) {
	workspace := t.TempDir()
	handler, router := newMemoryTestRouter(t, workspace)
	handler.SetMemoryRuntimeBootError(fmt.Errorf("provider token sk-abcdefghijklmnop failed during startup"))

	nonAdminReq := httptest.NewRequest(http.MethodGet, "/api/runtime/memory/status", nil)
	nonAdminRec := httptest.NewRecorder()
	router.ServeHTTP(nonAdminRec, nonAdminReq)
	require.Equal(t, http.StatusOK, nonAdminRec.Code, nonAdminRec.Body.String())
	var nonAdmin memoryStatusResponse
	require.NoError(t, json.NewDecoder(nonAdminRec.Body).Decode(&nonAdmin))
	require.Equal(t, "unavailable", nonAdmin.MemoryRuntimeState)
	require.Equal(t, "startup_failed", nonAdmin.MemoryRuntimeReason)
	require.Empty(t, nonAdmin.MemoryRuntimeError)
	require.NotContains(t, nonAdminRec.Body.String(), "sk-abcdefghijklmnop")

	adminReq := httptest.NewRequest(http.MethodGet, "/api/runtime/memory/status", nil)
	adminReq.Header.Set("X-Skills-Admin-Token", "memory-admin")
	adminRec := httptest.NewRecorder()
	router.ServeHTTP(adminRec, adminReq)
	require.Equal(t, http.StatusOK, adminRec.Code, adminRec.Body.String())
	var admin memoryStatusResponse
	require.NoError(t, json.NewDecoder(adminRec.Body).Decode(&admin))
	require.Equal(t, "startup_failed", admin.MemoryRuntimeReason)
	require.NotEmpty(t, admin.MemoryRuntimeError)
	require.NotContains(t, admin.MemoryRuntimeError, "sk-abcdefghijklmnop")
}

func TestMemoryStatusReportsExtractionBacklogAndRedactsDiagnostics(t *testing.T) {
	workspace := t.TempDir()
	_, router := newMemoryTestRouter(t, workspace)
	store, err := memorystore.New(memorystore.Config{ProjectRoot: workspace, Mode: "on"})
	require.NoError(t, err)
	now := time.Now().UTC().Truncate(time.Second)
	_, err = store.EnsureJob(context.Background(), memorystore.MemoryJob{
		Kind: "phase1_extract", JobKey: "api-pending", SessionID: "api-session",
		Status: memorystore.JobStatusPending, Priority: 20,
		CreatedAt: now.Add(-2 * time.Minute), AvailableAt: now.Add(-time.Minute),
	})
	require.NoError(t, err)
	_, err = store.EnsureJob(context.Background(), memorystore.MemoryJob{
		Kind: "phase1_extract", JobKey: "api-dead-letter", SessionID: "api-dead-session",
		Status: memorystore.JobStatusFailed, Attempt: 3, Priority: 10,
		CreatedAt:        now.Add(-3 * time.Minute),
		DeadLetterAt:     now.Add(-time.Minute),
		DeadLetterReason: "provider token sk-abcdefghijklmnop is unavailable",
		LastError:        "provider returned sk-abcdefghijklmnop",
	})
	require.NoError(t, err)
	require.NoError(t, store.Close())

	nonAdminReq := httptest.NewRequest(http.MethodGet, "/api/runtime/memory/status", nil)
	nonAdminRec := httptest.NewRecorder()
	router.ServeHTTP(nonAdminRec, nonAdminReq)
	require.Equal(t, http.StatusOK, nonAdminRec.Code, nonAdminRec.Body.String())
	var nonAdmin memoryStatusResponse
	require.NoError(t, json.NewDecoder(nonAdminRec.Body).Decode(&nonAdmin))
	require.Contains(t, []string{"fts5", "like_fallback"}, nonAdmin.SearchBackend)
	require.Equal(t, 1, nonAdmin.PendingExtractionJobs)
	require.Equal(t, 1, nonAdmin.PendingExtractionSessions)
	require.Equal(t, 1, nonAdmin.DeadLetterJobs)
	require.Equal(t, 1, nonAdmin.ExtractionJobCounts["pending"])
	require.Equal(t, 1, nonAdmin.ExtractionJobCounts["dead_letter"])
	require.Equal(t, 1, nonAdmin.JobsByKind["phase1_extract"]["pending"])
	require.Len(t, nonAdmin.ExtractionJobs, 2)
	for _, job := range nonAdmin.ExtractionJobs {
		require.Empty(t, job.LastError, "non-admin status must omit last_error")
		require.Empty(t, job.DeadLetterReason, "non-admin status must omit dead-letter reason")
	}
	require.NotContains(t, nonAdminRec.Body.String(), "sk-abcdefghijklmnop")

	adminReq := httptest.NewRequest(http.MethodGet, "/api/runtime/memory/status", nil)
	adminReq.Header.Set("X-Skills-Admin-Token", "memory-admin")
	adminRec := httptest.NewRecorder()
	router.ServeHTTP(adminRec, adminReq)
	require.Equal(t, http.StatusOK, adminRec.Code, adminRec.Body.String())
	require.Contains(t, adminRec.Body.String(), "sk-abcdefghijklmnop")
}

func TestMemoryAPIWhyNotRequiresAdminAndExplainsSelection(t *testing.T) {
	workspace := t.TempDir()
	handler, router := newMemoryTestRouter(t, workspace)
	cfg := runtimecfg.DefaultRuntimeConfig()
	cfg.Workspace.Root = workspace
	cfg.Memory.Mode = "on"
	cfg.Memory.Injection.TokenBudget = 300
	cfg.Memory.Injection.ExtendedPreset = true
	handler.SetRuntimeConfig(cfg, "")
	createReq := httptest.NewRequest(http.MethodPost, "/api/runtime/memory/records", strings.NewReader(`{"text":"why-not alpha record","kind":"fact"}`))
	createReq.Header.Set("Content-Type", "application/json")
	createReq.Header.Set("X-Skills-Admin-Token", "memory-admin")
	createRec := httptest.NewRecorder()
	router.ServeHTTP(createRec, createReq)
	require.Equal(t, http.StatusOK, createRec.Code, createRec.Body.String())
	var created memoryRecordDTO
	require.NoError(t, json.NewDecoder(createRec.Body).Decode(&created))

	coreStore, err := memorystore.New(memorystore.Config{ProjectRoot: workspace, Mode: "on"})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, coreStore.Close()) })
	coreCandidate, err := coreStore.CreateCoreCandidate(context.Background(), memorystore.CoreMemoryCandidateOptions{
		Content:   "Keep alpha project context stable.",
		SourceIDs: []string{created.ID},
	})
	require.NoError(t, err)
	_, err = coreStore.ActivateCore(context.Background(), coreCandidate.ID, "admin:test")
	require.NoError(t, err)

	whyNotReq := httptest.NewRequest(http.MethodGet, "/api/runtime/memory/why-not?q=alpha", nil)
	whyNotReq.Header.Set("X-Skills-Admin-Token", "memory-admin")
	whyNotRec := httptest.NewRecorder()
	router.ServeHTTP(whyNotRec, whyNotReq)
	require.Equal(t, http.StatusOK, whyNotRec.Code, whyNotRec.Body.String())
	var explanation memoryWhyNotResponse
	require.NoError(t, json.NewDecoder(whyNotRec.Body).Decode(&explanation))
	require.Equal(t, "on", explanation.Mode)
	require.Equal(t, 5000, explanation.TokenBudget, "why-not must use the effective configured injection budget")
	require.Equal(t, "alpha", explanation.QueryPreview)
	require.True(t, explanation.Injected)
	require.NotEmpty(t, explanation.Selected)
	require.NotEmpty(t, explanation.CategoryBudget)
	totalCategoryBudget := 0
	totalCategoryUsed := 0
	for _, budget := range explanation.CategoryBudget {
		totalCategoryBudget += budget
	}
	for category, used := range explanation.CategoryUsed {
		require.LessOrEqual(t, used, explanation.CategoryBudget[category])
		totalCategoryUsed += used
	}
	require.Equal(t, explanation.TokenBudget, totalCategoryBudget)
	require.LessOrEqual(t, totalCategoryUsed, explanation.TokenBudget)
	require.Equal(t, "core", explanation.Selected[0].Stage)
	require.Equal(t, coreCandidate.ID, explanation.Selected[0].ID)
	foundMemoryRecord := false
	for _, selected := range explanation.Selected {
		if selected.ID == created.ID && selected.Stage == "stable" {
			foundMemoryRecord = true
		}
	}
	require.True(t, foundMemoryRecord, "why-not should report both the reviewed core and selected source record")

	secretReq := httptest.NewRequest(http.MethodGet, "/api/runtime/memory/why-not?q="+url.QueryEscape("sk-abcdefghijklmnop"), nil)
	secretReq.Header.Set("X-Skills-Admin-Token", "memory-admin")
	secretRec := httptest.NewRecorder()
	router.ServeHTTP(secretRec, secretReq)
	require.Equal(t, http.StatusOK, secretRec.Code, secretRec.Body.String())
	var secretExplanation memoryWhyNotResponse
	require.NoError(t, json.NewDecoder(secretRec.Body).Decode(&secretExplanation))
	require.NotContains(t, secretRec.Body.String(), "sk-abcdefghijklmnop")
	require.NotContains(t, secretExplanation.QueryPreview, "sk-abcdefghijklmnop")
	require.Contains(t, secretExplanation.QueryPreview, "REDACTED")

	missingQuery := httptest.NewRequest(http.MethodGet, "/api/runtime/memory/why-not", nil)
	missingQuery.Header.Set("X-Skills-Admin-Token", "memory-admin")
	missingRec := httptest.NewRecorder()
	router.ServeHTTP(missingRec, missingQuery)
	require.Equal(t, http.StatusBadRequest, missingRec.Code, missingRec.Body.String())

	unauthorized := httptest.NewRequest(http.MethodGet, "/api/runtime/memory/why-not?q=alpha", nil)
	unauthorizedRec := httptest.NewRecorder()
	router.ServeHTTP(unauthorizedRec, unauthorized)
	require.Equal(t, http.StatusForbidden, unauthorizedRec.Code, unauthorizedRec.Body.String())
}

func TestMemoryAPIWorkspaceBindingAndAdmin(t *testing.T) {
	workspace := t.TempDir()
	other := t.TempDir()
	_, router := newMemoryTestRouter(t, workspace)

	unauthorizedCreate := httptest.NewRequest(http.MethodPost, "/api/runtime/memory/records", strings.NewReader(`{"text":"must not write"}`))
	unauthorizedCreate.Header.Set("Content-Type", "application/json")
	unauthorizedCreateRec := httptest.NewRecorder()
	router.ServeHTTP(unauthorizedCreateRec, unauthorizedCreate)
	require.Equal(t, http.StatusForbidden, unauthorizedCreateRec.Code, unauthorizedCreateRec.Body.String())

	body := `{"workspace_path":` + strconv.Quote(other) + `,"text":"must not cross workspace"}`
	req := httptest.NewRequest(http.MethodPost, "/api/runtime/memory/records", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Skills-Admin-Token", "memory-admin")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var created memoryRecordDTO
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&created))

	otherStore, err := memorystore.New(memorystore.Config{ProjectRoot: other, Mode: "on"})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, otherStore.Close()) })
	_, err = otherStore.GetRecord(context.Background(), created.ID)
	require.NoError(t, err)

	configuredStore, err := memorystore.New(memorystore.Config{ProjectRoot: workspace, Mode: "on"})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, configuredStore.Close()) })
	configuredPage, err := configuredStore.ListRecords(context.Background(), memorystore.RecordFilter{Limit: 10})
	require.NoError(t, err)
	for _, item := range configuredPage.Items {
		require.NotEqual(t, created.ID, item.ID)
	}

	missing := httptest.NewRequest(http.MethodDelete, "/api/runtime/memory/records/missing", nil)
	missingRec := httptest.NewRecorder()
	router.ServeHTTP(missingRec, missing)
	require.Equal(t, http.StatusForbidden, missingRec.Code, missingRec.Body.String())

	query := url.Values{"workspace_path": []string{other}}
	statusReq := httptest.NewRequest(http.MethodGet, "/api/runtime/memory/status?"+query.Encode(), nil)
	statusRec := httptest.NewRecorder()
	router.ServeHTTP(statusRec, statusReq)
	require.Equal(t, http.StatusBadRequest, statusRec.Code, statusRec.Body.String())

	for _, path := range []string{
		"/api/runtime/memory/records?scope=user",
		"/api/runtime/memory/records?status=bogus",
		"/api/runtime/memory/records?cursor=not-a-number",
	} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		require.Equal(t, http.StatusBadRequest, rec.Code, path+": "+rec.Body.String())
	}
}

func TestMemoryAPIStatusOffDoesNotCreateDB(t *testing.T) {
	workspace := t.TempDir()
	handler, router := newHarnessTestRouter(t)
	cfg := runtimecfg.DefaultRuntimeConfig()
	cfg.Workspace.Root = workspace
	cfg.Memory.Mode = "off"
	handler.SetRuntimeConfig(cfg, "")
	handler.SetAdminToken("memory-admin")

	req := httptest.NewRequest(http.MethodGet, "/api/runtime/memory/status", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	for _, path := range []string{
		"/api/runtime/memory/jobs",
		"/api/runtime/memory/summaries",
		"/api/runtime/memory/usage",
		"/api/runtime/memory/metrics",
		"/api/runtime/memory/audit",
		"/api/runtime/memory/export",
	} {
		offReq := httptest.NewRequest(http.MethodGet, path, nil)
		offReq.Header.Set("X-Skills-Admin-Token", "memory-admin")
		offRec := httptest.NewRecorder()
		router.ServeHTTP(offRec, offReq)
		require.Equal(t, http.StatusOK, offRec.Code, path+": "+offRec.Body.String())
	}
	for _, path := range []string{
		"/api/runtime/memory/jobs/missing/retry",
		"/api/runtime/memory/jobs/missing/dead-letter",
		"/api/runtime/memory/jobs/deadline-repair",
	} {
		offReq := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{"apply":true}`))
		offReq.Header.Set("X-Skills-Admin-Token", "memory-admin")
		offRec := httptest.NewRecorder()
		router.ServeHTTP(offRec, offReq)
		require.Equal(t, http.StatusConflict, offRec.Code, path+": "+offRec.Body.String())
	}
	importReq := httptest.NewRequest(http.MethodPost, "/api/runtime/memory/import", strings.NewReader(`{"type":"record","record":{"text":"must not create db"}}`))
	importReq.Header.Set("X-Skills-Admin-Token", "memory-admin")
	importRec := httptest.NewRecorder()
	router.ServeHTTP(importRec, importReq)
	require.Equal(t, http.StatusConflict, importRec.Code, importRec.Body.String())
	if _, err := os.Stat(filepath.Join(workspace, ".aicli", "memory", "memory.db")); !os.IsNotExist(err) {
		t.Fatalf("off status must not create memory.db, stat err=%v", err)
	}
}

func TestMemoryAPIDigestReplayPreviewRequiresAdminAndUsesBoundRuntime(t *testing.T) {
	workspace := t.TempDir()
	handler, router := newMemoryTestRouter(t, workspace)

	unauthorized := httptest.NewRequest(
		http.MethodGet, "/api/runtime/memory/digests/replay-preview", nil,
	)
	unauthorizedRec := httptest.NewRecorder()
	router.ServeHTTP(unauthorizedRec, unauthorized)
	require.Equal(t, http.StatusForbidden, unauthorizedRec.Code, unauthorizedRec.Body.String())

	unbound := httptest.NewRequest(
		http.MethodGet, "/api/runtime/memory/digests/replay-preview", nil,
	)
	unbound.Header.Set("X-Skills-Admin-Token", "memory-admin")
	unboundRec := httptest.NewRecorder()
	router.ServeHTTP(unboundRec, unbound)
	require.Equal(t, http.StatusServiceUnavailable, unboundRec.Code, unboundRec.Body.String())
	require.Contains(t, unboundRec.Body.String(), "memory runtime unavailable")
	require.NotContains(t, unboundRec.Body.String(), "sqlite")

	store, err := memorystore.New(memorystore.Config{ProjectRoot: workspace, Mode: "on"})
	require.NoError(t, err)
	_, err = store.RecordTurnDigest(context.Background(), memorystore.TurnDigest{
		ProjectKey:        store.ProjectKey(),
		SessionID:         "recovery-api-session",
		SessionGeneration: 1,
		TurnID:            "recovery-api-turn",
		Status:            memorystore.TurnDigestPending,
		CreatedAt:         time.Now().UTC(),
	})
	require.NoError(t, err)
	runtime := memoryruntime.New(store, nil, nil, nil, memoryruntime.Config{
		Mode: "on", Workspace: workspace, ProjectKey: store.ProjectKey(),
	})
	handler.SetMemoryRuntime(runtime)
	t.Cleanup(func() { _ = handler.CloseMemoryRuntime() })

	req := httptest.NewRequest(
		http.MethodGet, "/api/runtime/memory/digests/replay-preview?limit=999", nil,
	)
	req.Header.Set("X-Skills-Admin-Token", "memory-admin")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	var report memoryruntime.DigestReplayReport
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&report))
	require.Equal(t, 500, report.Limit, "API must preserve the bounded recovery limit")
	require.Equal(t, 1, report.Count)
	require.Equal(t, 1, report.ByClass[memorystore.DigestReplayMissingReceipt])
	require.Len(t, report.Items, 1)
	require.True(t, report.Items[0].DryRun)
	require.NotEmpty(t, report.GeneratedAt)
	require.NotContains(t, rec.Body.String(), "provider")
}

func TestMemoryAPIDigestReplayPreviewRedactsStoreDiagnostics(t *testing.T) {
	workspace := t.TempDir()
	handler, router := newMemoryTestRouter(t, workspace)
	store, err := memorystore.New(memorystore.Config{ProjectRoot: workspace, Mode: "on"})
	require.NoError(t, err)
	runtime := memoryruntime.New(store, nil, nil, nil, memoryruntime.Config{
		Mode: "on", Workspace: workspace, ProjectKey: store.ProjectKey(),
	})
	handler.SetMemoryRuntime(runtime)
	t.Cleanup(func() { _ = handler.CloseMemoryRuntime() })
	require.NoError(t, store.Close())

	req := httptest.NewRequest(
		http.MethodGet, "/api/runtime/memory/digests/replay-preview", nil,
	)
	req.Header.Set("X-Skills-Admin-Token", "memory-admin")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusServiceUnavailable, rec.Code, rec.Body.String())
	require.Contains(t, rec.Body.String(), "memory recovery report unavailable")
	require.NotContains(t, rec.Body.String(), "database is closed")
	require.NotContains(t, rec.Body.String(), "sqlite")
}

func TestMemoryAPISessionModeAndRecallTraces(t *testing.T) {
	workspace := t.TempDir()
	_, router := newMemoryTestRouter(t, workspace)

	modeReq := httptest.NewRequest(http.MethodPost, "/api/runtime/memory/sessions/s1/mode", strings.NewReader(`{"mode":"disabled"}`))
	modeReq.Header.Set("Content-Type", "application/json")
	modeReq.Header.Set("X-Skills-Admin-Token", "memory-admin")
	modeRec := httptest.NewRecorder()
	router.ServeHTTP(modeRec, modeReq)
	require.Equal(t, http.StatusOK, modeRec.Code, modeRec.Body.String())

	ctxReq := httptest.NewRequest(http.MethodGet, "/api/runtime/memory/sessions/s1/context", nil)
	ctxRec := httptest.NewRecorder()
	router.ServeHTTP(ctxRec, ctxReq)
	require.Equal(t, http.StatusOK, ctxRec.Code, ctxRec.Body.String())

	traceReq := httptest.NewRequest(http.MethodGet, "/api/runtime/memory/recall-traces", nil)
	traceReq.Header.Set("X-Skills-Admin-Token", "memory-admin")
	traceRec := httptest.NewRecorder()
	router.ServeHTTP(traceRec, traceReq)
	require.Equal(t, http.StatusOK, traceRec.Code, traceRec.Body.String())
}

type apiMemorySource struct {
	session memoryruntime.SessionRef
	tx      memoryruntime.Transcript
}

func (s *apiMemorySource) ListSessions(context.Context, string, int) ([]memoryruntime.SessionRef, error) {
	return []memoryruntime.SessionRef{s.session}, nil
}

func (s *apiMemorySource) ReadTranscript(context.Context, memoryruntime.SessionRef, int) (memoryruntime.Transcript, error) {
	return s.tx, nil
}

type apiMemoryExtractor struct{}

func (apiMemoryExtractor) Extract(context.Context, memoryruntime.SessionRef, memoryruntime.Transcript) (memoryruntime.ExtractionResult, error) {
	return memoryruntime.ExtractionResult{Candidates: []memoryruntime.Candidate{{
		Kind: "decision", Text: "Use append-only memory writes.", Confidence: 0.9,
		SourceMessageIDs: []string{"msg-1"},
	}}}, nil
}

func TestMemoryAPIManualExtract(t *testing.T) {
	workspace := t.TempDir()
	handler, router := newMemoryTestRouter(t, workspace)
	store, err := memorystore.New(memorystore.Config{ProjectRoot: workspace, Mode: "shadow"})
	if err != nil {
		t.Fatalf("memorystore.New: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	now := time.Now().UTC()
	source := &apiMemorySource{
		session: memoryruntime.SessionRef{ID: "s1", WorkspacePath: workspace, LastActivityAt: now},
		tx: memoryruntime.Transcript{
			SessionID: "s1", Hash: "hash-api", Revision: 1,
			Messages: []memoryruntime.TranscriptMessage{{ID: "msg-1", Seq: 1, Role: "user", Content: "remember this"}},
		},
	}
	rt := memoryruntime.New(store, source, apiMemoryExtractor{}, store, memoryruntime.Config{
		Mode: "shadow", Workspace: workspace, ProjectKey: store.ProjectKey(),
		MaxSessionsPerRun: 2, MaxSourceMessages: 10,
	})
	handler.SetMemoryRuntime(rt)

	req := httptest.NewRequest(http.MethodPost, "/api/runtime/memory/extract", strings.NewReader(`{"session_id":"s1","trigger":"manual"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Skills-Admin-Token", "memory-admin")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Contains(t, rec.Body.String(), `"candidates":1`)

	jobsReq := httptest.NewRequest(http.MethodGet, "/api/runtime/memory/jobs", nil)
	jobsReq.Header.Set("X-Skills-Admin-Token", "memory-admin")
	jobsRec := httptest.NewRecorder()
	router.ServeHTTP(jobsRec, jobsReq)
	require.Equal(t, http.StatusOK, jobsRec.Code, jobsRec.Body.String())
	require.Contains(t, jobsRec.Body.String(), "phase1_extract")
}

func TestMemoryAPIJobOperatorsRequireAdminAndRedactReason(t *testing.T) {
	workspace := t.TempDir()
	_, router := newMemoryTestRouter(t, workspace)
	store, err := memorystore.New(memorystore.Config{ProjectRoot: workspace, Mode: "on"})
	require.NoError(t, err)
	failed, err := store.EnsureJob(context.Background(), memorystore.MemoryJob{
		Kind: "phase1_extract", JobKey: "api-operator-retry",
		Status: memorystore.JobStatusFailed, LastError: "provider failed",
	})
	require.NoError(t, err)
	pending, err := store.EnsureJob(context.Background(), memorystore.MemoryJob{
		Kind: "phase1_extract", JobKey: "api-operator-dead-letter",
		Status: memorystore.JobStatusPending,
	})
	require.NoError(t, err)
	require.NoError(t, store.Close())

	for _, path := range []string{
		"/api/runtime/memory/jobs/" + failed.ID + "/retry",
		"/api/runtime/memory/jobs/" + pending.ID + "/dead-letter",
	} {
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{"reason":"unauthorized"}`))
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		require.Equal(t, http.StatusForbidden, rec.Code, path+": "+rec.Body.String())
	}

	retryReq := httptest.NewRequest(http.MethodPost,
		"/api/runtime/memory/jobs/"+failed.ID+"/retry", strings.NewReader(`{"reason":"operator retry"}`))
	retryReq.Header.Set("X-Skills-Admin-Token", "memory-admin")
	retryRec := httptest.NewRecorder()
	router.ServeHTTP(retryRec, retryReq)
	require.Equal(t, http.StatusOK, retryRec.Code, retryRec.Body.String())

	secret := "sk-abcdefghijklmnop"
	body, err := json.Marshal(memoryJobOperatorRequest{Reason: "operator decision " + secret})
	require.NoError(t, err)
	deadLetterReq := httptest.NewRequest(http.MethodPost,
		"/api/runtime/memory/jobs/"+pending.ID+"/dead-letter", strings.NewReader(string(body)))
	deadLetterReq.Header.Set("X-Skills-Admin-Token", "memory-admin")
	deadLetterRec := httptest.NewRecorder()
	router.ServeHTTP(deadLetterRec, deadLetterReq)
	require.Equal(t, http.StatusOK, deadLetterRec.Code, deadLetterRec.Body.String())

	listReq := httptest.NewRequest(http.MethodGet,
		"/api/runtime/memory/jobs?status=dead_letter", nil)
	listReq.Header.Set("X-Skills-Admin-Token", "memory-admin")
	listRec := httptest.NewRecorder()
	router.ServeHTTP(listRec, listReq)
	require.Equal(t, http.StatusOK, listRec.Code, listRec.Body.String())
	require.NotContains(t, listRec.Body.String(), secret)
	require.Contains(t, listRec.Body.String(), "REDACTED")
}

func TestMemoryAPIJobDeadlineRepairIsBoundedExplicitAndLeaseFenced(t *testing.T) {
	workspace := t.TempDir()
	_, router := newMemoryTestRouter(t, workspace)

	unauthorized := httptest.NewRecorder()
	router.ServeHTTP(unauthorized, httptest.NewRequest(
		http.MethodPost, "/api/runtime/memory/jobs/deadline-repair", strings.NewReader(`{}`),
	))
	require.Equal(t, http.StatusForbidden, unauthorized.Code)

	preview := httptest.NewRequest(
		http.MethodPost, "/api/runtime/memory/jobs/deadline-repair",
		strings.NewReader(`{"limit":999,"reason":"operator preview"}`),
	)
	preview.Header.Set("X-Skills-Admin-Token", "memory-admin")
	previewRec := httptest.NewRecorder()
	router.ServeHTTP(previewRec, preview)
	require.Equal(t, http.StatusOK, previewRec.Code, previewRec.Body.String())
	var previewResult memorystore.JobDeadlineRepairResult
	require.NoError(t, json.NewDecoder(previewRec.Body).Decode(&previewResult))
	require.True(t, previewResult.DryRun)
	require.Equal(t, 0, previewResult.Updated)

	apply := httptest.NewRequest(
		http.MethodPost, "/api/runtime/memory/jobs/deadline-repair",
		strings.NewReader(`{"apply":true,"limit":999,"reason":"operator apply sk-abcdefghijklmnop"}`),
	)
	apply.Header.Set("X-Skills-Admin-Token", "memory-admin")
	applyRec := httptest.NewRecorder()
	router.ServeHTTP(applyRec, apply)
	require.Equal(t, http.StatusOK, applyRec.Code, applyRec.Body.String())
	var applyResult memorystore.JobDeadlineRepairResult
	require.NoError(t, json.NewDecoder(applyRec.Body).Decode(&applyResult))
	require.False(t, applyResult.DryRun)
	require.Equal(t, 0, applyResult.Updated)
	require.NotContains(t, applyRec.Body.String(), "sk-abcdefghijklmnop")

	store, err := memorystore.New(memorystore.Config{ProjectRoot: workspace, Mode: "on"})
	require.NoError(t, err)
	lease, acquired, err := store.AcquireMaintenanceLease(
		context.Background(), "test-deadline-owner", "test", time.Minute,
	)
	require.NoError(t, err)
	require.True(t, acquired)
	t.Cleanup(func() {
		_ = store.ReleaseMaintenanceLease(context.Background(), lease)
		_ = store.Close()
	})
	busy := httptest.NewRequest(
		http.MethodPost, "/api/runtime/memory/jobs/deadline-repair",
		strings.NewReader(`{"apply":true}`),
	)
	busy.Header.Set("X-Skills-Admin-Token", "memory-admin")
	busyRec := httptest.NewRecorder()
	router.ServeHTTP(busyRec, busy)
	require.Equal(t, http.StatusConflict, busyRec.Code, busyRec.Body.String())
	require.NotContains(t, busyRec.Body.String(), "test-deadline-owner")
}

func TestMemoryAPIJobSummaryRequiresAdminAndDoesNotLeakDiagnostics(t *testing.T) {
	workspace := t.TempDir()
	_, router := newMemoryTestRouter(t, workspace)
	store, err := memorystore.New(memorystore.Config{ProjectRoot: workspace, Mode: "on"})
	require.NoError(t, err)
	job, err := store.EnsureJob(context.Background(), memorystore.MemoryJob{
		Kind: "phase1_extract", JobKey: "private-job-key",
		Status: memorystore.JobStatusFailed, Attempt: 1, MaxAttempts: 3,
		LastError: "provider diagnostic with sk-abcdefghijklmnop",
	})
	require.NoError(t, err)
	require.NoError(t, store.Close())
	require.NotEmpty(t, job.ID)

	path := "/api/runtime/memory/jobs/summary"
	unauthorized := httptest.NewRecorder()
	router.ServeHTTP(unauthorized, httptest.NewRequest(http.MethodGet, path, nil))
	require.Equal(t, http.StatusForbidden, unauthorized.Code)

	request := httptest.NewRequest(http.MethodGet, path, nil)
	request.Header.Set("X-Skills-Admin-Token", "memory-admin")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	var summary memorystore.JobOperatorSummary
	require.NoError(t, json.NewDecoder(response.Body).Decode(&summary))
	require.Equal(t, 1, summary.TotalJobs)
	require.Equal(t, 1, summary.RetryableJobs)
	require.Equal(t, 1, summary.ByKind.Counts["phase1_extract"])
	for _, forbidden := range []string{"private-job-key", "provider diagnostic", "sk-abcdefghijklmnop", job.ID} {
		require.NotContains(t, response.Body.String(), forbidden)
	}
}

func TestMemoryAPIJobSummaryModeOffDoesNotCreateDatabase(t *testing.T) {
	workspace := t.TempDir()
	handler, router := newMemoryTestRouter(t, workspace)
	cfg := runtimecfg.DefaultRuntimeConfig()
	cfg.Workspace.Root = workspace
	cfg.Memory.Mode = "off"
	handler.SetRuntimeConfig(cfg, "")

	request := httptest.NewRequest(http.MethodGet, "/api/runtime/memory/jobs/summary", nil)
	request.Header.Set("X-Skills-Admin-Token", "memory-admin")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	var summary memorystore.JobOperatorSummary
	require.NoError(t, json.NewDecoder(response.Body).Decode(&summary))
	require.Zero(t, summary.TotalJobs)

	_, err := os.Stat(filepath.Join(workspace, ".aicli", "memory", "memory.db"))
	require.ErrorIs(t, err, os.ErrNotExist)
}

func TestMemoryAPIConsolidateSummariesAndUsage(t *testing.T) {
	workspace := t.TempDir()
	_, router := newMemoryTestRouter(t, workspace)
	store, err := memorystore.New(memorystore.Config{ProjectRoot: workspace, Mode: "on"})
	require.NoError(t, err)
	candidate, err := store.InsertCandidate(context.Background(), memorystore.Candidate{
		SessionID: "s1", Kind: "decision", Text: "Use append-only memory writes.",
		Confidence: 0.9, SourceHash: "hash-api", SourceRevision: 1,
	})
	require.NoError(t, err)

	consolidateReq := httptest.NewRequest(http.MethodPost, "/api/runtime/memory/consolidate", strings.NewReader(`{}`))
	consolidateReq.Header.Set("Content-Type", "application/json")
	consolidateReq.Header.Set("X-Skills-Admin-Token", "memory-admin")
	consolidateRec := httptest.NewRecorder()
	router.ServeHTTP(consolidateRec, consolidateReq)
	require.Equal(t, http.StatusOK, consolidateRec.Code, consolidateRec.Body.String())
	require.Contains(t, consolidateRec.Body.String(), `"revision":1`)

	summariesReq := httptest.NewRequest(http.MethodGet, "/api/runtime/memory/summaries", nil)
	summariesRec := httptest.NewRecorder()
	router.ServeHTTP(summariesRec, summariesReq)
	require.Equal(t, http.StatusOK, summariesRec.Code, summariesRec.Body.String())
	var summaries memorySummaryListResponse
	require.NoError(t, json.NewDecoder(summariesRec.Body).Decode(&summaries))
	require.Equal(t, 1, summaries.Count)
	require.Equal(t, int64(1), summaries.Items[0].Revision)

	_, err = store.RecordExposure(context.Background(), memorystore.Exposure{
		MemoryIDs: []string{candidate.ID}, SessionID: "s1", TurnID: "turn1",
		UseKind: memorystore.UsageInjectedOnly,
	})
	require.NoError(t, err)
	usageReq := httptest.NewRequest(http.MethodGet, "/api/runtime/memory/usage?memory_id="+candidate.ID, nil)
	usageReq.Header.Set("X-Skills-Admin-Token", "memory-admin")
	usageRec := httptest.NewRecorder()
	router.ServeHTTP(usageRec, usageReq)
	require.Equal(t, http.StatusOK, usageRec.Code, usageRec.Body.String())
	var usage memoryUsageListResponse
	require.NoError(t, json.NewDecoder(usageRec.Body).Decode(&usage))
	require.Equal(t, 1, usage.Count)
	require.Equal(t, memorystore.UsageInjectedOnly, usage.Items[0].UseKind)

	corrected := "Use revisioned memory writes."
	_, err = store.UpdateRecord(context.Background(), candidate.ID, memorystore.RecordPatch{Text: &corrected})
	require.NoError(t, err)
	rebuildReq := httptest.NewRequest(http.MethodPost, "/api/runtime/memory/summaries/rebuild", strings.NewReader(`{}`))
	rebuildReq.Header.Set("Content-Type", "application/json")
	rebuildReq.Header.Set("X-Skills-Admin-Token", "memory-admin")
	rebuildRec := httptest.NewRecorder()
	router.ServeHTTP(rebuildRec, rebuildReq)
	require.Equal(t, http.StatusOK, rebuildRec.Code, rebuildRec.Body.String())
	require.Contains(t, rebuildRec.Body.String(), `"rebuilt":1`)

	require.NoError(t, store.Close())
}

func TestMemoryAPIGovernanceEndpoints(t *testing.T) {
	workspace := t.TempDir()
	_, router := newMemoryTestRouter(t, workspace)
	createBody := `{"text":"governance endpoint memory","kind":"fact"}`
	createReq := httptest.NewRequest(http.MethodPost, "/api/runtime/memory/records", strings.NewReader(createBody))
	createReq.Header.Set("Content-Type", "application/json")
	createReq.Header.Set("X-Skills-Admin-Token", "memory-admin")
	createRec := httptest.NewRecorder()
	router.ServeHTTP(createRec, createReq)
	require.Equal(t, http.StatusOK, createRec.Code, createRec.Body.String())
	var created memoryRecordDTO
	require.NoError(t, json.NewDecoder(createRec.Body).Decode(&created))

	revalidateReq := httptest.NewRequest(http.MethodPost, "/api/runtime/memory/records/"+created.ID+"/revalidate", nil)
	revalidateReq.Header.Set("X-Skills-Admin-Token", "memory-admin")
	revalidateRec := httptest.NewRecorder()
	router.ServeHTTP(revalidateRec, revalidateReq)
	require.Equal(t, http.StatusOK, revalidateRec.Code, revalidateRec.Body.String())

	deleteReq := httptest.NewRequest(http.MethodDelete, "/api/runtime/memory/records/"+created.ID, nil)
	deleteReq.Header.Set("X-Skills-Admin-Token", "memory-admin")
	deleteRec := httptest.NewRecorder()
	router.ServeHTTP(deleteRec, deleteReq)
	require.Equal(t, http.StatusOK, deleteRec.Code, deleteRec.Body.String())
	restoreReq := httptest.NewRequest(http.MethodPost, "/api/runtime/memory/records/"+created.ID+"/restore", nil)
	restoreReq.Header.Set("X-Skills-Admin-Token", "memory-admin")
	restoreRec := httptest.NewRecorder()
	router.ServeHTTP(restoreRec, restoreReq)
	require.Equal(t, http.StatusOK, restoreRec.Code, restoreRec.Body.String())

	exportReq := httptest.NewRequest(http.MethodGet, "/api/runtime/memory/export?format=jsonl", nil)
	exportReq.Header.Set("X-Skills-Admin-Token", "memory-admin")
	exportRec := httptest.NewRecorder()
	router.ServeHTTP(exportRec, exportReq)
	require.Equal(t, http.StatusOK, exportRec.Code, exportRec.Body.String())
	require.Contains(t, exportRec.Body.String(), created.ID)

	auditReq := httptest.NewRequest(http.MethodGet, "/api/runtime/memory/audit", nil)
	auditReq.Header.Set("X-Skills-Admin-Token", "memory-admin")
	auditRec := httptest.NewRecorder()
	router.ServeHTTP(auditRec, auditReq)
	require.Equal(t, http.StatusOK, auditRec.Code, auditRec.Body.String())
	require.Contains(t, auditRec.Body.String(), "revalidated")

	freshnessReq := httptest.NewRequest(http.MethodGet, "/api/runtime/memory/freshness", nil)
	freshnessRec := httptest.NewRecorder()
	router.ServeHTTP(freshnessRec, freshnessReq)
	require.Equal(t, http.StatusOK, freshnessRec.Code, freshnessRec.Body.String())

	store, err := memorystore.New(memorystore.Config{ProjectRoot: workspace, Mode: "on", InjectionMode: "summary"})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	derived, err := store.InsertCandidate(context.Background(), memorystore.Candidate{
		SessionID: "s-api", Kind: "decision", Text: "api summary input",
		Confidence: 0.9, SourceHash: "api-summary", SourceRevision: 1,
	})
	require.NoError(t, err)
	active := memorystore.StatusActive
	_, err = store.UpdateRecord(context.Background(), derived.ID, memorystore.RecordPatch{Status: &active})
	require.NoError(t, err)
	summary, err := store.CompileSummary(context.Background(), memorystore.Summary{Text: "api summary"}, []string{derived.ID})
	require.NoError(t, err)
	activateReq := httptest.NewRequest(http.MethodPost, "/api/runtime/memory/summaries/"+summary.ID+"/activate", nil)
	activateReq.Header.Set("X-Skills-Admin-Token", "memory-admin")
	activateRec := httptest.NewRecorder()
	router.ServeHTTP(activateRec, activateReq)
	require.Equal(t, http.StatusOK, activateRec.Code, activateRec.Body.String())

	unauthorizedExport := httptest.NewRequest(http.MethodGet, "/api/runtime/memory/export", nil)
	unauthorizedExport.RemoteAddr = "192.0.2.1:1234"
	unauthorizedRec := httptest.NewRecorder()
	router.ServeHTTP(unauthorizedRec, unauthorizedExport)
	require.Equal(t, http.StatusForbidden, unauthorizedRec.Code, unauthorizedRec.Body.String())

	resetWithoutConfirm := httptest.NewRequest(http.MethodPost, "/api/runtime/memory/reset", strings.NewReader(`{}`))
	resetWithoutConfirm.Header.Set("Content-Type", "application/json")
	resetWithoutConfirm.Header.Set("X-Skills-Admin-Token", "memory-admin")
	resetRec := httptest.NewRecorder()
	router.ServeHTTP(resetRec, resetWithoutConfirm)
	require.Equal(t, http.StatusBadRequest, resetRec.Code, resetRec.Body.String())
	resetReq := httptest.NewRequest(http.MethodPost, "/api/runtime/memory/reset", strings.NewReader(`{"confirm":true}`))
	resetReq.Header.Set("Content-Type", "application/json")
	resetReq.Header.Set("X-Skills-Admin-Token", "memory-admin")
	resetRec = httptest.NewRecorder()
	router.ServeHTTP(resetRec, resetReq)
	require.Equal(t, http.StatusOK, resetRec.Code, resetRec.Body.String())
}

func TestMemoryAPIImportExportRoundTrip(t *testing.T) {
	workspace := t.TempDir()
	_, router := newMemoryTestRouter(t, workspace)
	store, err := memorystore.New(memorystore.Config{ProjectRoot: workspace, Mode: "on", InjectionMode: "summary"})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	manual, err := store.Append(memorystore.AppendNoteOptions{Text: "round-trip manual memory", Kind: "reference"})
	require.NoError(t, err)
	derived, err := store.InsertCandidate(context.Background(), memorystore.Candidate{
		SessionID: "s-import", Kind: "decision", Text: "round-trip derived decision",
		Confidence: 0.9, SourceHash: "import-source", SourceRevision: 1,
	})
	require.NoError(t, err)
	active := memorystore.StatusActive
	_, err = store.UpdateRecord(context.Background(), derived.ID, memorystore.RecordPatch{Status: &active})
	require.NoError(t, err)
	_, err = store.CompileSummary(context.Background(), memorystore.Summary{Text: "round-trip summary"}, []string{derived.ID})
	require.NoError(t, err)
	body, err := store.ExportJSONL(context.Background())
	require.NoError(t, err)
	require.Contains(t, body, manual.ID)
	require.Contains(t, body, derived.ID)

	resetReq := httptest.NewRequest(http.MethodPost, "/api/runtime/memory/reset", strings.NewReader(`{"confirm":true}`))
	resetReq.Header.Set("Content-Type", "application/json")
	resetReq.Header.Set("X-Skills-Admin-Token", "memory-admin")
	resetRec := httptest.NewRecorder()
	router.ServeHTTP(resetRec, resetReq)
	require.Equal(t, http.StatusOK, resetRec.Code, resetRec.Body.String())

	dryReq := httptest.NewRequest(http.MethodPost, "/api/runtime/memory/import?dry_run=true", strings.NewReader(body))
	dryReq.Header.Set("Content-Type", "application/jsonl")
	dryReq.Header.Set("X-Skills-Admin-Token", "memory-admin")
	dryRec := httptest.NewRecorder()
	router.ServeHTTP(dryRec, dryReq)
	require.Equal(t, http.StatusOK, dryRec.Code, dryRec.Body.String())
	var dryResult memorystore.ImportResult
	require.NoError(t, json.NewDecoder(dryRec.Body).Decode(&dryResult))
	require.True(t, dryResult.DryRun)
	require.Equal(t, 2, dryResult.Imported)
	listReq := httptest.NewRequest(http.MethodGet, "/api/runtime/memory/records?status=active&limit=10", nil)
	listRec := httptest.NewRecorder()
	router.ServeHTTP(listRec, listReq)
	var listed memoryRecordListResponse
	require.NoError(t, json.NewDecoder(listRec.Body).Decode(&listed))
	require.Equal(t, 0, listed.Count)

	importReq := httptest.NewRequest(http.MethodPost, "/api/runtime/memory/import", strings.NewReader(body))
	importReq.Header.Set("Content-Type", "application/jsonl")
	importReq.Header.Set("X-Skills-Admin-Token", "memory-admin")
	importRec := httptest.NewRecorder()
	router.ServeHTTP(importRec, importReq)
	require.Equal(t, http.StatusOK, importRec.Code, importRec.Body.String())
	var imported memorystore.ImportResult
	require.NoError(t, json.NewDecoder(importRec.Body).Decode(&imported))
	require.Equal(t, 2, imported.Imported)
	require.Equal(t, 1, imported.Summaries)
	require.Zero(t, imported.Corrupt)

	listReq = httptest.NewRequest(http.MethodGet, "/api/runtime/memory/records?status=active&limit=10", nil)
	listRec = httptest.NewRecorder()
	router.ServeHTTP(listRec, listReq)
	require.NoError(t, json.NewDecoder(listRec.Body).Decode(&listed))
	require.Equal(t, 2, listed.Count)

	unauthorized := httptest.NewRequest(http.MethodPost, "/api/runtime/memory/import", strings.NewReader(body))
	unauthorized.RemoteAddr = "192.0.2.1:1234"
	unauthorizedRec := httptest.NewRecorder()
	router.ServeHTTP(unauthorizedRec, unauthorized)
	require.Equal(t, http.StatusForbidden, unauthorizedRec.Code, unauthorizedRec.Body.String())
}

func TestMemoryAPIPurgeTurnWindows(t *testing.T) {
	workspace := t.TempDir()
	_, router := newMemoryTestRouter(t, workspace)
	store, err := memorystore.New(memorystore.Config{ProjectRoot: workspace, Mode: "on"})
	require.NoError(t, err)
	ctx := context.Background()
	createRolledUp := func(sessionID, turnID string) {
		t.Helper()
		digest, err := store.RecordTurnDigest(ctx, memorystore.TurnDigest{
			SessionID: sessionID, TurnID: turnID, Outcome: "success", MessageSeqEnd: 2,
		})
		require.NoError(t, err)
		_, err = store.MarkTurnDigestsProcessed(ctx, sessionID, digest.DigestSeq, "")
		require.NoError(t, err)
		window, _, err := store.EnsureTurnWindow(ctx, memorystore.TurnWindow{
			ProjectKey: store.ProjectKey(), SessionID: sessionID, WindowIndex: 1,
			StartDigestSeq: digest.DigestSeq, EndDigestSeq: digest.DigestSeq,
			StartMessageSeq: 1, EndMessageSeq: 2, TurnCount: 1,
			SourceHash: "hash-" + sessionID, SourceRevision: digest.DigestSeq,
			Status: memorystore.TurnWindowPending,
		})
		require.NoError(t, err)
		window, err = store.CompleteTurnWindow(ctx, window.ID, memorystore.TurnWindowSummary{Text: "summary " + sessionID})
		require.NoError(t, err)
		_, err = store.MarkTurnDigestsRolledUp(ctx, sessionID, digest.DigestSeq, digest.DigestSeq, window.ID)
		require.NoError(t, err)
	}
	createRolledUp("s1", "turn-1")
	createRolledUp("s2", "turn-2")
	require.NoError(t, store.Close())

	post := func(body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/api/runtime/memory/turn-windows/purge", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Skills-Admin-Token", "memory-admin")
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		return rec
	}
	require.Equal(t, http.StatusBadRequest, post(`{"confirm":true}`).Code)
	require.Equal(t, http.StatusBadRequest, post(`{"confirm":true,"all":true,"session_id":"s1"}`).Code)

	sessionRec := post(`{"confirm":true,"session_id":"s1"}`)
	require.Equal(t, http.StatusOK, sessionRec.Code, sessionRec.Body.String())
	var sessionResp struct {
		Scope           string `json:"scope"`
		WindowsDeleted  int64  `json:"windows_deleted"`
		DigestsReleased int64  `json:"digests_released"`
	}
	require.NoError(t, json.NewDecoder(sessionRec.Body).Decode(&sessionResp))
	require.Equal(t, "session", sessionResp.Scope)
	require.EqualValues(t, 1, sessionResp.WindowsDeleted)
	require.EqualValues(t, 1, sessionResp.DigestsReleased)

	allRec := post(`{"confirm":true,"all":true}`)
	require.Equal(t, http.StatusOK, allRec.Code, allRec.Body.String())
	var allResp struct {
		Scope           string `json:"scope"`
		WindowsDeleted  int64  `json:"windows_deleted"`
		DigestsReleased int64  `json:"digests_released"`
	}
	require.NoError(t, json.NewDecoder(allRec.Body).Decode(&allResp))
	require.Equal(t, "project", allResp.Scope)
	require.EqualValues(t, 1, allResp.WindowsDeleted)
	require.EqualValues(t, 1, allResp.DigestsReleased)

	reopened, err := memorystore.New(memorystore.Config{ProjectRoot: workspace, Mode: "on"})
	require.NoError(t, err)
	t.Cleanup(func() { _ = reopened.Close() })
	status, err := reopened.Status(ctx)
	require.NoError(t, err)
	require.Empty(t, status.TurnWindows)
	for _, sessionID := range []string{"s1", "s2"} {
		digest, err := reopened.GetTurnDigestByTurn(ctx, sessionID, "turn-"+sessionID[len(sessionID)-1:])
		require.NoError(t, err)
		require.Equal(t, memorystore.TurnDigestRollupSkipped, digest.RollupStatus)
	}
}

func TestMemoryAPIPrivateRecordRequiresAdmin(t *testing.T) {
	workspace := t.TempDir()
	_, router := newMemoryTestRouter(t, workspace)
	store, err := memorystore.New(memorystore.Config{ProjectRoot: workspace, Mode: "on"})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	private, err := store.InsertCandidate(context.Background(), memorystore.Candidate{
		SessionID: "s-private", Kind: "fact", Text: "private api record",
		Confidence: 0.9, Sensitivity: "private", SourceHash: "api-private", SourceRevision: 1,
	})
	require.NoError(t, err)
	active := memorystore.StatusActive
	_, err = store.UpdateRecord(context.Background(), private.ID, memorystore.RecordPatch{Status: &active})
	require.NoError(t, err)

	listReq := httptest.NewRequest(http.MethodGet, "/api/runtime/memory/records?status=active&limit=10", nil)
	listRec := httptest.NewRecorder()
	router.ServeHTTP(listRec, listReq)
	require.Equal(t, http.StatusOK, listRec.Code, listRec.Body.String())
	var listed memoryRecordListResponse
	require.NoError(t, json.NewDecoder(listRec.Body).Decode(&listed))
	require.Zero(t, listed.Count)

	adminList := httptest.NewRequest(http.MethodGet, "/api/runtime/memory/records?status=active&limit=10", nil)
	adminList.Header.Set("X-Skills-Admin-Token", "memory-admin")
	adminRec := httptest.NewRecorder()
	router.ServeHTTP(adminRec, adminList)
	require.Equal(t, http.StatusOK, adminRec.Code, adminRec.Body.String())
	require.NoError(t, json.NewDecoder(adminRec.Body).Decode(&listed))
	require.Equal(t, 1, listed.Count)
	require.Equal(t, "private", listed.Items[0].Sensitivity)

	getReq := httptest.NewRequest(http.MethodGet, "/api/runtime/memory/records/"+private.ID, nil)
	getRec := httptest.NewRecorder()
	router.ServeHTTP(getRec, getReq)
	require.Equal(t, http.StatusForbidden, getRec.Code, getRec.Body.String())

	adminGet := httptest.NewRequest(http.MethodGet, "/api/runtime/memory/records/"+private.ID, nil)
	adminGet.Header.Set("X-Skills-Admin-Token", "memory-admin")
	adminGetRec := httptest.NewRecorder()
	router.ServeHTTP(adminGetRec, adminGet)
	require.Equal(t, http.StatusOK, adminGetRec.Code, adminGetRec.Body.String())
}

func TestMemoryAPIEvidenceAndExtractionDiagnosticsAreAdminOnly(t *testing.T) {
	workspace := t.TempDir()
	_, router := newMemoryTestRouter(t, workspace)
	store, err := memorystore.New(memorystore.Config{ProjectRoot: workspace, Mode: "on"})
	require.NoError(t, err)
	_, err = store.EnsureJob(context.Background(), memorystore.MemoryJob{
		ID: "evidence-api-job", Kind: "phase1_extract", JobKey: "evidence-api-job",
		ProjectKey: store.ProjectKey(),
	})
	require.NoError(t, err)
	record, err := store.InsertCandidate(context.Background(), memorystore.Candidate{
		SessionID:        "evidence-session",
		Kind:             "decision",
		Text:             "Keep evidence separate from memory claims.",
		Confidence:       0.9,
		SourceKind:       "session_extract",
		SourceType:       "transcript_message",
		SourceTrust:      "unverified",
		SourceQuote:      "[message-1] Keep evidence separate from memory claims.",
		SourceMessageIDs: []string{"message-1"},
		SourceHash:       "evidence-api-hash",
		SourceRevision:   2,
	})
	require.NoError(t, err)
	require.NoError(t, store.RecordExtractionDiagnostics(context.Background(), []memorystore.ExtractionDiagnostic{{
		JobID:     "evidence-api-job",
		SessionID: "evidence-session",
		Reason:    "ignored_schema",
		Summary:   "candidate did not satisfy the bounded extraction schema",
	}}))
	require.NoError(t, store.Close())

	unauthorizedSources := httptest.NewRequest(
		http.MethodGet, "/api/runtime/memory/records/"+record.ID+"/sources", nil,
	)
	unauthorizedSourcesRec := httptest.NewRecorder()
	router.ServeHTTP(unauthorizedSourcesRec, unauthorizedSources)
	require.Equal(t, http.StatusForbidden, unauthorizedSourcesRec.Code, unauthorizedSourcesRec.Body.String())

	adminSources := httptest.NewRequest(
		http.MethodGet, "/api/runtime/memory/records/"+record.ID+"/sources", nil,
	)
	adminSources.Header.Set("X-Skills-Admin-Token", "memory-admin")
	adminSourcesRec := httptest.NewRecorder()
	router.ServeHTTP(adminSourcesRec, adminSources)
	require.Equal(t, http.StatusOK, adminSourcesRec.Code, adminSourcesRec.Body.String())
	var sourcesResponse struct {
		MemoryID string                     `json:"memory_id"`
		Items    []memorystore.RecordSource `json:"items"`
		Count    int                        `json:"count"`
	}
	require.NoError(t, json.NewDecoder(adminSourcesRec.Body).Decode(&sourcesResponse))
	require.Equal(t, record.ID, sourcesResponse.MemoryID)
	require.Equal(t, 1, sourcesResponse.Count)
	require.Equal(t, "transcript_message", sourcesResponse.Items[0].SourceType)
	require.Equal(t, "[message-1] Keep evidence separate from memory claims.", sourcesResponse.Items[0].Quote)

	unauthorizedDiagnostics := httptest.NewRequest(
		http.MethodGet, "/api/runtime/memory/extraction-diagnostics", nil,
	)
	unauthorizedDiagnosticsRec := httptest.NewRecorder()
	router.ServeHTTP(unauthorizedDiagnosticsRec, unauthorizedDiagnostics)
	require.Equal(t, http.StatusForbidden, unauthorizedDiagnosticsRec.Code, unauthorizedDiagnosticsRec.Body.String())

	adminDiagnostics := httptest.NewRequest(
		http.MethodGet, "/api/runtime/memory/extraction-diagnostics?job_id=evidence-api-job&limit=1", nil,
	)
	adminDiagnostics.Header.Set("X-Skills-Admin-Token", "memory-admin")
	adminDiagnosticsRec := httptest.NewRecorder()
	router.ServeHTTP(adminDiagnosticsRec, adminDiagnostics)
	require.Equal(t, http.StatusOK, adminDiagnosticsRec.Code, adminDiagnosticsRec.Body.String())
	var diagnosticsResponse struct {
		JobID string                             `json:"job_id"`
		Items []memorystore.ExtractionDiagnostic `json:"items"`
		Count int                                `json:"count"`
	}
	require.NoError(t, json.NewDecoder(adminDiagnosticsRec.Body).Decode(&diagnosticsResponse))
	require.Equal(t, "evidence-api-job", diagnosticsResponse.JobID)
	require.Equal(t, 1, diagnosticsResponse.Count)
	require.Equal(t, "ignored_schema", diagnosticsResponse.Items[0].Reason)
}
func TestMemoryAPIRecordsScopeIsolation(t *testing.T) {
	workspace := t.TempDir()
	_, router := newMemoryTestRouter(t, workspace)

	create := func(owner, text string) memoryRecordDTO {
		t.Helper()
		body := fmt.Sprintf(`{"text":%q,"kind":"reference","scope_kind":"user","owner_id":%q}`, text, owner)
		req := httptest.NewRequest(http.MethodPost, "/api/runtime/memory/records", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Skills-Admin-Token", "memory-admin")
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		var record memoryRecordDTO
		require.NoError(t, json.NewDecoder(rec.Body).Decode(&record))
		require.Equal(t, "user", record.ScopeKind)
		require.Equal(t, owner, record.OwnerID)
		return record
	}

	alice := create("alice", "alice API scoped marker")
	bob := create("bob", "bob API scoped marker")

	list := func(owner string) []memoryRecordDTO {
		t.Helper()
		target := "/api/runtime/memory/records?status=active"
		if owner != "" {
			target += "&owner_id=" + url.QueryEscape(owner)
		}
		req := httptest.NewRequest(http.MethodGet, target, nil)
		req.Header.Set("X-Skills-Admin-Token", "memory-admin")
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		var response memoryRecordListResponse
		require.NoError(t, json.NewDecoder(rec.Body).Decode(&response))
		return response.Items
	}

	require.Empty(t, list(""))
	aliceItems := list("alice")
	require.Len(t, aliceItems, 1)
	require.Equal(t, alice.ID, aliceItems[0].ID)
	bobItems := list("bob")
	require.Len(t, bobItems, 1)
	require.Equal(t, bob.ID, bobItems[0].ID)

	req := httptest.NewRequest(http.MethodGet, "/api/runtime/memory/records/"+alice.ID+"?owner_id=alice", nil)
	req.Header.Set("X-Skills-Admin-Token", "memory-admin")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	req = httptest.NewRequest(http.MethodGet, "/api/runtime/memory/records/"+alice.ID+"?owner_id=bob", nil)
	req.Header.Set("X-Skills-Admin-Token", "memory-admin")
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusNotFound, rec.Code, rec.Body.String())
}
