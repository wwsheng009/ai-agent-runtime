package runtimeapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
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

// waitForResumeCondition 带 deadline 轮询条件，替代固定 sleep（避免 sleep 竞态导致 flaky）。
func waitForResumeCondition(t *testing.T, timeout time.Duration, condition func() bool, message string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("等待超时（%s）：%s", timeout, message)
}

// TestActiveTurnRegistry_BeginReleaseSemantics 覆盖 activeTurnRegistry 的纯语义：
// begin/get 字段、release 幂等、覆盖时旧 release 不误删、空标识安全。
func TestActiveTurnRegistry_BeginReleaseSemantics(t *testing.T) {
	t.Run("begin 后 get 命中且字段正确", func(t *testing.T) {
		registry := newActiveTurnRegistry()
		release := registry.begin("sess-basic", "turn-basic", agentChatActiveTurnSource, true)
		t.Cleanup(release)

		entry, ok := registry.get("sess-basic")
		require.True(t, ok)
		assert.Equal(t, "sess-basic", entry.SessionID)
		assert.Equal(t, "turn-basic", entry.TurnID)
		assert.Equal(t, agentChatActiveTurnSource, entry.Source)
		assert.True(t, entry.Detached)
		assert.False(t, entry.StartedAt.IsZero())

		// begin 会 trim 空白；get 同样按 trim 后的键命中。
		trimmed := registry.begin(" sess-trim ", " turn-trim ", agentChatActiveTurnSource, false)
		t.Cleanup(trimmed)
		entry, ok = registry.get("sess-trim")
		require.True(t, ok)
		assert.Equal(t, "sess-trim", entry.SessionID)
		assert.Equal(t, "turn-trim", entry.TurnID)
		assert.False(t, entry.Detached)
	})

	t.Run("release 幂等且调用后 get 不再命中", func(t *testing.T) {
		registry := newActiveTurnRegistry()
		release := registry.begin("sess-idempotent", "turn-idempotent", agentChatActiveTurnSource, true)

		release()
		release()
		release()

		_, ok := registry.get("sess-idempotent")
		assert.False(t, ok)
	})

	t.Run("旧 release 不得删除后开始的回合", func(t *testing.T) {
		registry := newActiveTurnRegistry()
		releaseOld := registry.begin("sess-override", "turn-old", agentChatActiveTurnSource, false)
		releaseNew := registry.begin("sess-override", "turn-new", agentChatActiveTurnSource, true)

		// 旧回合的 release 身份不匹配：不能误清新条目。
		releaseOld()

		entry, ok := registry.get("sess-override")
		require.True(t, ok, "旧 release 不应删除新登记的回合")
		assert.Equal(t, "turn-new", entry.TurnID)
		assert.True(t, entry.Detached)

		// 新回合自己的 release 仍然有效。
		releaseNew()
		_, ok = registry.get("sess-override")
		assert.False(t, ok)
	})

	t.Run("空 sessionID/turnID 的 release 可安全调用", func(t *testing.T) {
		registry := newActiveTurnRegistry()

		emptySession := registry.begin("", "turn-x", agentChatActiveTurnSource, true)
		emptySession()
		emptySession()

		emptyTurn := registry.begin("sess-empty-turn", "   ", agentChatActiveTurnSource, true)
		emptyTurn()
		emptyTurn()
		_, ok := registry.get("sess-empty-turn")
		assert.False(t, ok)

		// 空键查询不命中。
		_, ok = registry.get("  ")
		assert.False(t, ok)

		// 零值注册表（turns 为 nil）也能 begin/get，内部会惰性建 map。
		zero := &activeTurnRegistry{}
		releaseZero := zero.begin("sess-zero", "turn-zero", agentChatActiveTurnSource, false)
		entry, ok := zero.get("sess-zero")
		require.True(t, ok)
		assert.Equal(t, "turn-zero", entry.TurnID)
		releaseZero()
		_, ok = zero.get("sess-zero")
		assert.False(t, ok)
	})

	t.Run("Handler 懒初始化返回同一注册表", func(t *testing.T) {
		handler := &Handler{}
		first := handler.getActiveTurnRegistry()
		second := handler.getActiveTurnRegistry()
		require.NotNil(t, first)
		assert.Same(t, first, second)
	})
}

