package runtimeapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/gorilla/mux"
	"github.com/stretchr/testify/require"

	runtimecfg "github.com/wwsheng009/ai-agent-runtime/internal/config"
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
	patchRec := httptest.NewRecorder()
	router.ServeHTTP(patchRec, patchReq)
	require.Equal(t, http.StatusOK, patchRec.Code, patchRec.Body.String())

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

func TestMemoryAPIWorkspaceBindingAndAdmin(t *testing.T) {
	workspace := t.TempDir()
	other := t.TempDir()
	_, router := newMemoryTestRouter(t, workspace)

	body := `{"workspace_path":` + strconv.Quote(other) + `,"text":"must not cross workspace"}`
	req := httptest.NewRequest(http.MethodPost, "/api/runtime/memory/records", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
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
}

func TestMemoryAPIStatusOffDoesNotCreateDB(t *testing.T) {
	workspace := t.TempDir()
	handler, router := newHarnessTestRouter(t)
	cfg := runtimecfg.DefaultRuntimeConfig()
	cfg.Workspace.Root = workspace
	cfg.Memory.Mode = "off"
	handler.SetRuntimeConfig(cfg, "")

	req := httptest.NewRequest(http.MethodGet, "/api/runtime/memory/status", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
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
	traceRec := httptest.NewRecorder()
	router.ServeHTTP(traceRec, traceReq)
	require.Equal(t, http.StatusOK, traceRec.Code, traceRec.Body.String())
}
