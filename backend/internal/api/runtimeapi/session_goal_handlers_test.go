package runtimeapi

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gorilla/mux"
	"github.com/stretchr/testify/require"

	"github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimegoal "github.com/wwsheng009/ai-agent-runtime/internal/goal"
)

func newSessionGoalAPIHarness(t *testing.T) (*Handler, *chat.SessionManager, *chat.Session) {
	t.Helper()
	handler := NewHandler(nil, nil, nil)
	sessionManager := chat.NewSessionManager(chat.NewInMemoryStorage(), nil)
	t.Cleanup(sessionManager.Stop)
	handler.SetSessionManager(sessionManager)
	session, err := sessionManager.Create(context.Background(), "user-goal-api")
	require.NoError(t, err)
	return handler, sessionManager, session
}

// callSessionGoalAPI 直接调用 handler（不经路由表），RemoteAddr 默认回环以通过
// 会话写端点授权（回环 / admin token / admin role）。
func callSessionGoalAPI(t *testing.T, handler *Handler, method, sessionID, body string) *httptest.ResponseRecorder {
	t.Helper()
	return callSessionGoalAPIFrom(t, handler, method, sessionID, body, "127.0.0.1:43210")
}

func callSessionGoalAPIFrom(t *testing.T, handler *Handler, method, sessionID, body, remoteAddr string) *httptest.ResponseRecorder {
	t.Helper()
	var reader io.Reader
	if strings.TrimSpace(body) != "" {
		reader = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, "/api/runtime/sessions/"+sessionID+"/goal", reader)
	req.RemoteAddr = remoteAddr
	req = mux.SetURLVars(req, map[string]string{"id": sessionID})
	rec := httptest.NewRecorder()
	if method == http.MethodGet {
		handler.GetSessionGoal(rec, req)
	} else {
		handler.UpdateSessionGoal(rec, req)
	}
	return rec
}

func decodeSessionGoalResponse(t *testing.T, rec *httptest.ResponseRecorder) sessionGoalMutationResponse {
	t.Helper()
	var resp sessionGoalMutationResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp), "body=%s", rec.Body.String())
	return resp
}

func decodeSessionGoalReadResponse(t *testing.T, rec *httptest.ResponseRecorder) sessionGoalResponse {
	t.Helper()
	var resp sessionGoalResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp), "body=%s", rec.Body.String())
	return resp
}

