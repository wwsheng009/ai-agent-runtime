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
	"github.com/wwsheng009/ai-agent-runtime/internal/agentcontrol"
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
	id       int64
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
			case strings.HasPrefix(line, "id: "):
				var id int64
				require.NoError(t, json.Unmarshal([]byte(strings.TrimPrefix(line, "id: ")), &id))
				frame.id = id
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

// TestTrajectoryEmitterPersistsAndAlignsSeq 验证 Batch 1 落盘口径：
// wire-only 帧（chunk/reasoning/带工具身份的 observation）不落盘、不带 seq/id；
// 其余帧先写 EventStore，帧 _event.sequence 与持久化 seq 对齐，after 拉取正确。
func TestTrajectoryEmitterPersistsAndAlignsSeq(t *testing.T) {
	store := chat.NewInMemoryRuntimeStore(64)
	handler := &Handler{sessionEventStore: store}

	rec := httptest.NewRecorder()
	emitter := handler.newTrajectoryEmitter(rec, &chat.Session{ID: "sess-1"})
	emitter.Emit("chunk", map[string]interface{}{"type": "text", "content": "hi"})
	emitter.Emit("observation", map[string]interface{}{"tool": "read_file", "content": "tool copy"})
	emitter.Emit("observation", map[string]interface{}{"content": "structured observation"})
	emitter.Emit("done", map[string]interface{}{"status": "completed", "content": "hi"})

	frames := parseSSETestFrames(t, rec.Body.String())
	require.Len(t, frames, 4)
	// wire-only 帧：宁缺勿假，不带 seq/id（连接内计数器不是持久化游标）。
	assert.Zero(t, frames[0].sequence)
	assert.Zero(t, frames[0].id)
	assert.Zero(t, frames[1].sequence)
	assert.Zero(t, frames[1].id)
	// 持久化帧：seq/id 对齐且按 session 递增。
	assert.Equal(t, int64(1), frames[2].sequence)
	assert.Equal(t, int64(1), frames[2].id)
	assert.Equal(t, int64(2), frames[3].sequence)
	assert.Equal(t, int64(2), frames[3].id)

	events, err := store.ListEvents(context.Background(), "sess-1", 0, 0)
	require.NoError(t, err)
	require.Len(t, events, 2)
	assert.Equal(t, chatSSEStreamEventPrefix+"observation", events[0].Type)
	assert.Equal(t, chatSSEStreamEventPrefix+"done", events[1].Type)
	assert.Equal(t, "structured observation", events[0].Payload["content"])
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
// SSE 主链路不受影响，非 wire-only 帧降级为连接内计数且不写 id（无持久化游标）。
func TestTrajectoryEmitterDegradesOnAppendFailure(t *testing.T) {
	handler := &Handler{sessionEventStore: failingEventStore{}}

	rec := httptest.NewRecorder()
	emitter := handler.newTrajectoryEmitter(rec, &chat.Session{ID: "sess-1"})
	emitter.Emit("observation", map[string]interface{}{"content": "structured observation"})
	emitter.Emit("done", map[string]interface{}{"status": "completed"})

	frames := parseSSETestFrames(t, rec.Body.String())
	require.Len(t, frames, 2)
	// 降级：连接内计数 1、2；id 不写（连接内计数不是持久化 seq）。
	assert.Equal(t, int64(1), frames[0].sequence)
	assert.Zero(t, frames[0].id)
	assert.Equal(t, int64(2), frames[1].sequence)
	assert.Zero(t, frames[1].id)
}

// TestChatSSEFrameIsWireOnly 固化去重名单判据（与前端 recovery.ts 的
// isToolObservation 同口径）：chunk/reasoning 一律 wire-only；observation 仅在
// 携带非空字符串工具身份（tool/step）时才算同源重复；其余帧照常落盘。
func TestChatSSEFrameIsWireOnly(t *testing.T) {
	cases := []struct {
		name      string
		eventName string
		payload   map[string]interface{}
		want      bool
	}{
		{name: "chunk", eventName: "chunk", payload: map[string]interface{}{"content": "hi"}, want: true},
		{name: "reasoning", eventName: "reasoning", payload: map[string]interface{}{"content": "hi"}, want: true},
		{
			name:      "observation with tool identity",
			eventName: "observation",
			payload:   map[string]interface{}{"tool": "read_file", "content": "copy"},
			want:      true,
		},
		{
			name:      "observation with step identity",
			eventName: "observation",
			payload:   map[string]interface{}{"step": "1", "content": "copy"},
			want:      true,
		},
		{
			name:      "observation without identity",
			eventName: "observation",
			payload:   map[string]interface{}{"content": "structured"},
			want:      false,
		},
		{
			name:      "observation with blank identity",
			eventName: "observation",
			payload:   map[string]interface{}{"tool": "  ", "content": "structured"},
			want:      false,
		},
		{
			name:      "observation with non-string identity",
			eventName: "observation",
			payload:   map[string]interface{}{"step": 1, "content": "structured"},
			want:      false,
		},
		{name: "tool_end", eventName: "tool_end", payload: map[string]interface{}{"tool": "read_file"}, want: false},
		{name: "done", eventName: "done", payload: map[string]interface{}{"status": "completed"}, want: false},
		{name: "result", eventName: "result", payload: map[string]interface{}{"content": "final"}, want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, chatSSEFrameIsWireOnly(tc.eventName, tc.payload))
		})
	}
}

