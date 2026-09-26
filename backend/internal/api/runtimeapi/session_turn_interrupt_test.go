package runtimeapi

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/mux"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wwsheng009/ai-agent-runtime/internal/chat"
	"github.com/wwsheng009/ai-agent-runtime/internal/llm"
	"github.com/wwsheng009/ai-agent-runtime/internal/skill"
)

// decodeCommandPayload 反序列化 /runtime/commands 的 JSON 响应体。
func decodeCommandPayload(t *testing.T, rec *httptest.ResponseRecorder) map[string]interface{} {
	t.Helper()
	payload := map[string]interface{}{}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &payload), "响应体：%s", rec.Body.String())
	return payload
}

// postSessionRuntimeCommand 走与生产一致的路径变量语义提交一次 runtime 命令。
func postSessionRuntimeCommand(t *testing.T, handler *Handler, sessionID, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost,
		"/api/runtime/sessions/"+sessionID+"/runtime/commands", strings.NewReader(body))
	req = mux.SetURLVars(req, map[string]string{"id": sessionID})
	rec := httptest.NewRecorder()
	handler.SubmitSessionRuntimeCommand(rec, req)
	return rec
}

// TestActiveTurnRegistry_CancelReasonMatrix 锁定建议 3 的取消判定矩阵。
//
// 四种「没能取消」必须可区分，因为调用方下一步动作不同：no_active_turn /
// not_cancelable 要回退 durable actor 路径；already_cancelled 属于成功语义
// （重复 stop 不该报错）；turn_mismatch 必须拒绝（迟到的 stop 不能误杀新回合）。
func TestActiveTurnRegistry_CancelReasonMatrix(t *testing.T) {
	registry := newActiveTurnRegistry()

	// 1) 没有在途回合 → no_active_turn，且不带任何回合身份（不能编造 turn_id）。
	result := registry.cancel("sess-1", "", activeTurnCancelSourceUserInterrupt)
	assert.False(t, result.Cancelled)
	assert.Equal(t, activeTurnCancelReasonNoActiveTurn, result.Reason)
	assert.Empty(t, result.Turn.TurnID)

	// 2) 只登记不可取消（历史 begin 调用方）→ not_cancelable，仍回带回合身份。
	releaseUncancelable := registry.begin("sess-1", "turn-1", agentChatActiveTurnSource, true)
	result = registry.cancel("sess-1", "turn-1", activeTurnCancelSourceUserInterrupt)
	assert.False(t, result.Cancelled)
	assert.Equal(t, activeTurnCancelReasonNotCancelable, result.Reason)
	assert.Equal(t, "turn-1", result.Turn.TurnID)
	releaseUncancelable()

	// 3) 可取消回合：首次 stop → cancelled，来源写入快照（刷新后的页面据此显示
	//    「已请求停止、等收尾」）。
	fired := make(chan string, 4)
	release := registry.beginCancelable("sess-1", "turn-2", agentChatActiveTurnSource, true, func(source string) bool {
		fired <- source
		return true
	})
	result = registry.cancel("sess-1", "turn-2", activeTurnCancelSourceUserInterrupt)
	assert.True(t, result.Cancelled)
	assert.Equal(t, activeTurnCancelReasonCancelled, result.Reason)
	assert.Equal(t, activeTurnCancelSourceUserInterrupt, result.Turn.CancelSource)
	assert.Equal(t, activeTurnCancelSourceUserInterrupt, <-fired)

	snapshot, ok := registry.get("sess-1")
	require.True(t, ok)
	assert.Equal(t, activeTurnCancelSourceUserInterrupt, snapshot.CancelSource)

	// 4) 同一回合重复 stop → already_cancelled（幂等），且不再触发第二次真实取消。
	result = registry.cancel("sess-1", "turn-2", activeTurnCancelSourceUserInterrupt)
	assert.False(t, result.Cancelled)
	assert.Equal(t, activeTurnCancelReasonAlreadyCancelled, result.Reason)
	assert.Len(t, fired, 0, "重复取消不得再触发一次取消")

	// 5) 过期 turn_id 打到新回合上 → turn_mismatch，绝不能误杀新回合。
	var newTurnFired atomic.Int32
	releaseNew := registry.beginCancelable("sess-1", "turn-3", agentChatActiveTurnSource, true, func(string) bool {
		newTurnFired.Add(1)
		return true
	})
	result = registry.cancel("sess-1", "turn-2", activeTurnCancelSourceUserInterrupt)
	assert.False(t, result.Cancelled)
	assert.Equal(t, activeTurnCancelReasonTurnMismatch, result.Reason)
	assert.Zero(t, newTurnFired.Load(), "过期 turn_id 不得取消新回合")
	snapshot, ok = registry.get("sess-1")
	require.True(t, ok)
	assert.Equal(t, "turn-3", snapshot.TurnID)

	// 6) 被覆盖回合的 release 不误删新条目（身份不匹配即跳过）。
	release()
	snapshot, ok = registry.get("sess-1")
	require.True(t, ok, "旧回合 release 不应删掉新登记的回合")
	assert.Equal(t, "turn-3", snapshot.TurnID)

	releaseNew()
	_, ok = registry.get("sess-1")
	assert.False(t, ok, "回合收尾后应释放登记")
}

