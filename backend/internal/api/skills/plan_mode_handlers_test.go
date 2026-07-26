package skills

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
	"github.com/wwsheng009/ai-agent-runtime/internal/planmode"
	runtimepolicy "github.com/wwsheng009/ai-agent-runtime/internal/policy"
	"github.com/wwsheng009/ai-agent-runtime/internal/sessionmeta"
	"github.com/wwsheng009/ai-agent-runtime/internal/skill"
)

func TestGetSessionPlanModeReturnsInactiveWithoutState(t *testing.T) {
	ctx := context.Background()
	storage := chat.NewInMemoryStorage()
	manager := chat.NewSessionManager(storage, nil)
	session, err := manager.Create(ctx, "user-1")
	require.NoError(t, err)

	handler := NewHandler(skill.NewRegistry(nil), nil, nil)
	handler.SetSessionManager(manager)
	router := mux.NewRouter()
	handler.RegisterRoutes(router)

	req := httptest.NewRequest(http.MethodGet, "/api/runtime/sessions/"+session.ID+"/plan", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	var resp sessionPlanModeResponse
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
	require.Equal(t, session.ID, resp.SessionID)
	require.False(t, resp.Active)
	require.Equal(t, string(planmode.StatusInactive), resp.Status)
	require.Equal(t, string(runtimepolicy.ModeDefault), resp.PermissionMode)
	require.Empty(t, resp.PlanContent)
	require.False(t, resp.PlanContentAvailable)
}

func TestSessionPlanModeEnterExitApproveAndPreview(t *testing.T) {
	ctx := context.Background()
	workspace := t.TempDir()
	planPath := "docs/implementation-plan.md"
	planAbs := filepath.Join(workspace, filepath.FromSlash(planPath))
	require.NoError(t, os.MkdirAll(filepath.Dir(planAbs), 0o755))
	require.NoError(t, os.WriteFile(planAbs, []byte("# Plan\n\n1. implement preview\n"), 0o644))

	storage := chat.NewInMemoryStorage()
	manager := chat.NewSessionManager(storage, nil)
	session, err := manager.Create(ctx, "user-1")
	require.NoError(t, err)
	session.SetContext(sessionmeta.WorkspacePath, workspace)
	session.SetContext(sessionmeta.PermissionMode, string(runtimepolicy.ModeAcceptEdits))
	require.NoError(t, manager.Update(ctx, session))

	handler := NewHandler(skill.NewRegistry(nil), nil, nil)
	handler.SetSessionManager(manager)
	router := mux.NewRouter()
	handler.RegisterRoutes(router)

	enterBody := `{"action":"enter","plan_path":"docs/implementation-plan.md"}`
	enterReq := httptest.NewRequest(http.MethodPost, "/api/runtime/sessions/"+session.ID+"/plan", strings.NewReader(enterBody))
	enterReq.Header.Set("Content-Type", "application/json")
	enterRec := httptest.NewRecorder()
	router.ServeHTTP(enterRec, enterReq)
	require.Equal(t, http.StatusOK, enterRec.Code, enterRec.Body.String())

	var entered sessionPlanModeResponse
	require.NoError(t, json.NewDecoder(enterRec.Body).Decode(&entered))
	require.True(t, entered.Active)
	require.Equal(t, string(planmode.StatusActive), entered.Status)
	require.Equal(t, string(runtimepolicy.ModePlan), entered.PermissionMode)
	require.Equal(t, string(runtimepolicy.ModeAcceptEdits), entered.PreviousMode)
	require.Equal(t, planPath, entered.PlanPath)
	require.Contains(t, entered.WriteAllowPaths, planPath)
	require.True(t, entered.PlanContentAvailable)
	require.Contains(t, entered.PlanContent, "implement preview")
	require.Equal(t, workspace, entered.WorkspacePath)

	// GET should mirror durable state + file content.
	getReq := httptest.NewRequest(http.MethodGet, "/api/runtime/sessions/"+session.ID+"/plan", nil)
	getRec := httptest.NewRecorder()
	router.ServeHTTP(getRec, getReq)
	require.Equal(t, http.StatusOK, getRec.Code)
	var got sessionPlanModeResponse
	require.NoError(t, json.NewDecoder(getRec.Body).Decode(&got))
	require.True(t, got.Active)
	require.True(t, got.PlanContentAvailable)
	require.Contains(t, got.PlanContent, "# Plan")

	// request_changes stays active.
	changeBody := `{"action":"request_changes","notes":"add tests"}`
	changeReq := httptest.NewRequest(http.MethodPost, "/api/runtime/sessions/"+session.ID+"/plan", strings.NewReader(changeBody))
	changeReq.Header.Set("Content-Type", "application/json")
	changeRec := httptest.NewRecorder()
	router.ServeHTTP(changeRec, changeReq)
	require.Equal(t, http.StatusOK, changeRec.Code, changeRec.Body.String())
	var changed sessionPlanModeResponse
	require.NoError(t, json.NewDecoder(changeRec.Body).Decode(&changed))
	require.True(t, changed.Active)
	require.Equal(t, string(planmode.StatusActive), changed.Status)
	require.Equal(t, string(planmode.ExitRequestChanges), changed.ExitDecision)
	require.Equal(t, "add tests", changed.Notes)
	require.Equal(t, string(runtimepolicy.ModePlan), changed.PermissionMode)

	// approve exits plan mode and restores previous permission mode.
	approveBody := `{"action":"exit","decision":"approve","notes":"looks good"}`
	approveReq := httptest.NewRequest(http.MethodPost, "/api/runtime/sessions/"+session.ID+"/plan", strings.NewReader(approveBody))
	approveReq.Header.Set("Content-Type", "application/json")
	approveRec := httptest.NewRecorder()
	router.ServeHTTP(approveRec, approveReq)
	require.Equal(t, http.StatusOK, approveRec.Code, approveRec.Body.String())
	var approved sessionPlanModeResponse
	require.NoError(t, json.NewDecoder(approveRec.Body).Decode(&approved))
	require.False(t, approved.Active)
	require.Equal(t, string(planmode.StatusExited), approved.Status)
	require.Equal(t, string(planmode.ExitApprove), approved.ExitDecision)
	require.Equal(t, string(runtimepolicy.ModeAcceptEdits), approved.PermissionMode)
	require.Equal(t, "looks good", approved.Notes)

	// Durable session context should match.
	stored, err := manager.GetSession(ctx, session.ID)
	require.NoError(t, err)
	state := planmode.Load(stored)
	require.Equal(t, planmode.StatusExited, state.Status)
	require.Equal(t, planmode.ExitApprove, state.ExitDecision)
	require.Equal(t, string(runtimepolicy.ModeAcceptEdits), sessionPermissionMode(stored))
}

func TestSessionPlanModeExitQuitAndMissingPlanFile(t *testing.T) {
	ctx := context.Background()
	workspace := t.TempDir()

	storage := chat.NewInMemoryStorage()
	manager := chat.NewSessionManager(storage, nil)
	session, err := manager.Create(ctx, "user-1")
	require.NoError(t, err)
	session.SetContext(sessionmeta.WorkspacePath, workspace)
	require.NoError(t, manager.Update(ctx, session))

	handler := NewHandler(skill.NewRegistry(nil), nil, nil)
	handler.SetSessionManager(manager)
	router := mux.NewRouter()
	handler.RegisterRoutes(router)

	enterReq := httptest.NewRequest(http.MethodPost, "/api/runtime/sessions/"+session.ID+"/plan", strings.NewReader(`{"action":"enter"}`))
	enterReq.Header.Set("Content-Type", "application/json")
	enterRec := httptest.NewRecorder()
	router.ServeHTTP(enterRec, enterReq)
	require.Equal(t, http.StatusOK, enterRec.Code)
	var entered sessionPlanModeResponse
	require.NoError(t, json.NewDecoder(enterRec.Body).Decode(&entered))
	require.True(t, entered.Active)
	require.Equal(t, planmode.DefaultPlanPath, entered.PlanPath)
	require.False(t, entered.PlanContentAvailable)
	require.Empty(t, entered.PlanContent)

	quitReq := httptest.NewRequest(http.MethodPost, "/api/runtime/sessions/"+session.ID+"/plan", strings.NewReader(`{"action":"quit"}`))
	quitReq.Header.Set("Content-Type", "application/json")
	quitRec := httptest.NewRecorder()
	router.ServeHTTP(quitRec, quitReq)
	require.Equal(t, http.StatusOK, quitRec.Code)
	var quit sessionPlanModeResponse
	require.NoError(t, json.NewDecoder(quitRec.Body).Decode(&quit))
	require.False(t, quit.Active)
	require.Equal(t, string(planmode.StatusExited), quit.Status)
	require.Equal(t, string(planmode.ExitQuit), quit.ExitDecision)
	require.Equal(t, string(runtimepolicy.ModeDefault), quit.PermissionMode)
}

func TestSessionPlanModeRejectsPathEscapeAndInvalidAction(t *testing.T) {
	ctx := context.Background()
	workspace := t.TempDir()

	storage := chat.NewInMemoryStorage()
	manager := chat.NewSessionManager(storage, nil)
	session, err := manager.Create(ctx, "user-1")
	require.NoError(t, err)
	session.SetContext(sessionmeta.WorkspacePath, workspace)
	state := planmode.Enter(string(runtimepolicy.ModeDefault), "../outside.md")
	planmode.Save(session, state)
	session.SetContext(sessionmeta.PermissionMode, string(runtimepolicy.ModePlan))
	require.NoError(t, manager.Update(ctx, session))

	handler := NewHandler(skill.NewRegistry(nil), nil, nil)
	handler.SetSessionManager(manager)
	router := mux.NewRouter()
	handler.RegisterRoutes(router)

	getReq := httptest.NewRequest(http.MethodGet, "/api/runtime/sessions/"+session.ID+"/plan", nil)
	getRec := httptest.NewRecorder()
	router.ServeHTTP(getRec, getReq)
	require.Equal(t, http.StatusOK, getRec.Code)
	var got sessionPlanModeResponse
	require.NoError(t, json.NewDecoder(getRec.Body).Decode(&got))
	require.True(t, got.Active)
	require.False(t, got.PlanContentAvailable)
	require.Contains(t, got.PlanContentError, "escapes workspace")

	badReq := httptest.NewRequest(http.MethodPost, "/api/runtime/sessions/"+session.ID+"/plan", strings.NewReader(`{"action":"nope"}`))
	badReq.Header.Set("Content-Type", "application/json")
	badRec := httptest.NewRecorder()
	router.ServeHTTP(badRec, badReq)
	require.Equal(t, http.StatusBadRequest, badRec.Code)

	// Exit without being in plan mode should fail.
	session2, err := manager.Create(ctx, "user-2")
	require.NoError(t, err)
	exitReq := httptest.NewRequest(http.MethodPost, "/api/runtime/sessions/"+session2.ID+"/plan", strings.NewReader(`{"action":"approve"}`))
	exitReq.Header.Set("Content-Type", "application/json")
	exitRec := httptest.NewRecorder()
	router.ServeHTTP(exitRec, exitReq)
	require.Equal(t, http.StatusBadRequest, exitRec.Code)
}

func TestResolvePlanPreviewPathRejectsTraversal(t *testing.T) {
	workspace := t.TempDir()
	_, err := resolvePlanPreviewPath(workspace, "..\\secret.md")
	require.Error(t, err)
	require.Contains(t, err.Error(), "escapes workspace")

	abs, err := resolvePlanPreviewPath(workspace, "plan.md")
	require.NoError(t, err)
	require.Equal(t, filepath.Join(workspace, "plan.md"), abs)
}

func TestNormalizePlanModeActionAliases(t *testing.T) {
	action, decision, err := normalizePlanModeAction(sessionPlanModeRequest{Action: "on", PlanPath: "x.md"})
	require.NoError(t, err)
	require.Equal(t, "enter", action)
	require.Equal(t, planmode.ExitNone, decision)

	action, decision, err = normalizePlanModeAction(sessionPlanModeRequest{Decision: "approve"})
	require.NoError(t, err)
	require.Equal(t, "exit", action)
	require.Equal(t, planmode.ExitApprove, decision)

	action, decision, err = normalizePlanModeAction(sessionPlanModeRequest{Action: "request-changes"})
	require.NoError(t, err)
	require.Equal(t, "request_changes", action)
	require.Equal(t, planmode.ExitRequestChanges, decision)

	_, _, err = normalizePlanModeAction(sessionPlanModeRequest{Action: "exit"})
	require.Error(t, err)
}
