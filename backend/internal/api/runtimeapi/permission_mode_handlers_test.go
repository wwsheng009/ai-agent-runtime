package runtimeapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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

func newPermissionModeRouter(t *testing.T) (*chat.SessionManager, *mux.Router, *chat.Session) {
	t.Helper()
	ctx := context.Background()
	storage := chat.NewInMemoryStorage()
	manager := chat.NewSessionManager(storage, nil)
	session, err := manager.Create(ctx, "user-1")
	require.NoError(t, err)

	handler := NewHandler(skill.NewRegistry(nil), nil, nil)
	handler.SetSessionManager(manager)
	router := mux.NewRouter()
	handler.RegisterRoutes(router)
	return manager, router, session
}

func postPermissionMode(t *testing.T, router *mux.Router, sessionID, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/runtime/sessions/"+sessionID+"/permission-mode", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec
}

func TestGetSessionPermissionModeListsBackendModes(t *testing.T) {
	_, router, session := newPermissionModeRouter(t)

	req := httptest.NewRequest(http.MethodGet, "/api/runtime/sessions/"+session.ID+"/permission-mode", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var resp sessionPermissionModeResponse
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
	require.Equal(t, session.ID, resp.SessionID)
	require.Equal(t, string(runtimepolicy.ModeDefault), resp.Mode)
	require.False(t, resp.PlanActive)

	values := make([]string, 0, len(resp.Supported))
	for _, option := range resp.Supported {
		values = append(values, option.Value)
	}
	// 断言与后端枚举常量同源，避免测试把驼峰别名写死成"契约"。
	require.Equal(t, []string{
		string(runtimepolicy.ModeDefault),
		string(runtimepolicy.ModeAcceptEdits),
		string(runtimepolicy.ModePlan),
		string(runtimepolicy.ModeBypassPermissions),
	}, values)

	// 危险模式与 plan 入口语义必须暴露给前端，避免 UI 静默放宽策略。
	require.False(t, resp.Supported[0].Dangerous)
	require.False(t, resp.Supported[1].Dangerous)
	require.True(t, resp.Supported[2].RequiresPlanEntry)
	require.True(t, resp.Supported[3].Dangerous)
}

func TestUpdateSessionPermissionModePersistsForIdleSession(t *testing.T) {
	ctx := context.Background()
	manager, router, session := newPermissionModeRouter(t)

	rec := postPermissionMode(t, router, session.ID, `{"mode":"accept_edits"}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	var resp sessionPermissionModeResponse
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
	require.True(t, resp.Updated)
	require.Equal(t, string(runtimepolicy.ModeAcceptEdits), resp.Mode)

	stored, err := manager.GetSession(ctx, session.ID)
	require.NoError(t, err)
	require.Equal(t, string(runtimepolicy.ModeAcceptEdits), sessionPermissionMode(stored))
	require.Equal(t, string(runtimepolicy.ModeAcceptEdits),
		sessionmeta.String(stored.Metadata.Context, sessionmeta.RequestedPermissionMode))
	require.Equal(t, string(runtimepolicy.ModeAcceptEdits),
		sessionmeta.String(stored.Metadata.Context, sessionmeta.EffectivePermissionMode))
}

func TestUpdateSessionPermissionModeRejectsUnknownAndPlan(t *testing.T) {
	ctx := context.Background()
	manager, router, session := newPermissionModeRouter(t)

	// 未知模式绝不静默降级为 default。
	unknown := postPermissionMode(t, router, session.ID, `{"mode":"yolo"}`)
	require.Equal(t, http.StatusBadRequest, unknown.Code)

	stored, err := manager.GetSession(ctx, session.ID)
	require.NoError(t, err)
	require.Equal(t, string(runtimepolicy.ModeDefault), sessionPermissionMode(stored))

	// plan 必须走 plan 入口（需要 plan_path / previous_mode 记账）。
	plan := postPermissionMode(t, router, session.ID, `{"mode":"plan"}`)
	require.Equal(t, http.StatusBadRequest, plan.Code)
	require.Contains(t, plan.Body.String(), "plan")

	stored, err = manager.GetSession(ctx, session.ID)
	require.NoError(t, err)
	require.False(t, planmode.IsActive(planmode.Load(stored)))
}

func TestUpdateSessionPermissionModeClosesActivePlanState(t *testing.T) {
	ctx := context.Background()
	manager, router, session := newPermissionModeRouter(t)

	// 先进入 plan 模式（actor 不存活的空闲路径）。
	enter := httptest.NewRequest(http.MethodPost, "/api/runtime/sessions/"+session.ID+"/plan", strings.NewReader(`{"action":"enter"}`))
	enter.Header.Set("Content-Type", "application/json")
	enterRec := httptest.NewRecorder()
	router.ServeHTTP(enterRec, enter)
	require.Equal(t, http.StatusOK, enterRec.Code, enterRec.Body.String())

	rec := postPermissionMode(t, router, session.ID, `{"mode":"bypass_permissions"}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	var resp sessionPermissionModeResponse
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&resp))
	require.Equal(t, string(runtimepolicy.ModeBypassPermissions), resp.Mode)
	require.False(t, resp.PlanActive)

	stored, err := manager.GetSession(ctx, session.ID)
	require.NoError(t, err)
	state := planmode.Load(stored)
	require.False(t, planmode.IsActive(state))
	require.Equal(t, planmode.ExitQuit, state.ExitDecision)
	require.Equal(t, string(runtimepolicy.ModeBypassPermissions), sessionPermissionMode(stored))
	// plan 状态已 quit：权限真值以会话级模式为准（执行策略读它），
	// plan state 里保留的 previous_mode 只是「回到计划前模式」的记账值。
	require.Equal(t, string(runtimepolicy.ModeDefault), planmode.ResumeModeAfterExit(state))
}