// TestGetSessionRuntimeState_ExposesActiveTurn 通过真实路由验证
// GET /api/runtime/sessions/{id}/runtime 的 active_turn 字段：
// 登记后在途回合可见，release 后为 null。
func TestGetSessionRuntimeState_ExposesActiveTurn(t *testing.T) {
	handler := NewHandler(skill.NewRegistry(nil), nil, nil)
	sessionManager := chat.NewSessionManager(chat.NewInMemoryStorage(), nil)
	defer sessionManager.Stop()
	handler.SetSessionManager(sessionManager)

	session, err := sessionManager.Create(context.Background(), "user-active-turn-runtime-state")
	require.NoError(t, err)

	router := mux.NewRouter()
	handler.RegisterRoutes(router)

	const turnID = "turn_runtime_state_visible"
	release := handler.getActiveTurnRegistry().begin(session.ID, turnID, agentChatActiveTurnSource, true)

	// begin 后 → 200，active_turn 等于登记的回合。
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/runtime/sessions/"+session.ID+"/runtime", nil))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	var payload map[string]interface{}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &payload))
	activeTurn, ok := payload["active_turn"].(map[string]interface{})
	require.True(t, ok, "active_turn 应为对象，实际为 %v", payload["active_turn"])
	assert.Equal(t, turnID, activeTurn["turn_id"])
	assert.Equal(t, session.ID, activeTurn["session_id"])
	assert.Equal(t, agentChatActiveTurnSource, activeTurn["source"])
	assert.Equal(t, true, activeTurn["detached"])
	assert.NotEmpty(t, activeTurn["started_at"])

	// release 后 → 200，active_turn 显式为 null（注意换一份 map 反序列化，避免旧键残留）。
	release()
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/runtime/sessions/"+session.ID+"/runtime", nil))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	var releasedPayload map[string]interface{}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &releasedPayload))
	assert.Nil(t, releasedPayload["active_turn"], "release 后 active_turn 应为 null")
}

const (
	// resumeDisconnectChunkCount/Interval 模拟流式生成的在途窗口：5 个分片 × 50ms。
	resumeDisconnectChunkCount    = 5
	resumeDisconnectChunkInterval = 50 * time.Millisecond
)

// resumeDisconnectStreamProvider 是「刷新续传」集成测试专用 provider：
// 每次生成先 close(entered) 通知测试「run 已进入 provider」，随后在 5 个分片
// 间隔内在途生成，结束回传 provider 侧观察到的 ctx.Err()（nil 表示 run 未被
// 请求取消连带取消），并 close(finished) 标记本次生成收尾。
//
// 注意：ReAct 循环固定走 LLMRuntime.Call → Provider.Call，真实 provider 的增量
// 分片正是 Call 内部消费上游 SSE 后经 stream reporter 上报的（见 llm 包
// ProviderWrapper.Call），因此「在途生成窗口」必须放在 Call 中；Stream 保持同样
// 行为以完整实现 llm.Provider 契约。
type resumeDisconnectStreamProvider struct {
	name string

	enteredOnce  sync.Once
	entered      chan struct{}
	finishedOnce sync.Once
	finished     chan struct{}
	// ctxErrCh 缓冲 1：只保留首次生成的 ctx.Err() 观测值。
	ctxErrCh chan error

	callCalls atomic.Int32
}

func newResumeDisconnectStreamProvider(name string) *resumeDisconnectStreamProvider {
	return &resumeDisconnectStreamProvider{
		name:     name,
		entered:  make(chan struct{}),
		finished: make(chan struct{}),
		ctxErrCh: make(chan error, 1),
	}
}

func (p *resumeDisconnectStreamProvider) Name() string { return p.name }

func (p *resumeDisconnectStreamProvider) Call(ctx context.Context, req *llm.LLMRequest) (*llm.LLMResponse, error) {
	p.callCalls.Add(1)
	p.signalEntered()
	p.simulateInFlightGeneration(ctx)
	return &llm.LLMResponse{Content: "resume-disconnect-final", Model: p.name}, nil
}