func TestSessionGoalAPI_Lifecycle(t *testing.T) {
	handler, _, session := newSessionGoalAPIHarness(t)
	id := session.ID

	// 初始无 goal：GET 返回 200 + goal=null（不是 404）。
	rec := callSessionGoalAPI(t, handler, http.MethodGet, id, "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Nil(t, decodeSessionGoalReadResponse(t, rec).Goal)

	// set
	rec = callSessionGoalAPI(t, handler, http.MethodPost, id, `{"action":"set","objective":"finish gap two","token_budget":1000}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	set := decodeSessionGoalResponse(t, rec)
	require.True(t, set.Applied)
	require.NotNil(t, set.Goal)
	require.Equal(t, "finish gap two", set.Goal.Objective)
	require.Equal(t, runtimegoal.StatusActive, set.Goal.Status)
	require.Equal(t, 1000, set.Goal.TokenBudget)

	// GET 反映落库状态
	rec = callSessionGoalAPI(t, handler, http.MethodGet, id, "")
	read := decodeSessionGoalReadResponse(t, rec)
	require.NotNil(t, read.Goal)
	require.Equal(t, set.Goal.GoalID, read.Goal.GoalID)
	require.EqualValues(t, 1000, read.RemainingTokens)

	// pause / resume
	rec = callSessionGoalAPI(t, handler, http.MethodPost, id, `{"action":"pause"}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Equal(t, runtimegoal.StatusPaused, decodeSessionGoalResponse(t, rec).Goal.Status)

	rec = callSessionGoalAPI(t, handler, http.MethodPost, id, `{"action":"resume"}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Equal(t, runtimegoal.StatusActive, decodeSessionGoalResponse(t, rec).Goal.Status)

	// budget 调整
	rec = callSessionGoalAPI(t, handler, http.MethodPost, id, `{"action":"budget","token_budget":250}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Equal(t, 250, decodeSessionGoalResponse(t, rec).Goal.TokenBudget)

	// complete（用户）：完成字段落库
	rec = callSessionGoalAPI(t, handler, http.MethodPost, id, `{"action":"complete","summary":"verified end-to-end"}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	done := decodeSessionGoalResponse(t, rec)
	require.True(t, done.Applied)
	require.Equal(t, runtimegoal.StatusComplete, done.Goal.Status)
	require.Equal(t, "user", done.Goal.CompletedBy)
	require.Equal(t, "verified end-to-end", done.Goal.CompletionSummary)

	// clear
	rec = callSessionGoalAPI(t, handler, http.MethodPost, id, `{"action":"clear"}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	cleared := decodeSessionGoalResponse(t, rec)
	require.True(t, cleared.Applied)
	require.Nil(t, cleared.Goal)

	rec = callSessionGoalAPI(t, handler, http.MethodGet, id, "")
	require.Nil(t, decodeSessionGoalReadResponse(t, rec).Goal)
}

func TestSessionGoalAPI_ValidationErrors(t *testing.T) {
	handler, _, session := newSessionGoalAPIHarness(t)
	id := session.ID

	cases := []struct {
		name   string
		body   string
		status int
	}{
		{"missing action", `{}`, http.StatusBadRequest},
		{"unknown action", `{"action":"explode"}`, http.StatusBadRequest},
		{"set without objective", `{"action":"set"}`, http.StatusBadRequest},
		{"set negative budget", `{"action":"set","objective":"x","token_budget":-1}`, http.StatusBadRequest},
		{"pause without goal", `{"action":"pause"}`, http.StatusNotFound},
		{"resume without goal", `{"action":"resume"}`, http.StatusNotFound},
		{"complete without goal", `{"action":"complete"}`, http.StatusNotFound},
		{"budget without goal", `{"action":"budget","token_budget":10}`, http.StatusNotFound},
		{"invalid json", `{"action":`, http.StatusBadRequest},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := callSessionGoalAPI(t, handler, http.MethodPost, id, tc.body)
			require.Equal(t, tc.status, rec.Code, rec.Body.String())
		})
	}

	// 有 goal 后 budget 仍要求显式 token_budget。
	rec := callSessionGoalAPI(t, handler, http.MethodPost, id, `{"action":"set","objective":"x"}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	rec = callSessionGoalAPI(t, handler, http.MethodPost, id, `{"action":"budget"}`)
	require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())

	// 未知会话：会话存储层映射 404。
	rec = callSessionGoalAPI(t, handler, http.MethodGet, "session-does-not-exist", "")
	require.Equal(t, http.StatusNotFound, rec.Code, rec.Body.String())
}

// TestSessionGoalAPI_CompleteIsTerminal：complete 是终态——不能回退，但可以用
// 新 objective（新 GoalID）设置新目标（与 CLI `/goal <objective>` 同口径）。
func TestSessionGoalAPI_CompleteIsTerminal(t *testing.T) {
	handler, _, session := newSessionGoalAPIHarness(t)
	id := session.ID

	require.Equal(t, http.StatusOK, callSessionGoalAPI(t, handler, http.MethodPost, id, `{"action":"set","objective":"first"}`).Code)
	require.Equal(t, http.StatusOK, callSessionGoalAPI(t, handler, http.MethodPost, id, `{"action":"complete"}`).Code)

	rec := callSessionGoalAPI(t, handler, http.MethodPost, id, `{"action":"pause"}`)
	require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())

	rec = callSessionGoalAPI(t, handler, http.MethodPost, id, `{"action":"set","objective":"second"}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	replaced := decodeSessionGoalResponse(t, rec)
	require.True(t, replaced.Applied, "a new objective must replace a completed goal")
	require.Equal(t, "second", replaced.Goal.Objective)
	require.Equal(t, runtimegoal.StatusActive, replaced.Goal.Status)
}

// TestSessionGoalAPI_BudgetLimitedResumeNeedsRaise：budget_limited → active 仅在
// 预算提高到超过已用时成立（GoalMergePolicy 守卫，与 CLI 同口径）。
func TestSessionGoalAPI_BudgetLimitedResumeNeedsRaise(t *testing.T) {
	handler, sessionManager, session := newSessionGoalAPIHarness(t)
	id := session.ID

	limited := runtimegoal.SessionGoal{
		GoalID:      "goal_budget_limited",
		SessionID:   id,
		Objective:   "budgeted work",
		Status:      runtimegoal.StatusBudgetLimited,
		TokenBudget: 50,
		TokensUsed:  120,
	}
	require.NoError(t, runtimegoal.NewMetadataStore().Put(session, limited))
	require.NoError(t, sessionManager.Update(context.Background(), session))

	rec := callSessionGoalAPI(t, handler, http.MethodPost, id, `{"action":"resume"}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	blocked := decodeSessionGoalResponse(t, rec)
	require.False(t, blocked.Applied, "resume without raising the budget must not revive a budget_limited goal")
	require.Equal(t, runtimegoal.StatusBudgetLimited, blocked.Goal.Status)
	require.Contains(t, blocked.Message, "预算未提高到超过已用")

	// 提高预算到超过已用 → 自动恢复 active。
	rec = callSessionGoalAPI(t, handler, http.MethodPost, id, `{"action":"budget","token_budget":200}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	raised := decodeSessionGoalResponse(t, rec)
	require.True(t, raised.Applied)
	require.Equal(t, runtimegoal.StatusActive, raised.Goal.Status)
	require.Equal(t, 200, raised.Goal.TokenBudget)
}

// TestSessionGoalAPI_WriteRequiresAuthorization：写端点沿用会话写授权
// （回环 / admin token / admin role），非回环无凭证 = 403；GET 只读不受影响。
func TestSessionGoalAPI_WriteRequiresAuthorization(t *testing.T) {
	handler, _, session := newSessionGoalAPIHarness(t)
	id := session.ID

	rec := callSessionGoalAPIFrom(t, handler, http.MethodPost, id, `{"action":"set","objective":"x"}`, "203.0.113.7:5555")
	require.Equal(t, http.StatusForbidden, rec.Code, rec.Body.String())

	rec = callSessionGoalAPIFrom(t, handler, http.MethodGet, id, "", "203.0.113.7:5555")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
}

// TestSessionGoalAPI_RoutesRegistered 经真实路由表验证
// GET/POST /api/runtime/sessions/{id}/goal 已注册（直接调用 handler 的用例不覆盖
// 路由注册，正是本测试的价值）。
func TestSessionGoalAPI_RoutesRegistered(t *testing.T) {
	handler, _, session := newSessionGoalAPIHarness(t)
	router := mux.NewRouter()
	handler.RegisterRoutes(router)

	getReq := httptest.NewRequest(http.MethodGet, "/api/runtime/sessions/"+session.ID+"/goal", nil)
	getReq.RemoteAddr = "127.0.0.1:43210"
	getRec := httptest.NewRecorder()
	router.ServeHTTP(getRec, getReq)
	require.Equal(t, http.StatusOK, getRec.Code, getRec.Body.String())
	require.Nil(t, decodeSessionGoalReadResponse(t, getRec).Goal)

	postReq := httptest.NewRequest(http.MethodPost, "/api/runtime/sessions/"+session.ID+"/goal",
		strings.NewReader(`{"action":"set","objective":"route smoke"}`))
	postReq.RemoteAddr = "127.0.0.1:43210"
	postReq.Header.Set("Content-Type", "application/json")
	postRec := httptest.NewRecorder()
	router.ServeHTTP(postRec, postReq)
	require.Equal(t, http.StatusOK, postRec.Code, postRec.Body.String())
	require.Equal(t, "route smoke", decodeSessionGoalResponse(t, postRec).Goal.Objective)
}
