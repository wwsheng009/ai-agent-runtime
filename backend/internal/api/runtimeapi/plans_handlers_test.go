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
	runtimepolicy "github.com/wwsheng009/ai-agent-runtime/internal/policy"
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

// newPlansReopenTestRig wires a session manager (with a workspace-scoped session)
// plus a plan store holding one archived round for docs/plan.md.
func newPlansReopenTestRig(t *testing.T, workspace string) (*mux.Router, *chat.Session, *planstore.Store) {
	t.Helper()
	ctx := context.Background()
	storage := chat.NewInMemoryStorage()
	manager := chat.NewSessionManager(storage, nil)
	session, err := manager.Create(ctx, "user-1")
	require.NoError(t, err)
	session.SetContext(sessionmeta.WorkspacePath, workspace)
	require.NoError(t, manager.Update(ctx, session))

	store := planstore.NewStore(t.TempDir())
	record, err := store.Record(planstore.RecordOptions{
		SessionID:   session.ID,
		ProjectPath: workspace,
		PlanPath:    "docs/plan.md",
		Title:       "reopen demo",
	})
	require.NoError(t, err)
	_, err = store.Snapshot(planstore.SnapshotOptions{
		ID:       record.ID,
		Decision: "request_changes",
		Source:   "user",
		Content:  []byte("# Plan\n\n1. restored round\n"),
		Status:   planstore.StatusPending,
	})
	require.NoError(t, err)

	handler := NewHandler(skill.NewRegistry(nil), nil, nil)
	handler.SetSessionManager(manager)
	handler.plansStore = store
	router := mux.NewRouter()
	handler.RegisterRoutes(router)
	return router, session, store
}

func postPlanReopen(t *testing.T, router *mux.Router, sessionID, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/runtime/sessions/"+sessionID+"/plan/reopen", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec
}