func (p *resumeDisconnectStreamProvider) Stream(ctx context.Context, req *llm.LLMRequest) (<-chan llm.StreamChunk, error) {
	p.signalEntered()
	// 通道带缓冲：即使消费者（客户端断开后）提前退出，provider goroutine 也不会永久阻塞。
	stream := make(chan llm.StreamChunk, resumeDisconnectChunkCount+1)
	go func() {
		defer close(stream)
		for index := 0; index < resumeDisconnectChunkCount; index++ {
			time.Sleep(resumeDisconnectChunkInterval)
			stream <- llm.StreamChunk{
				Type:     llm.EventTypeText,
				Content:  fmt.Sprintf("chunk-%d", index+1),
				Done:     index == resumeDisconnectChunkCount-1,
				Sequence: uint64(index + 1),
			}
		}
		p.finishGeneration(ctx)
	}()
	return stream, nil
}

func (p *resumeDisconnectStreamProvider) signalEntered() {
	p.enteredOnce.Do(func() { close(p.entered) })
}

// simulateInFlightGeneration 阻塞 resumeDisconnectChunkCount 个分片间隔，模拟
// 「客户端断开时 run 仍在生成」的窗口。这里刻意不监听 ctx.Done：真实 provider
// 的上游调用也不会因请求取消回滚已产生的分片，测试才能在收尾时读到一个稳定的
// ctx.Err()。
func (p *resumeDisconnectStreamProvider) simulateInFlightGeneration(ctx context.Context) {
	for index := 0; index < resumeDisconnectChunkCount; index++ {
		time.Sleep(resumeDisconnectChunkInterval)
	}
	p.finishGeneration(ctx)
}

func (p *resumeDisconnectStreamProvider) finishGeneration(ctx context.Context) {
	select {
	case p.ctxErrCh <- ctx.Err():
	default:
	}
	p.finishedOnce.Do(func() { close(p.finished) })
}

func (p *resumeDisconnectStreamProvider) CountTokens(text string) int { return len(text) }

func (p *resumeDisconnectStreamProvider) GetCapabilities() *llm.ModelCapabilities {
	return &llm.ModelCapabilities{SupportsTools: true, SupportsStreaming: true}
}

func (p *resumeDisconnectStreamProvider) CheckHealth(ctx context.Context) error { return nil }