// TestSubmitSessionRuntimeCommand_InterruptContract 覆盖 interrupt 命令的 HTTP 契约：
// 注册表命中/幂等/过期 turn_id 拒绝，以及没有在途回合时回退 durable actor 并如实
// 报告 no_active_turn（不把「本来就空闲」谎报成「已停止」）。
func TestSubmitSessionRuntimeCommand_InterruptContract(t *testing.T) {
	handler := NewHandler(skill.NewRegistry(nil), nil, nil)
	sessionManager := chat.NewSessionManager(chat.NewInMemoryStorage(), nil)
	defer sessionManager.Stop()
	handler.SetSessionManager(sessionManager)

	provider := newResumeDisconnectStreamProvider("test-interrupt-contract-model")
	llmRuntime := llm.NewLLMRuntime(&llm.RuntimeConfig{DefaultModel: provider.Name(), MaxRetries: 0})
	require.NoError(t, llmRuntime.RegisterProvider(provider.Name(), provider))
	handler.SetLLMRuntime(llmRuntime)

	session, err := sessionManager.Create(context.Background(), "user-interrupt-contract")
	require.NoError(t, err)

	var firedCount atomic.Int32
	release := handler.getActiveTurnRegistry().beginCancelable(session.ID, "turn-A", agentChatActiveTurnSource, true, func(string) bool {
		firedCount.Add(1)
		return true
	})
	defer release()

	// 1) 过期 turn_id → 409，且不得取消当前在途回合。
	rec := postSessionRuntimeCommand(t, handler, session.ID, `{"type":"interrupt","turn_id":"turn-stale"}`)
	require.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())
	assert.Zero(t, firedCount.Load(), "过期 turn_id 不得触发取消")

	// 2) 命中在途回合 → 200 + cancelled，带上回合身份与取消来源。
	rec = postSessionRuntimeCommand(t, handler, session.ID, `{"type":"interrupt","turn_id":"turn-A"}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	payload := decodeCommandPayload(t, rec)
	assert.Equal(t, true, payload["ok"])
	assert.Equal(t, true, payload["cancelled"])
	assert.Equal(t, string(activeTurnCancelReasonCancelled), payload["reason"])
	assert.Equal(t, sessionTurnInterruptChannelActiveTurn, payload["channel"])
	assert.Equal(t, "turn-A", payload["turn_id"])
	assert.Equal(t, activeTurnCancelSourceUserInterrupt, payload["cancel_source"])
	assert.Equal(t, int32(1), firedCount.Load())

	// 3) 重复 stop（不带 turn_id）→ 200 + already_cancelled，不再触发取消。
	rec = postSessionRuntimeCommand(t, handler, session.ID, `{"type":"interrupt"}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	payload = decodeCommandPayload(t, rec)
	assert.Equal(t, false, payload["cancelled"])
	assert.Equal(t, string(activeTurnCancelReasonAlreadyCancelled), payload["reason"])
	assert.Equal(t, "turn-A", payload["turn_id"])
	assert.Equal(t, int32(1), firedCount.Load(), "重复 stop 不得再取消一次")

	// 4) 回合收尾释放后：回退 durable actor 路径，idle actor 如实报告 no_active_turn。
	release()
	rec = postSessionRuntimeCommand(t, handler, session.ID, `{"type":"interrupt"}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	payload = decodeCommandPayload(t, rec)
	assert.Equal(t, false, payload["cancelled"], "空闲 actor 不得谎报已停止")
	assert.Equal(t, string(activeTurnCancelReasonNoActiveTurn), payload["reason"])
	assert.Equal(t, sessionTurnInterruptChannelSessionActor, payload["channel"])
	assert.NotContains(t, payload, "turn_id", "无在途回合时不应回带回合身份")
}

// TestAgentChatDetachedTurn_InterruptStopsServerRun 是建议 3 的核心集成证据：
// detached（resume_on_disconnect）回合在客户端已断开后仍在服务端跑，此时
// interrupt 命令必须真正取消它 —— 取消要传播到 run 的 ctx（provider 侧可见
// context.Canceled），而不是只停掉「本页接收」。
func TestAgentChatDetachedTurn_InterruptStopsServerRun(t *testing.T) {
	const (
		model  = "test-model"
		turnID = "turn_interrupt_detached"
	)

	mcpManager := &testMCPManager{}
	handler := NewHandler(skill.NewRegistry(mcpManager), nil, mcpManager)

	provider := newResumeDisconnectStreamProvider(model)
	llmRuntime := llm.NewLLMRuntime(&llm.RuntimeConfig{DefaultModel: model, MaxRetries: 0})
	require.NoError(t, llmRuntime.RegisterProvider(model, provider))
	handler.SetLLMRuntime(llmRuntime)

	sessionManager := chat.NewSessionManager(chat.NewInMemoryStorage(), nil)
	defer sessionManager.Stop()
	handler.SetSessionManager(sessionManager)

	session, err := sessionManager.Create(context.Background(), "user-interrupt-detached")
	require.NoError(t, err)

	router := mux.NewRouter()
	handler.RegisterRoutes(router)
	server := httptest.NewServer(router)
	defer server.Close()

	body, err := json.Marshal(map[string]interface{}{
		"messages":             []map[string]string{{"role": "user", "content": "hi"}},
		"enable_react":         true,
		"stream":               true,
		"resume_on_disconnect": true,
		"session_id":           session.ID,
		"turn_id":              turnID,
	})
	require.NoError(t, err)

	requestCtx, cancelRequest := context.WithCancel(context.Background())
	defer cancelRequest()
	request, err := http.NewRequestWithContext(requestCtx, http.MethodPost, server.URL+"/api/agent/chat", bytes.NewReader(body))
	require.NoError(t, err)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "text/event-stream")

	response, err := server.Client().Do(request)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, response.StatusCode)

	go func() {
		_, _ = io.Copy(io.Discard, response.Body)
	}()

	// 等 run 真正进入 provider：此后「在途窗口」内取消才有意义（provider 在
	// 分片间隔内不会自行检查 ctx，取消到达时会稳定地留下 context.Canceled）。
	select {
	case <-provider.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("run 未在超时内进入 provider")
	}

	// 模拟浏览器刷新：abort 在途 POST。resume_on_disconnect 下 run 不受影响
	// （这正是「detached 回合没有停止按钮」的起点）。
	cancelRequest()
	_ = response.Body.Close()

	// 通过运行时命令接口下发停止：200 + cancelled，且标明取消打到了哪个通道/回合。
	rec := postSessionRuntimeCommand(t, handler, session.ID,
		`{"type":"interrupt","turn_id":"`+turnID+`"}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	payload := decodeCommandPayload(t, rec)
	assert.Equal(t, true, payload["cancelled"], "detached 回合应被真正取消")
	assert.Equal(t, string(activeTurnCancelReasonCancelled), payload["reason"])
	assert.Equal(t, sessionTurnInterruptChannelActiveTurn, payload["channel"])
	assert.Equal(t, turnID, payload["turn_id"])
	assert.Equal(t, activeTurnCancelSourceUserInterrupt, payload["cancel_source"])

	// 取消必须传播到 run：provider 收尾时观测到 context.Canceled（而不是 nil ——
	// nil 就意味着「只停了本页接收，服务端还在跑」）。
	select {
	case ctxErr := <-provider.ctxErrCh:
		assert.ErrorIs(t, ctxErr, context.Canceled, "interrupt 必须取消 run 的 ctx")
	case <-time.After(5 * time.Second):
		t.Fatal("provider 未在超时内收尾，run 可能未被取消")
	}

	// 回合收尾后释放登记：刷新后的页面不应再看到在途回合。
	waitForResumeCondition(t, 5*time.Second, func() bool {
		_, stillRunning := handler.getActiveTurnRegistry().get(session.ID)
		return !stillRunning
	}, "取消收尾后 active_turn 应被释放")
}
