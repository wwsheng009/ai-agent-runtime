package skills

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/mux"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
	"github.com/wwsheng009/ai-agent-runtime/internal/llm"
	"github.com/wwsheng009/ai-agent-runtime/internal/skill"
)

// sseTestFrame 解析后的单条 SSE 帧。
type sseTestFrame struct {
	event    string
	data     map[string]interface{}
	sequence int64
}

// parseSSETestFrames 解析 SSE body 为帧列表（按 \n\n 分隔；提取 event 行与 data 行）。
func parseSSETestFrames(t *testing.T, body string) []sseTestFrame {
	t.Helper()
	var frames []sseTestFrame
	for _, block := range strings.Split(body, "\n\n") {
		block = strings.TrimSpace(block)
		if block == "" {
			continue
		}
		frame := sseTestFrame{}
		for _, line := range strings.Split(block, "\n") {
			line = strings.TrimSpace(line)
			switch {
			case strings.HasPrefix(line, "event: "):
				frame.event = strings.TrimPrefix(line, "event: ")
			case strings.HasPrefix(line, "data: "):
				var payload map[string]interface{}
				require.NoError(t, json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &payload))
				frame.data = payload
				if meta, ok := payload["_event"].(map[string]interface{}); ok {
					if seq, ok := meta["sequence"].(float64); ok {
						frame.sequence = int64(seq)
					}
				}
			}
		}
		frames = append(frames, frame)
	}
	return frames
}

// failingEventStore 模拟 EventStore 写入失败（错误注入）。
type failingEventStore struct{}

func (failingEventStore) AppendEvent(ctx context.Context, event runtimeevents.Event) (int64, error) {
	return 0, errors.New("simulated store failure")
}

func (failingEventStore) ListEvents(ctx context.Context, sessionID string, afterSeq int64, limit int) ([]runtimeevents.Event, error) {
	return nil, nil
}

// eventSeq 从 ListEvents 返回的事件中读取 seq（EventStore 将 seq 注入 Payload["seq"]）。
func eventSeq(t *testing.T, event runtimeevents.Event) int64 {
	t.Helper()
	switch value := event.Payload["seq"].(type) {
	case int64:
		return value
	case float64:
		return int64(value)
	}
	require.FailNow(t, "event payload should carry seq, got %v", event.Payload)
	return 0
}

// TestTrajectoryEmitterPersistsAndAlignsSeq 验证：每个 SSE 事件先写 EventStore，
// 帧 _event.sequence 与持久化 seq 对齐，Type 带 chat.sse. 前缀，after 拉取正确。
func TestTrajectoryEmitterPersistsAndAlignsSeq(t *testing.T) {
	store := chat.NewInMemoryRuntimeStore(64)
	handler := &Handler{sessionEventStore: store}

	rec := httptest.NewRecorder()
	emitter := handler.newTrajectoryEmitter(rec, &chat.Session{ID: "sess-1"})
	emitter.Emit("chunk", map[string]interface{}{"type": "text", "content": "hi"})
	emitter.Emit("done", map[string]interface{}{"status": "completed", "content": "hi"})

	frames := parseSSETestFrames(t, rec.Body.String())
	require.Len(t, frames, 2)
	assert.Equal(t, int64(1), frames[0].sequence)
	assert.Equal(t, int64(2), frames[1].sequence)

	events, err := store.ListEvents(context.Background(), "sess-1", 0, 0)
	require.NoError(t, err)
	require.Len(t, events, 2)
	assert.Equal(t, chatSSEStreamEventPrefix+"chunk", events[0].Type)
	assert.Equal(t, chatSSEStreamEventPrefix+"done", events[1].Type)
	assert.Equal(t, "hi", events[0].Payload["content"])
	assert.Equal(t, "completed", events[1].Payload["status"])

	// after=1 只返回第 2 条。
	rest, err := store.ListEvents(context.Background(), "sess-1", 1, 0)
	require.NoError(t, err)
	require.Len(t, rest, 1)
	assert.Equal(t, chatSSEStreamEventPrefix+"done", rest[0].Type)

	// after=最后一条 seq 返回空。
	tail, err := store.ListEvents(context.Background(), "sess-1", eventSeq(t, events[1]), 0)
	require.NoError(t, err)
	assert.Empty(t, tail)
}

