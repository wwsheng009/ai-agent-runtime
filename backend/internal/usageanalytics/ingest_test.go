package usageanalytics

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
)

// testLookup 是可控的 SessionMetaLookup 测试桩。
type testLookup struct {
	meta map[string]SessionMeta
}

func (l testLookup) SessionMeta(sessionID string) (SessionMeta, bool) {
	meta, ok := l.meta[sessionID]
	return meta, ok
}

// newTestService 在临时目录打开分析库并订阅测试总线（now 固定，保证断言确定）。
func newTestService(t *testing.T, lookup SessionMetaLookup, now time.Time) (*Service, *runtimeevents.Bus) {
	t.Helper()
	bus := runtimeevents.NewBus()
	service, err := Attach(bus, Options{
		Config: Config{Path: filepath.Join(t.TempDir(), DefaultDBFileName)},
		Lookup: lookup,
		Now:    func() time.Time { return now },
	})
	require.NoError(t, err)
	require.NotNil(t, service)
	t.Cleanup(service.Close)
	return service, bus
}

func publishRequestStarted(bus *runtimeevents.Bus, sessionID, llmRequestID, traceID, turnID string, step int, at time.Time, provider, model string) {
	bus.Publish(runtimeevents.Event{
		Type:      EventLLMRequestStarted,
		TraceID:   traceID,
		SessionID: sessionID,
		Timestamp: at,
		Payload: map[string]interface{}{
			"llm_request_id": llmRequestID,
			"trace_id":       traceID,
			"turn_id":        turnID,
			"step":           step,
			"provider":       provider,
			"model":          model,
		},
	})
}

func publishRequestFinished(bus *runtimeevents.Bus, sessionID, llmRequestID string, extra map[string]interface{}) {
	payload := map[string]interface{}{"llm_request_id": llmRequestID, "success": true}
	for key, value := range extra {
		payload[key] = value
	}
	bus.Publish(runtimeevents.Event{
		Type:      EventLLMRequestFinished,
		SessionID: sessionID,
		Payload:   payload,
	})
}

func usagePayload(prompt, completion, total, cacheRead int) map[string]interface{} {
	payload := map[string]interface{}{
		"usage_prompt_tokens":     prompt,
		"usage_completion_tokens": completion,
		"usage_total_tokens":      total,
	}
	if cacheRead > 0 {
		payload["usage_cache_read_tokens"] = cacheRead
		payload["usage_cache_read_reported"] = true
	}
	return payload
}

func publishSessionEnd(bus *runtimeevents.Bus, sessionID string) {
	bus.Publish(runtimeevents.Event{Type: EventSessionEnd, SessionID: sessionID})
}

// TestIngestRequestTerminalIsIdempotent 验证同一 llm_request_id 的终态重复写入
// 仍然是单行（UPSERT），且后写覆盖计数类字段。
func TestIngestRequestTerminalIsIdempotent(t *testing.T) {
	now := time.Date(2026, 9, 13, 10, 0, 0, 0, time.UTC)
	service, bus := newTestService(t, nil, now)

	publishRequestStarted(bus, "sess-1", "req-1", "trace-1", "turn-1", 1, now.Add(-2*time.Second), "acme", "model-a")
	publishRequestFinished(bus, "sess-1", "req-1", usagePayload(100, 20, 120, 40))
	publishRequestFinished(bus, "sess-1", "req-1", usagePayload(50, 5, 55, 0))

	detail, err := service.SessionUsage("sess-1")
	require.NoError(t, err)
	require.Equal(t, 1, detail.Session.TotalRequests, "同一 llm_request_id 只能有一行")
	require.Equal(t, 1, detail.StepCount)
	require.Equal(t, 50, detail.Session.PromptTokens)
	require.Equal(t, 5, detail.Session.CompletionTokens)
	require.Equal(t, 55, detail.Session.TotalTokens)
	require.Equal(t, 1, detail.Session.LLMSuccesses)
	require.Equal(t, 0, detail.Session.LLMErrors)
}

