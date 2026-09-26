package runtimeapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gorilla/mux"
	"github.com/stretchr/testify/require"
	"github.com/wwsheng009/ai-agent-runtime/internal/chat"
	"github.com/wwsheng009/ai-agent-runtime/internal/planstore"
	"github.com/wwsheng009/ai-agent-runtime/internal/sessionmeta"
	"github.com/wwsheng009/ai-agent-runtime/internal/skill"
)

func newPlansTestRouter(t *testing.T, store *planstore.Store) *mux.Router {
	t.Helper()
	handler := NewHandler(skill.NewRegistry(nil), nil, nil)
	handler.plansStore = store
	router := mux.NewRouter()
	handler.RegisterRoutes(router)
	return router
}

func seedStoredPlan(t *testing.T, store *planstore.Store) planstore.Record {
	t.Helper()
	record, err := store.Record(planstore.RecordOptions{
		SessionID:   "session-1",
		ProjectPath: "/work/demo",
		PlanPath:    "docs/plan.md",
		Title:       "demo plan",
	})
	require.NoError(t, err)
	record, err = store.Snapshot(planstore.SnapshotOptions{
		ID:       record.ID,
		Decision: "approve",
		Source:   "user",
		Content:  []byte("# Plan\n\n1. ship it\n"),
		Status:   planstore.StatusApproved,
	})
	require.NoError(t, err)
	return record
}

func TestListStoredPlansReturnsArchivedRecords(t *testing.T) {
	store := planstore.NewStore(t.TempDir())
	record := seedStoredPlan(t, store)
	router := newPlansTestRouter(t, store)

	req := httptest.NewRequest(http.MethodGet, "/api/runtime/plans", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var resp storedPlanListResponse
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
	require.Equal(t, 1, resp.Count)
	require.Equal(t, record.ID, resp.Plans[0].ID)
	require.Equal(t, string(planstore.StatusApproved), resp.Plans[0].Status)
	require.Equal(t, 1, resp.Plans[0].Version)
}

func TestGetStoredPlanReturnsLatestSnapshot(t *testing.T) {
	store := planstore.NewStore(t.TempDir())
	record := seedStoredPlan(t, store)
	router := newPlansTestRouter(t, store)

	req := httptest.NewRequest(http.MethodGet, "/api/runtime/plans/"+record.ID, nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var resp storedPlanResponse
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
	require.Equal(t, record.ID, resp.ID)
	require.True(t, resp.ContentAvailable)
	require.Contains(t, resp.Content, "ship it")
	require.False(t, resp.ContentTruncated)
	require.Len(t, resp.Rounds, 1)
	require.Equal(t, "user", resp.Rounds[0].Source)
}

func TestGetStoredPlanUnknownIdReturnsNotFound(t *testing.T) {
	store := planstore.NewStore(t.TempDir())
	router := newPlansTestRouter(t, store)

	req := httptest.NewRequest(http.MethodGet, "/api/runtime/plans/demo/ghost", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusNotFound, rec.Code, rec.Body.String())
}

func TestListStoredPlansReportsCorruptIndex(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, "index.json"), []byte("{not-json"), 0o644))
	router := newPlansTestRouter(t, planstore.NewStore(root))

	req := httptest.NewRequest(http.MethodGet, "/api/runtime/plans", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusInternalServerError, rec.Code, rec.Body.String())
}

// Session-direct plan transitions archive the artifact without an actor.
func TestUpdateSessionPlanModeArchivesArtifact(t *testing.T) {
	ctx := context.Background()
	workspace := t.TempDir()
	planPath := "docs/plan.md"
	planAbs := filepath.Join(workspace, filepath.FromSlash(planPath))
	require.NoError(t, os.MkdirAll(filepath.Dir(planAbs), 0o755))
	require.NoError(t, os.WriteFile(planAbs, []byte("# Plan\n\nfirst pass\n"), 0o644))

	storage := chat.NewInMemoryStorage()
	manager := chat.NewSessionManager(storage, nil)
	session, err := manager.Create(ctx, "user-1")
	require.NoError(t, err)
	session.SetContext(sessionmeta.WorkspacePath, workspace)
	require.NoError(t, manager.Update(ctx, session))

	store := planstore.NewStore(t.TempDir())
	handler := NewHandler(skill.NewRegistry(nil), nil, nil)
	handler.SetSessionManager(manager)
	handler.plansStore = store
	router := mux.NewRouter()
	handler.RegisterRoutes(router)

	enterReq := httptest.NewRequest(http.MethodPost, "/api/runtime/sessions/"+session.ID+"/plan", strings.NewReader(`{"action":"enter","plan_path":"docs/plan.md"}`))
	enterReq.Header.Set("Content-Type", "application/json")
	enterRec := httptest.NewRecorder()
	router.ServeHTTP(enterRec, enterReq)
	require.Equal(t, http.StatusOK, enterRec.Code, enterRec.Body.String())

	// enter registers metadata only: no review round yet.
	records, err := store.List()
	require.NoError(t, err)
	require.Len(t, records, 1)
	require.Equal(t, 0, records[0].Version)
	require.Equal(t, planstore.StatusPending, records[0].Status)

	approveReq := httptest.NewRequest(http.MethodPost, "/api/runtime/sessions/"+session.ID+"/plan", strings.NewReader(`{"action":"approve","notes":"ship it"}`))
	approveReq.Header.Set("Content-Type", "application/json")
	approveRec := httptest.NewRecorder()
	router.ServeHTTP(approveRec, approveReq)
	require.Equal(t, http.StatusOK, approveRec.Code, approveRec.Body.String())

	records, err = store.List()
	require.NoError(t, err)
	require.Len(t, records, 1)
	require.Equal(t, planstore.StatusApproved, records[0].Status)
	require.Equal(t, 1, records[0].Version)
	require.Len(t, records[0].Rounds, 1)
	require.Equal(t, "ship it", records[0].Rounds[0].Notes)
	require.Equal(t, "user", records[0].Rounds[0].Source)

	latest, err := store.ReadLatest(records[0].ID)
	require.NoError(t, err)
	require.Contains(t, string(latest), "first pass")
}

// DELETE removes the record and its snapshots; a second delete stays idempotent
// (deleted=false) so retrying hosts do not have to feature-detect the route.
func TestDeleteStoredPlanRemovesRecord(t *testing.T) {
	store := planstore.NewStore(t.TempDir())
	record := seedStoredPlan(t, store)
	router := newPlansTestRouter(t, store)

	req := httptest.NewRequest(http.MethodDelete, "/api/runtime/plans/"+record.ID, nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var resp map[string]interface{}
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
	require.Equal(t, true, resp["deleted"])

	if _, ok, err := store.Get(record.ID); err != nil || ok {
		t.Fatalf("record must be gone, ok=%v err=%v", ok, err)
	}
	if _, err := store.ReadLatest(record.ID); err == nil {
		t.Fatal("snapshot must be gone with the record")
	}

	req = httptest.NewRequest(http.MethodDelete, "/api/runtime/plans/"+record.ID, nil)
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	resp = map[string]interface{}{}
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
	require.Equal(t, false, resp["deleted"])
}

func TestDeleteStoredPlanRequiresId(t *testing.T) {
	store := planstore.NewStore(t.TempDir())
	handler := NewHandler(skill.NewRegistry(nil), nil, nil)
	handler.plansStore = store
	rec := httptest.NewRecorder()
	handler.DeleteStoredPlan(rec, httptest.NewRequest(http.MethodDelete, "/api/runtime/plans/", nil))
	require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
}