// TestTrajectoryEmitterDegradesOnAppendFailure 验证错误隔离：EventStore 写入失败时
// SSE 主链路不受影响，帧 sequence 降级为连接内计数。
func TestTrajectoryEmitterDegradesOnAppendFailure(t *testing.T) {
	handler := &Handler{sessionEventStore: failingEventStore{}}

	rec := httptest.NewRecorder()
	emitter := handler.newTrajectoryEmitter(rec, &chat.Session{ID: "sess-1"})
	emitter.Emit("chunk", map[string]interface{}{"content": "hi"})
	emitter.Emit("done", map[string]interface{}{"status": "completed"})

	frames := parseSSETestFrames(t, rec.Body.String())
	require.Len(t, frames, 2)
	// 降级：连接内计数 1、2。
	assert.Equal(t, int64(1), frames[0].sequence)
	assert.Equal(t, int64(2), frames[1].sequence)
}

// TestAgentChatTrajectoryEventsPersistedEndToEnd 集成验证：/api/agent/chat SSE 流
// 每帧 seq 与 EventStore 一致、事件可增量拉取、与 runtime 生命周期事件可区分。
func TestAgentChatTrajectoryEventsPersistedEndToEnd(t *testing.T) {
	store := chat.NewInMemoryRuntimeStore(64)

	mcpManager := &testMCPManager{}
	registry := skill.NewRegistry(mcpManager)
	handler := NewHandler(registry, nil, mcpManager)
	handler.sessionEventStore = store

	provider := &testLLMProvider{
		name: "test-model",
		streamChunks: []llm.StreamChunk{
			{Type: llm.EventTypeReasoning, Content: "thinking step 1"},
			{Type: llm.EventTypeText, Content: "hello "},
			{Type: llm.EventTypeText, Content: "world", Done: true},
		},
	}
	runtime := llm.NewLLMRuntime(&llm.RuntimeConfig{DefaultModel: "test-model", MaxRetries: 0})
	require.NoError(t, runtime.RegisterProvider("test-model", provider))
	handler.SetLLMRuntime(runtime)

	sessionManager := chat.NewSessionManager(chat.NewInMemoryStorage(), &chat.SessionManagerConfig{
		TTL:             time.Hour,
		MaxHistory:      20,
		CleanupInterval: time.Hour,
		AutoArchive:     false,
		IdleTimeout:     time.Hour,
	})
	handler.SetSessionManager(sessionManager)

	router := mux.NewRouter()
	handler.RegisterRoutes(router)

	body := []byte(`{"messages":[{"role":"user","content":"hi"}],"user_id":"user-1"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/agent/chat", strings.NewReader(string(body)))
	req.Header.Set("Accept", "text/event-stream")
	rec := httptest.NewRecorder()

	router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)

	frames := parseSSETestFrames(t, rec.Body.String())
	require.NotEmpty(t, frames)

	// 从 meta 帧取 session_id。
	sid, ok := frames[0].data["session_id"].(string)
	require.True(t, ok, "meta frame should carry session_id")
	require.NotEmpty(t, sid)

	// 帧 sequence 单调（持久化 seq 按 session 递增）。
	prev := int64(0)
	for _, frame := range frames {
		require.Greater(t, frame.sequence, prev, "frame sequence must be monotonically increasing")
		prev = frame.sequence
	}

	// EventStore 中的 chat.sse.* 事件与 SSE 帧一一对应（数量、seq、类型）。
	events, err := store.ListEvents(context.Background(), sid, 0, 0)
	require.NoError(t, err)
	chatSSEEvents := make([]runtimeevents.Event, 0, len(events))
	for _, event := range events {
		if strings.HasPrefix(event.Type, chatSSEStreamEventPrefix) {
			chatSSEEvents = append(chatSSEEvents, event)
		}
	}
	require.Len(t, chatSSEEvents, len(frames), "every SSE frame must be persisted as chat.sse.* event")
	for i, frame := range frames {
		assert.Equal(t, chatSSEStreamEventPrefix+frame.event, chatSSEEvents[i].Type, "frame %d type", i)
	}

	// 双事件源可区分：写入一个 runtime 生命周期事件后，两者共存且按前缀过滤互不干扰。
	_, err = store.AppendEvent(context.Background(), runtimeevents.Event{
		Type:      "session.activated",
		SessionID: sid,
		Payload:   map[string]interface{}{"origin": "test"},
		Timestamp: time.Now().UTC(),
	})
	require.NoError(t, err)
	all, err := store.ListEvents(context.Background(), sid, 0, 0)
	require.NoError(t, err)
	// 全量 = chat.sse 帧 + 请求期生命周期事件（compact 检查，Q4 白名单已纳入）
	// + 测试写入的 session.activated。按前缀分类断言共存，不硬编码生命周期事件数。
	runtimeOnly := make([]runtimeevents.Event, 0, 2)
	for _, event := range all {
		if !strings.HasPrefix(event.Type, chatSSEStreamEventPrefix) {
			runtimeOnly = append(runtimeOnly, event)
			assert.False(t, strings.HasPrefix(event.Type, "chat.sse."),
				"runtime events must not use chat.sse. prefix")
		}
	}
	require.NotEmpty(t, runtimeOnly)
	assert.Equal(t, "session.activated", runtimeOnly[len(runtimeOnly)-1].Type)

	// after=最后一条 chat.sse seq 只返回生命周期事件。
	lastChatSeq := eventSeq(t, chatSSEEvents[len(chatSSEEvents)-1])
	rest, err := store.ListEvents(context.Background(), sid, lastChatSeq, 0)
	require.NoError(t, err)
	require.Len(t, rest, 1)
	assert.Equal(t, "session.activated", rest[0].Type)
}

// TestShouldPersistRuntimeSessionEvent (Q4) 验证 runtime 生命周期事件落库白名单：
// 审批/压缩/会话生命周期事件落库（轨迹视图可映射），高频内部事件不落库，
// 无 SessionID 的事件一律不落库。
func TestShouldPersistRuntimeSessionEvent(t *testing.T) {
	withSession := func(eventType string) runtimeevents.Event {
		return runtimeevents.Event{Type: eventType, SessionID: "session-q4"}
	}

	for _, eventType := range []string{
		"tool.requested", "tool.completed", "checkpoint_created",
		chat.EventApprovalRequested, chat.EventApprovalResolved,
		chat.EventSessionCompactStarted, chat.EventSessionCompactCompleted,
		chat.EventSessionCompactSkipped, chat.EventSessionCompactFailed,
		chat.EventSessionStart, chat.EventSessionEnd, chat.EventSessionInterrupted,
		chat.EventContextReconciled,
		// 方案B：打字机增量事件（assistant_delta / assistant.reasoning /
		// assistant.image_progress）必须落库，runtime/stream 长轮询才能拉到。
		chat.EventAssistantDelta, chat.EventAssistantReasoning,
		chat.EventAssistantReasoningDelta, chat.EventAssistantImageProgress,
	} {
		require.Truef(t, shouldPersistRuntimeSessionEvent(withSession(eventType)),
			"expected %q to be persisted", eventType)
	}

	for _, eventType := range []string{
		"job_output", "team.orchestrator.step", "mailbox_received", "llm.request.started",
	} {
		require.Falsef(t, shouldPersistRuntimeSessionEvent(withSession(eventType)),
			"expected %q NOT to be persisted", eventType)
	}

	// 无 SessionID 一律不落库（即使事件类型在白名单内）。
	require.False(t, shouldPersistRuntimeSessionEvent(runtimeevents.Event{
		Type: chat.EventApprovalRequested,
	}))
}