// TestIngestAliasEventsAndSessionTerminal 验证下划线别名事件同样入库，
// 会话终态把悬挂请求兜底为 interrupted / completed。
func TestIngestAliasEventsAndSessionTerminal(t *testing.T) {
	now := time.Date(2026, 9, 13, 10, 0, 0, 0, time.UTC)
	service, bus := newTestService(t, nil, now)

	// 别名事件（internal/chat/events.go 常量）走同一条 ingest 路径。
	bus.Publish(runtimeevents.Event{
		Type:      EventLLMRequestStartedAlias,
		TraceID:   "trace-alias",
		SessionID: "sess-alias",
		Timestamp: now.Add(-time.Second),
		Payload: map[string]interface{}{
			"llm_request_id": "req-alias",
			"trace_id":       "trace-alias",
			"provider":       "acme",
			"model":          "model-a",
		},
	})
	bus.Publish(runtimeevents.Event{
		Type:      EventLLMRequestFinishedAlias,
		SessionID: "sess-alias",
		Payload:   map[string]interface{}{"llm_request_id": "req-alias", "success": true},
	})

	detail, err := service.SessionUsage("sess-alias")
	require.NoError(t, err)
	require.Equal(t, 1, detail.Session.TotalRequests)

	// 悬挂请求：只有 started，没有 finished；会话中断应兜底为 terminated 记录。
	publishRequestStarted(bus, "sess-int", "req-int", "trace-int", "", 1, now.Add(-time.Second), "acme", "model-a")
	bus.Publish(runtimeevents.Event{Type: EventSessionInterrupted, SessionID: "sess-int"})

	interrupted, err := service.SessionUsage("sess-int")
	require.NoError(t, err)
	require.Equal(t, SessionStatusInterrupted, interrupted.Session.Status)
	require.Equal(t, 1, interrupted.Session.TotalRequests, "悬挂请求应被兜底落库")
	require.Equal(t, 1, interrupted.Session.LLMErrors)

	// session_end → completed。
	publishRequestStarted(bus, "sess-done", "req-done", "trace-done", "", 1, now.Add(-time.Second), "acme", "model-a")
	bus.Publish(runtimeevents.Event{Type: EventSessionEnd, SessionID: "sess-done"})
	completed, err := service.SessionUsage("sess-done")
	require.NoError(t, err)
	require.Equal(t, SessionStatusCompleted, completed.Session.Status)
}

// TestIngestSessionMetaLookupFillsSessionFields 验证注入的元数据来源 best-effort
// 补齐 title/project/protocol/status，且事件携带的 provider/model 不被覆盖。
func TestIngestSessionMetaLookupFillsSessionFields(t *testing.T) {
	now := time.Date(2026, 9, 13, 10, 0, 0, 0, time.UTC)
	lookup := testLookup{meta: map[string]SessionMeta{
		"sess-meta": {
			Title:            "Realtime usage rollout",
			ProjectPath:      "/work/project",
			WorkingDirectory: "/work/project",
			Protocol:         "openai-responses",
			Status:           "active",
		},
	}}
	service, bus := newTestService(t, lookup, now)

	publishRequestStarted(bus, "sess-meta", "req-meta", "trace-meta", "turn-meta", 2, now.Add(-time.Second), "acme", "model-a")
	publishRequestFinished(bus, "sess-meta", "req-meta", usagePayload(10, 5, 15, 0))

	detail, err := service.SessionUsage("sess-meta")
	require.NoError(t, err)
	require.Equal(t, "Realtime usage rollout", detail.Session.Title)
	require.Equal(t, "/work/project", detail.Session.Project)
	require.Equal(t, "/work/project", detail.Session.Directory)
	require.Equal(t, "acme", detail.Session.Provider, "事件元数据优先于 lookup")
	require.Equal(t, "model-a", detail.Session.Model)
	require.Equal(t, "openai-responses", detail.Session.Protocol)
	require.Equal(t, "active", detail.Session.Status)

	// lookup 未命中时不报错，保留空值（best-effort）。
	publishRequestStarted(bus, "sess-unknown", "req-unknown", "trace-unknown", "", 1, now.Add(-time.Second), "acme", "model-a")
	unknown, err := service.SessionUsage("sess-unknown")
	require.NoError(t, err)
	require.Empty(t, unknown.Session.Title)
}

// TestIngestWithoutSessionIDIsIgnored 验证无会话归属的请求不落库（无法归组）。
func TestIngestWithoutSessionIDIsIgnored(t *testing.T) {
	now := time.Date(2026, 9, 13, 10, 0, 0, 0, time.UTC)
	service, bus := newTestService(t, nil, now)

	bus.Publish(runtimeevents.Event{
		Type:      EventLLMRequestFinished,
		Timestamp: now,
		Payload: map[string]interface{}{
			"llm_request_id":          "req-orphan",
			"success":                 true,
			"usage_prompt_tokens":     10,
			"usage_completion_tokens": 1,
			"usage_total_tokens":      11,
		},
	})

	list, err := service.ListSessions(Query{})
	require.NoError(t, err)
	require.Equal(t, 0, list.Total)
	require.Empty(t, list.Sessions)
}
