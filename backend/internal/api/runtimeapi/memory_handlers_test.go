package runtimeapi

import (
	"context"
	"encoding/json"
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

func TestMemoryAPIWhyNotRequiresAdminAndExplainsSelection(t *testing.T) {
	workspace := t.TempDir()
	_, router := newMemoryTestRouter(t, workspace)
	createReq := httptest.NewRequest(http.MethodPost, "/api/runtime/memory/records", strings.NewReader(`{"text":"why-not alpha record","kind":"fact"}`))
	createReq.Header.Set("Content-Type", "application/json")
	createReq.Header.Set("X-Skills-Admin-Token", "memory-admin")
	createRec := httptest.NewRecorder()
	router.ServeHTTP(createRec, createReq)
	require.Equal(t, http.StatusOK, createRec.Code, createRec.Body.String())
	var created memoryRecordDTO
	require.NoError(t, json.NewDecoder(createRec.Body).Decode(&created))

	whyNotReq := httptest.NewRequest(http.MethodGet, "/api/runtime/memory/why-not?q=alpha", nil)
	whyNotReq.Header.Set("X-Skills-Admin-Token", "memory-admin")
	whyNotRec := httptest.NewRecorder()
	router.ServeHTTP(whyNotRec, whyNotReq)
	require.Equal(t, http.StatusOK, whyNotRec.Code, whyNotRec.Body.String())
	var explanation memoryWhyNotResponse
	require.NoError(t, json.NewDecoder(whyNotRec.Body).Decode(&explanation))
	require.Equal(t, "on", explanation.Mode)
	require.Equal(t, "alpha", explanation.QueryPreview)
	require.True(t, explanation.Injected)
	require.NotEmpty(t, explanation.Selected)
	require.Equal(t, created.ID, explanation.Selected[0].ID)

	secretReq := httptest.NewRequest(http.MethodGet, "/api/runtime/memory/why-not?q="+url.QueryEscape("sk-abcdefghijklmnop"), nil)
	secretReq.Header.Set("X-Skills-Admin-Token", "memory-admin")
	secretRec := httptest.NewRecorder()
	router.ServeHTTP(secretRec, secretReq)
	require.Equal(t, http.StatusOK, secretRec.Code, secretRec.Body.String())
	var secretExplanation memoryWhyNotResponse
	require.NoError(t, json.NewDecoder(secretRec.Body).Decode(&secretExplanation))
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
	require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())

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
	importReq := httptest.NewRequest(http.MethodPost, "/api/runtime/memory/import", strings.NewReader(`{"type":"record","record":{"text":"must not create db"}}`))
	importReq.Header.Set("X-Skills-Admin-Token", "memory-admin")
	importRec := httptest.NewRecorder()
	router.ServeHTTP(importRec, importReq)
	require.Equal(t, http.StatusConflict, importRec.Code, importRec.Body.String())
	if _, err := os.Stat(filepath.Join(workspace, ".aicli", "memory", "memory.db")); !os.IsNotExist(err) {
		t.Fatalf("off status must not create memory.db, stat err=%v", err)
	}
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