// TestAgentChat_ResumeOnDisconnect_ContinuesAfterClientAbort 是「页面刷新后 SSE live
// 续传」的核心集成测试：resume_on_disconnect=true 时，客户端 abort 在途 SSE 不应
// 连带取消 ReAct run；run 正常收尾并释放 active_turn。
func TestAgentChat_ResumeOnDisconnect_ContinuesAfterClientAbort(t *testing.T) {
	const (
		model  = "test-model"
		turnID = "turn_resume_on_disconnect_fixed"
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

	// 先建会话再带上固定 session_id 请求：GetOrCreate 会复用该会话（而非另建新 id），
	// 注册表键即 session.ID，测试可在断开前后精确查询。
	session, err := sessionManager.Create(context.Background(), "user-resume-on-disconnect")
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
	require.Contains(t, response.Header.Get("Content-Type"), "text/event-stream")

	// 后台模拟浏览器消费 SSE；主线程 cancel + Close body 后读取随之解阻。
	drainDone := make(chan struct{})
	go func() {
		defer close(drainDone)
		_, _ = io.Copy(io.Discard, response.Body)
	}()

	// 等 run 真正进入 provider：此后 active_turn 一定已登记。
	select {
	case <-provider.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("run 未在超时内进入 provider")
	}

	// 断言 1：断开之前 active_turn 已在途，且标记为 detached。
	entry, ok := handler.getActiveTurnRegistry().get(session.ID)
	require.True(t, ok, "cancel 之前 active_turn 应已登记")
	assert.Equal(t, turnID, entry.TurnID)
	assert.Equal(t, session.ID, entry.SessionID)
	assert.Equal(t, agentChatActiveTurnSource, entry.Source)
	assert.True(t, entry.Detached, "resume_on_disconnect 回合应标记 detached")

	// 确认生成仍在途（而非在 cancel 前已收尾），否则本次取消验证没有意义。
	select {
	case <-provider.finished:
		t.Fatal("provider 在 cancel 之前已完成生成，无法验证断开发生在 run 在途期间")
	default:
	}

	// 模拟浏览器刷新：abort 在途 POST（请求 ctx 取消 + 关闭响应体）。
	cancelRequest()
	_ = response.Body.Close()

	// 断言 2：客户端断开未连带取消 run —— provider 侧 ctx.Err() 必须为 nil。
	select {
	case ctxErr := <-provider.ctxErrCh:
		assert.NoError(t, ctxErr, "resume_on_disconnect 下客户端断开不应取消在途 run")
	case <-time.After(5 * time.Second):
		t.Fatal("provider 未在超时内完成，run 可能被客户端断开卡死")
	}
	assert.GreaterOrEqual(t, provider.callCalls.Load(), int32(1), "run 应通过 provider.Call 执行生成")
	select {
	case <-provider.finished:
	case <-time.After(5 * time.Second):
		t.Fatal("provider 未在超时内收尾")
	}

	// 断言 3：run 收尾后 active_turn 被释放（release 幂等，handler defer 释放）。
	waitForResumeCondition(t, 5*time.Second, func() bool {
		_, stillRunning := handler.getActiveTurnRegistry().get(session.ID)
		return !stillRunning
	}, "run 结束后 active_turn 应被释放")

	select {
	case <-drainDone:
	case <-time.After(5 * time.Second):
		t.Fatal("响应读取协程未在超时内退出")
	}
}

// TestAgentChat_WithoutResumeOnDisconnect_CancelsOnClientAbort 是核心正例的对照：
// 同一取消时机下，未开启 resume_on_disconnect 的回合必须被连带取消
// （provider 侧 ctx.Err() == context.Canceled），证明正例断言的 ctx.Err()==nil
// 来自 detached 分支而非测试假象。这里直接调用 handler 并传入可取消的请求上下文，
// 取消不经过网络，判定完全确定。
func TestAgentChat_WithoutResumeOnDisconnect_CancelsOnClientAbort(t *testing.T) {
	const model = "test-model"

	mcpManager := &testMCPManager{}
	handler := NewHandler(skill.NewRegistry(mcpManager), nil, mcpManager)

	provider := newResumeDisconnectStreamProvider(model)
	llmRuntime := llm.NewLLMRuntime(&llm.RuntimeConfig{DefaultModel: model, MaxRetries: 0})
	require.NoError(t, llmRuntime.RegisterProvider(model, provider))
	handler.SetLLMRuntime(llmRuntime)

	sessionManager := chat.NewSessionManager(chat.NewInMemoryStorage(), nil)
	defer sessionManager.Stop()
	handler.SetSessionManager(sessionManager)

	session, err := sessionManager.Create(context.Background(), "user-cancel-on-disconnect-control")
	require.NoError(t, err)

	router := mux.NewRouter()
	handler.RegisterRoutes(router)

	body, err := json.Marshal(map[string]interface{}{
		"messages":     []map[string]string{{"role": "user", "content": "hi"}},
		"enable_react": true,
		"stream":       true,
		"session_id":   session.ID,
		"turn_id":      "turn_cancel_on_disconnect_control",
	})
	require.NoError(t, err)

	requestCtx, cancelRequest := context.WithCancel(context.Background())
	defer cancelRequest()
	request := httptest.NewRequest(http.MethodPost, "/api/agent/chat", bytes.NewReader(body)).WithContext(requestCtx)
	request.Header.Set("Accept", "text/event-stream")

	handlerDone := make(chan struct{})
	go func() {
		defer close(handlerDone)
		router.ServeHTTP(httptest.NewRecorder(), request)
	}()

	select {
	case <-provider.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("run 未在超时内进入 provider")
	}
	cancelRequest()

	select {
	case ctxErr := <-provider.ctxErrCh:
		require.ErrorIs(t, ctxErr, context.Canceled, "未开启 resume_on_disconnect 时客户端断开应取消在途 run")
	case <-time.After(5 * time.Second):
		t.Fatal("provider 未在超时内完成")
	}

	select {
	case <-handlerDone:
	case <-time.After(5 * time.Second):
		t.Fatal("handler 未在超时内收尾")
	}
}
