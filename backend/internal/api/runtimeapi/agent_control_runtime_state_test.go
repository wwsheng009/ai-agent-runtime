package runtimeapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gorilla/mux"
	"github.com/stretchr/testify/require"
	"github.com/wwsheng009/ai-agent-runtime/internal/chat"
	"github.com/wwsheng009/ai-agent-runtime/internal/skill"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolbroker"
)

// 本次修复的核心回归：子代理跑完（容器 idle）后，兼容投影**必须**如实上报
// `runtime_state=idle`，同时**不得**改写身份状态（仍是 active，直到有人显式
// close）。否则面板会一直显示「运行中」，直到主代理调用 close_agent。
func TestListAgentControlAgentsReportsIdleRuntimeStateForFinishedChild(t *testing.T) {
	ctx := context.Background()
	handler, sessionManager := newAgentRuntimeStateHandler(t)
	runtimeStore := chat.NewInMemoryRuntimeStore(64)
	handler.sessionRuntimeStore = runtimeStore
	handler.sessionRuntimeStoreKey = "test-runtime.sqlite|"

	root, err := sessionManager.Create(ctx, "user-runtime-state")
	require.NoError(t, err)
	child := newRuntimeStateTestChild(t, sessionManager, root.ID, "finished-child", "/root/finished-child")
	require.NoError(t, runtimeStore.SaveState(ctx, &chat.RuntimeState{
		SessionID: child.ID,
		Status:    chat.SessionIdle,
		UpdatedAt: time.Now().UTC(),
	}))

	payload := requestAgentControlAgents(t, handler, root.ID, "/root/finished-child")
	require.Equal(t, 1, payload.Count)
	require.Len(t, payload.Agents, 1)
	// 身份状态不变：容器结束 ≠ 身份关闭（close 仍是显式动作）。
	require.Equal(t, "active", payload.Agents[0].Status)
	// 运行态如实上报：面板据此把该行放进「已结束」，而不是「运行中」。
	require.Equal(t, AgentRuntimeStateIdle, payload.Agents[0].RuntimeState)
}

func TestListAgentControlAgentsReportsRunningRuntimeStateFromDurableStore(t *testing.T) {
	ctx := context.Background()
	handler, sessionManager := newAgentRuntimeStateHandler(t)
	runtimeStore := chat.NewInMemoryRuntimeStore(64)
	handler.sessionRuntimeStore = runtimeStore
	handler.sessionRuntimeStoreKey = "test-runtime.sqlite|"

	root, err := sessionManager.Create(ctx, "user-runtime-state")
	require.NoError(t, err)
	child := newRuntimeStateTestChild(t, sessionManager, root.ID, "busy-child", "/root/busy-child")
	// 跨进程场景：容器状态由真正执行该会话的宿主写入，本进程没有 actor。
	require.NoError(t, runtimeStore.SaveState(ctx, &chat.RuntimeState{
		SessionID: child.ID,
		Status:    chat.SessionWaitingApproval,
		UpdatedAt: time.Now().UTC(),
	}))

	payload := requestAgentControlAgents(t, handler, root.ID, "/root/busy-child")
	require.Len(t, payload.Agents, 1)
	require.Equal(t, "active", payload.Agents[0].Status)
	// 等待审批仍算「容器在跑」，不能因为会话行不是 running 就当作已结束。
	require.Equal(t, AgentRuntimeStateRunning, payload.Agents[0].RuntimeState)
}

func TestListAgentControlAgentsOmitsRuntimeStateWithoutDurableStore(t *testing.T) {
	ctx := context.Background()
	handler, sessionManager := newAgentRuntimeStateHandler(t)
	// 内存兜底 store：对本进程外执行过的会话只会给出空白，因此不做断言。
	handler.sessionRuntimeStore = chat.NewInMemoryRuntimeStore(64)
	handler.sessionRuntimeStoreKey = agentRuntimeMemoryStoreKey

	root, err := sessionManager.Create(ctx, "user-runtime-state")
	require.NoError(t, err)
	_ = newRuntimeStateTestChild(t, sessionManager, root.ID, "unasserted-child", "/root/unasserted-child")

	payload := requestAgentControlAgents(t, handler, root.ID, "/root/unasserted-child")
	require.Len(t, payload.Agents, 1)
	// 无持久运行态证据 → 字段省略（前端回退身份状态），**不臆断「已结束」**。
	require.Empty(t, payload.Agents[0].RuntimeState)
}

func newAgentRuntimeStateHandler(t *testing.T) (*Handler, *chat.SessionManager) {
	t.Helper()
	handler := NewHandler(skill.NewRegistry(nil), nil, nil)
	sessionManager := chat.NewSessionManager(chat.NewInMemoryStorage(), nil)
	t.Cleanup(sessionManager.Stop)
	handler.SetSessionManager(sessionManager)
	return handler, sessionManager
}

func newRuntimeStateTestChild(t *testing.T, sessionManager *chat.SessionManager, rootSessionID, childID, agentPath string) *chat.Session {
	t.Helper()
	ctx := context.Background()
	child := chat.NewSession("user-runtime-state")
	child.ID = childID
	child.SetContext(toolbroker.AgentSessionContextParentSessionID, rootSessionID)
	child.SetContext(toolbroker.AgentSessionContextRootSessionID, rootSessionID)
	child.SetContext(toolbroker.AgentSessionContextPath, agentPath)
	child.SetContext(toolbroker.AgentSessionContextDepth, 1)
	child.SetContext(toolbroker.AgentSessionContextAgentType, "researcher")
	require.NoError(t, sessionManager.GetStorage().Save(ctx, child))
	return child
}

type agentControlRuntimeStatePayload struct {
	Agents []struct {
		AgentID      string `json:"agent_id"`
		SessionID    string `json:"session_id"`
		Status       string `json:"status"`
		RuntimeState string `json:"runtime_state"`
	} `json:"agents"`
	Count  int    `json:"count"`
	Source string `json:"source"`
}

func requestAgentControlAgents(t *testing.T, handler *Handler, rootSessionID, pathPrefix string) agentControlRuntimeStatePayload {
	t.Helper()
	router := mux.NewRouter()
	handler.RegisterRoutes(router)
	req := httptest.NewRequest(http.MethodGet, "/api/runtime/agent-control/agents?root_session_id="+rootSessionID+"&path_prefix="+pathPrefix, nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)

	var payload agentControlRuntimeStatePayload
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &payload))
	return payload
}