func TestReopenStoredPlanRestoresSnapshotAndEntersPlanMode(t *testing.T) {
	workspace := t.TempDir()
	router, session, store := newPlansReopenTestRig(t, workspace)
	records, err := store.List()
	require.NoError(t, err)
	require.Len(t, records, 1)

	rec := postPlanReopen(t, router, session.ID, `{"plan_id":"`+records[0].ID+`"}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	var resp storedPlanReopenResponse
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
	require.True(t, resp.Reopened)
	require.Equal(t, records[0].ID, resp.PlanID)
	require.Equal(t, "docs/plan.md", resp.DisplayPath)
	require.Equal(t, 1, resp.Version)
	require.True(t, resp.Created, "missing workspace file is created from the snapshot")
	require.NotNil(t, resp.PlanMode)
	require.True(t, resp.PlanMode.Active)
	require.Equal(t, records[0].ID, resp.PlanMode.ReopenedFrom)
	require.Equal(t, 1, resp.PlanMode.ReopenedVersion)
	require.Equal(t, string(runtimepolicy.ModePlan), resp.PlanMode.PermissionMode)

	written, err := os.ReadFile(filepath.Join(workspace, "docs", "plan.md"))
	require.NoError(t, err)
	require.Equal(t, "# Plan\n\n1. restored round\n", string(written))

	// The durable session carries the same lineage for later GETs and /plan status.
	detail := httptest.NewRequest(http.MethodGet, "/api/runtime/sessions/"+session.ID+"/plan", nil)
	detailRec := httptest.NewRecorder()
	router.ServeHTTP(detailRec, detail)
	require.Equal(t, http.StatusOK, detailRec.Code)
	var state sessionPlanModeResponse
	require.NoError(t, json.NewDecoder(detailRec.Body).Decode(&state))
	require.True(t, state.Active)
	require.Equal(t, records[0].ID, state.ReopenedFrom)
	require.Equal(t, 1, state.ReopenedVersion)
}

func TestReopenStoredPlanConflictNeedsForce(t *testing.T) {
	workspace := t.TempDir()
	router, session, store := newPlansReopenTestRig(t, workspace)
	records, err := store.List()
	require.NoError(t, err)
	planAbs := filepath.Join(workspace, "docs", "plan.md")
	require.NoError(t, os.MkdirAll(filepath.Dir(planAbs), 0o755))
	require.NoError(t, os.WriteFile(planAbs, []byte("# Plan\n\n1. local work in progress\n"), 0o644))

	rec := postPlanReopen(t, router, session.ID, `{"plan_id":"`+records[0].ID+`"}`)
	require.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())
	var conflict map[string]interface{}
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&conflict))
	require.Equal(t, true, conflict["conflict"])
	require.Contains(t, conflict["hint"], "force=true")

	// A refused reopen never touches the workspace file.
	kept, err := os.ReadFile(planAbs)
	require.NoError(t, err)
	require.Equal(t, "# Plan\n\n1. local work in progress\n", string(kept))

	forced := postPlanReopen(t, router, session.ID, `{"plan_id":"`+records[0].ID+`","force":true,"version":1}`)
	require.Equal(t, http.StatusOK, forced.Code, forced.Body.String())
	var resp storedPlanReopenResponse
	require.NoError(t, json.NewDecoder(forced.Body).Decode(&resp))
	require.True(t, resp.Forced)
	require.True(t, resp.Reopened)
	overwritten, err := os.ReadFile(planAbs)
	require.NoError(t, err)
	require.Equal(t, "# Plan\n\n1. restored round\n", string(overwritten))
}

func TestReopenStoredPlanRejectsMissingRecordAndPlanID(t *testing.T) {
	workspace := t.TempDir()
	router, session, _ := newPlansReopenTestRig(t, workspace)

	missing := postPlanReopen(t, router, session.ID, `{"plan_id":"demo/unknown"}`)
	require.Equal(t, http.StatusNotFound, missing.Code, missing.Body.String())

	empty := postPlanReopen(t, router, session.ID, `{}`)
	require.Equal(t, http.StatusBadRequest, empty.Code, empty.Body.String())
	require.Contains(t, empty.Body.String(), "plan_id is required")

	unknownSession := postPlanReopen(t, router, "session-missing", `{"plan_id":"demo/plan"}`)
	require.NotEqual(t, http.StatusOK, unknownSession.Code)
}

// seedStoredPlanRounds archives `contents` as successive review rounds and
// returns the final record (one round per content, versions 1..N).
func seedStoredPlanRounds(t *testing.T, store *planstore.Store, contents ...string) planstore.Record {
	t.Helper()
	record, err := store.Record(planstore.RecordOptions{
		SessionID:   "session-1",
		ProjectPath: "/work/demo",
		PlanPath:    "docs/plan.md",
		Title:       "demo plan",
	})
	require.NoError(t, err)
	for index, content := range contents {
		decision := "request_changes"
		if index == len(contents)-1 {
			decision = "approve"
		}
		record, err = store.Snapshot(planstore.SnapshotOptions{
			ID:       record.ID,
			Decision: decision,
			Source:   "user",
			Content:  []byte(content),
			Status:   planstore.StatusPending,
		})
		require.NoError(t, err)
	}
	return record
}

func getStoredPlanDiff(t *testing.T, router *mux.Router, planID, query string) *httptest.ResponseRecorder {
	t.Helper()
	url := "/api/runtime/plans/" + planID + "/diff"
	if query != "" {
		url += "?" + query
	}
	req := httptest.NewRequest(http.MethodGet, url, nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec
}

func TestDiffStoredPlanReturnsRoundDelta(t *testing.T) {
	store := planstore.NewStore(t.TempDir())
	record := seedStoredPlanRounds(t, store,
		"# Plan\n\n1. ship it\n",
		"# Plan\n\n1. ship it\n2. roll back\n3. document\n",
	)
	router := newPlansTestRouter(t, store)

	// 默认口径：from=上一轮，to=最新轮；路由必须赢过贪婪的 /plans/{id:.*} 明细路由。
	rec := getStoredPlanDiff(t, router, record.ID, "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var resp storedPlanDiffResponse
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
	require.Equal(t, record.ID, resp.PlanID)
	require.Equal(t, 1, resp.FromVersion)
	require.Equal(t, 2, resp.ToVersion)
	require.False(t, resp.Identical)
	require.Equal(t, 2, resp.Added)
	require.Zero(t, resp.Removed)
	require.False(t, resp.Truncated)
	require.Contains(t, resp.Text, "--- v1 request_changes")
	require.Contains(t, resp.Text, "+++ v2 approve")
	require.Contains(t, resp.Text, "+2. roll back")
	require.Contains(t, resp.Text, "@@")
}

func TestDiffStoredPlanHonoursKnobsAndErrors(t *testing.T) {
	store := planstore.NewStore(t.TempDir())
	record := seedStoredPlanRounds(t, store,
		"# Plan\n\n1. ship it\n",
		"# Plan\n\n1. ship it\n2. roll back\n",
	)
	router := newPlansTestRouter(t, store)

	// 同一轮对比：Identical=true，正文只剩版本框架行。
	same := getStoredPlanDiff(t, router, record.ID, "from=2&to=2")
	require.Equal(t, http.StatusOK, same.Code, same.Body.String())
	var sameResp storedPlanDiffResponse
	require.NoError(t, json.NewDecoder(same.Body).Decode(&sameResp))
	require.True(t, sameResp.Identical)
	require.Zero(t, sameResp.Added)
	require.NotContains(t, sameResp.Text, "@@")

	// max_lines=1：正文被截断（计数仍按渲染部分给出）。
	truncated := getStoredPlanDiff(t, router, record.ID, "max_lines=1")
	require.Equal(t, http.StatusOK, truncated.Code, truncated.Body.String())
	var truncatedResp storedPlanDiffResponse
	require.NoError(t, json.NewDecoder(truncated.Body).Decode(&truncatedResp))
	require.True(t, truncatedResp.Truncated)

	// 参数校验：非整数 / 越界 → 400；未知记录与未知版本 → 404。
	badQuery := getStoredPlanDiff(t, router, record.ID, "from=abc")
	require.Equal(t, http.StatusBadRequest, badQuery.Code, badQuery.Body.String())
	require.Contains(t, badQuery.Body.String(), "from must be an integer")

	outOfRange := getStoredPlanDiff(t, router, record.ID, "context=99")
	require.Equal(t, http.StatusBadRequest, outOfRange.Code, outOfRange.Body.String())

	missingRecord := getStoredPlanDiff(t, router, "demo/ghost", "")
	require.Equal(t, http.StatusNotFound, missingRecord.Code, missingRecord.Body.String())

	missingVersion := getStoredPlanDiff(t, router, record.ID, "from=9&to=2")
	require.Equal(t, http.StatusNotFound, missingVersion.Code, missingVersion.Body.String())
}