// TestTrajectoryEmitterWireOnlyFramesSkipStore 验证：
// wire-only 帧既不落盘也不写 id；done 帧落盘时裁掉内嵌 result（wire 帧保留），
// 避免与相邻 result 帧重复存储；result 帧照常落盘作为回放内容源。
func TestTrajectoryEmitterWireOnlyFramesSkipStore(t *testing.T) {
	store := chat.NewInMemoryRuntimeStore(64)
	handler := &Handler{sessionEventStore: store}

	rec := httptest.NewRecorder()
	emitter := handler.newTrajectoryEmitter(rec, &chat.Session{ID: "sess-wire"})
	emitter.Emit("reasoning", map[string]interface{}{"content": "thinking"})
	emitter.Emit("chunk", map[string]interface{}{"content": "hello"})
	emitter.Emit("observation", map[string]interface{}{"step": "1", "content": "tool copy"})
	bigResult := strings.Repeat("x", 4096)
	emitter.Emit("result", map[string]interface{}{"content": bigResult})
	emitter.Emit("done", map[string]interface{}{
		"status":  "completed",
		"content": "hello",
		"result":  map[string]interface{}{"content": bigResult},
	})

	frames := parseSSETestFrames(t, rec.Body.String())
	require.Len(t, frames, 5)
	for _, frame := range frames[:3] {
		assert.Zero(t, frame.sequence, "wire-only frame %q must not carry sequence", frame.event)
		assert.Zero(t, frame.id, "wire-only frame %q must not carry id", frame.event)
	}
	// wire 帧保留完整 done.result。
	wireResult, ok := frames[4].data["result"].(map[string]interface{})
	require.True(t, ok, "wire done frame must keep embedded result")
	assert.Equal(t, bigResult, wireResult["content"])
	assert.Equal(t, int64(1), frames[3].sequence)
	assert.Equal(t, int64(2), frames[4].sequence)

	events, err := store.ListEvents(context.Background(), "sess-wire", 0, 0)
	require.NoError(t, err)
	require.Len(t, events, 2)
	assert.Equal(t, chatSSEStreamEventPrefix+"result", events[0].Type)
	assert.Equal(t, chatSSEStreamEventPrefix+"done", events[1].Type)
	_, hasResult := events[1].Payload["result"]
	assert.False(t, hasResult, "persisted done payload must drop embedded result")
	assert.Equal(t, "completed", events[1].Payload["status"])
	assert.Equal(t, "hello", events[1].Payload["content"])
	assert.Equal(t, bigResult, events[0].Payload["content"])
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

	// Batch 1：wire-only 帧（chunk/reasoning/带工具身份的 observation）不落盘、
	// 不带 seq/id；持久化帧 seq 单调且与落盘事件一一对应。
	wireOnlyFrames := make([]sseTestFrame, 0, 4)
	persistedFrames := make([]sseTestFrame, 0, len(frames))
	for _, frame := range frames {
		if frame.sequence == 0 {
			assert.Truef(t, chatSSEFrameIsWireOnly(frame.event, frame.data),
				"frame without sequence must be wire-only: %q", frame.event)
			assert.Zero(t, frame.id, "wire-only frame %q must not carry id", frame.event)
			wireOnlyFrames = append(wireOnlyFrames, frame)
			continue
		}
		persistedFrames = append(persistedFrames, frame)
	}
	require.NotEmpty(t, wireOnlyFrames, "streaming turn should produce wire-only frames")
	require.NotEmpty(t, persistedFrames, "streaming turn should produce persisted frames")

	prev := int64(0)
	for _, frame := range persistedFrames {
		require.Greater(t, frame.sequence, prev, "persisted frame sequence must be monotonically increasing")
		prev = frame.sequence
		assert.Equal(t, frame.sequence, frame.id, "persisted frame must carry matching id")
	}

	// EventStore 中的 chat.sse.* 事件与持久化帧一一对应（数量、seq、类型）。
	events, err := store.ListEvents(context.Background(), sid, 0, 0)
	require.NoError(t, err)
	chatSSEEvents := make([]runtimeevents.Event, 0, len(events))
	for _, event := range events {
		if strings.HasPrefix(event.Type, chatSSEStreamEventPrefix) {
			chatSSEEvents = append(chatSSEEvents, event)
		}
	}
	require.Len(t, chatSSEEvents, len(persistedFrames), "every persisted frame must be stored as chat.sse.* event")
	for i, frame := range persistedFrames {
		assert.Equal(t, chatSSEStreamEventPrefix+frame.event, chatSSEEvents[i].Type, "frame %d type", i)
		assert.Equal(t, frame.sequence, eventSeq(t, chatSSEEvents[i]), "frame %d seq", i)
	}
	// 去重名单内的帧类型不得出现在落盘事件里。
	for _, event := range chatSSEEvents {
		name := strings.TrimPrefix(event.Type, chatSSEStreamEventPrefix)
		assert.Falsef(t, chatSSEFrameIsWireOnly(name, event.Payload),
			"wire-only event %q must not be persisted", event.Type)
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
		// P2-8 方案 4：配额驱逐产品事件落库，父会话 /runtime/events 与
		// /runtime/stream 才能把“子会话被谁回收”渲染成一行。
		agentcontrol.EventAgentReclaimed,
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
