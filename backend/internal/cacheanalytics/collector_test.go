package cacheanalytics

import (
	"sync/atomic"
	"testing"
	"time"

	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
)

func newTestBus() *runtimeevents.Bus { return runtimeevents.NewBus() }

// testClockNsec 包级递增计数器：bus.Publish 会自动填充真实 time.Now()
//（bus.go:312-314），Windows 时钟精度下同刻事件会破坏排序断言，
// 故测试事件显式传递增 Timestamp。
var testClockNsec int64

func nextTestTimestamp() time.Time {
	return time.Date(2026, 1, 1, 0, 0, 0, int(atomic.AddInt64(&testClockNsec, 1000)), time.UTC)
}

func publishStarted(bus *runtimeevents.Bus, sessionID, llmRequestID string, extra map[string]interface{}) {
	payload := map[string]interface{}{
		"trace_id":           "trace-" + llmRequestID,
		"logical_turn_id":    "turn-1",
		"llm_request_id":     llmRequestID,
		"step":               1,
		"provider":           "openai",
		"model":              "gpt-4o",
		"prompt_cache_epoch": 3,
		"prompt_cache_key":   "ck-1",
		"prompt_fingerprint": "fp-1",
	}
	for key, value := range extra {
		payload[key] = value
	}
	bus.Publish(runtimeevents.Event{
		Type:      eventLLMRequestStarted,
		SessionID: sessionID,
		TraceID:   "trace-" + llmRequestID,
		Payload:   payload,
		Timestamp: nextTestTimestamp(),
	})
}

func publishFinished(bus *runtimeevents.Bus, sessionID, llmRequestID string, success bool, extra map[string]interface{}) {
	payload := map[string]interface{}{
		"trace_id":        "trace-" + llmRequestID,
		"logical_turn_id": "turn-1",
		"llm_request_id":  llmRequestID,
		"step":            1,
		"provider":        "openai",
		"model":           "gpt-4o",
		"success":         success,
	}
	if !success {
		payload["error"] = "boom"
		payload["error_code"] = "rate_limited"
	}
	for key, value := range extra {
		payload[key] = value
	}
	bus.Publish(runtimeevents.Event{
		Type:      eventLLMRequestFinished,
		SessionID: sessionID,
		TraceID:   "trace-" + llmRequestID,
		Payload:   payload,
		Timestamp: nextTestTimestamp(),
	})
}

func attachTest(t *testing.T, max int) (*runtimeevents.Bus, *Service) {
	t.Helper()
	bus := newTestBus()
	clock := 0
	service := Attach(bus, Options{MaxRequestsPerSession: max, Now: func() time.Time {
		clock++
		return time.Date(2026, 1, 1, 0, 0, clock, 0, time.UTC)
	}}, nil)
	if service == nil {
		t.Fatal("expected service")
	}
	t.Cleanup(service.Close)
	return bus, service
}

// TestCollectorPublishesCacheRequestFinished 验证记录终态落盘后发布
// cache_request_finished（§6.3 SSE 增量），载荷为 CacheRequestRecord 投影。
func TestCollectorPublishesCacheRequestFinished(t *testing.T) {
	bus, _ := attachTest(t, 100)

	received := make(chan runtimeevents.Event, 8)
	unsub := bus.SubscribeCancelable(EventCacheRequestFinished, func(ev runtimeevents.Event) {
		received <- ev
	})
	defer unsub()

	publishStarted(bus, "s1", "req-sse", nil)
	publishFinished(bus, "s1", "req-sse", true, map[string]interface{}{
		"usage_prompt_tokens":       1000,
		"usage_completion_tokens":   50,
		"usage_cache_read_tokens":   800,
		"usage_cache_read_reported": true,
	})

	select {
	case ev := <-received:
		if ev.Type != EventCacheRequestFinished {
			t.Fatalf("event type = %q", ev.Type)
		}
		if ev.SessionID != "s1" {
			t.Fatalf("session_id = %q, want s1", ev.SessionID)
		}
		if ev.TraceID != "trace-req-sse" {
			t.Fatalf("trace_id = %q", ev.TraceID)
		}
		if got, _ := ev.Payload["llm_request_id"].(string); got != "req-sse" {
			t.Fatalf("payload llm_request_id = %v", ev.Payload["llm_request_id"])
		}
		if got, _ := ev.Payload["cache_status"].(string); got != CacheStatusHit {
			t.Fatalf("payload cache_status = %v, want hit", ev.Payload["cache_status"])
		}
		usage, ok := ev.Payload["usage"].(map[string]interface{})
		if !ok {
			t.Fatalf("payload usage missing: %v", ev.Payload["usage"])
		}
		if got, _ := usage["cache_read_tokens"].(float64); got != 800 {
			t.Fatalf("usage.cache_read_tokens = %v, want 800", usage["cache_read_tokens"])
		}
	case <-time.After(time.Second):
		t.Fatal("cache_request_finished event not published")
	}

	// error 终态同样发布。
	publishStarted(bus, "s1", "req-sse-err", nil)
	publishFinished(bus, "s1", "req-sse-err", false, nil)
	select {
	case ev := <-received:
		if got, _ := ev.Payload["cache_status"].(string); got != CacheStatusError {
			t.Fatalf("error path cache_status = %v, want error", ev.Payload["cache_status"])
		}
	case <-time.After(time.Second):
		t.Fatal("error path event not published")
	}
}

func TestCollectorCacheHitClassification(t *testing.T) {
	bus, service := attachTest(t, 100)
	src := service.Source()

	publishStarted(bus, "s1", "req-hit", nil)
	publishFinished(bus, "s1", "req-hit", true, map[string]interface{}{
		"usage_prompt_tokens":       1000,
		"usage_completion_tokens":   50,
		"usage_total_tokens":        1050,
		"usage_cached_tokens":       800,
		"usage_cache_read_tokens":   800,
		"usage_cache_read_reported": true,
		"usage_cache_hit_ratio":     0.8,
		"usage_cache_status":        "hit",
	})
	record, err := src.Request("s1", "req-hit")
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	if record.CacheStatus != CacheStatusHit {
		t.Fatalf("expected hit, got %s", record.CacheStatus)
	}
	if record.Usage == nil || record.Usage.CacheReadTokens != 800 {
		t.Fatalf("usage cache read mismatch: %+v", record.Usage)
	}
	if record.CacheHitRatio == nil || *record.CacheHitRatio != 0.8 {
		t.Fatalf("hit ratio mismatch: %v", record.CacheHitRatio)
	}
	if record.Status != RequestStatusSuccess {
		t.Fatalf("expected success, got %s", record.Status)
	}
}

func TestCollectorWriteAndReportedZeroAndNotReported(t *testing.T) {
	bus, service := attachTest(t, 100)
	src := service.Source()

	// write：creation>0 且 read=0
	publishStarted(bus, "s1", "req-write", nil)
	publishFinished(bus, "s1", "req-write", true, map[string]interface{}{
		"usage_prompt_tokens":         500,
		"usage_completion_tokens":     10,
		"usage_total_tokens":          510,
		"usage_cache_creation_tokens": 500,
		"usage_cache_read_reported":   false,
	})
	// reported_zero：显式上报但为 0
	publishStarted(bus, "s1", "req-zero", nil)
	publishFinished(bus, "s1", "req-zero", true, map[string]interface{}{
		"usage_prompt_tokens":       300,
		"usage_completion_tokens":   5,
		"usage_total_tokens":        305,
		"usage_cache_read_reported": true,
	})
	// not_reported：无 usage 字段
	publishStarted(bus, "s1", "req-none", nil)
	publishFinished(bus, "s1", "req-none", true, nil)

	cases := map[string]string{
		"req-write": CacheStatusWrite,
		"req-zero":  CacheStatusReportedZero,
		"req-none":  CacheStatusNotReported,
	}
	for id, want := range cases {
		record, err := src.Request("s1", id)
		if err != nil {
			t.Fatalf("%s: %v", id, err)
		}
		if record.CacheStatus != want {
			t.Fatalf("%s: expected %s, got %s", id, want, record.CacheStatus)
		}
	}
	overview, err := src.Overview("s1")
	if err != nil {
		t.Fatalf("overview: %v", err)
	}
	if overview.RequestsTotal != 3 || overview.RequestsWithUsage != 2 || overview.RequestsCacheReported != 1 {
		t.Fatalf("aggregate mismatch: total=%d usage=%d reported=%d", overview.RequestsTotal, overview.RequestsWithUsage, overview.RequestsCacheReported)
	}
	if overview.CacheStatusDistribution.Write != 1 || overview.CacheStatusDistribution.ReportedZero != 1 || overview.CacheStatusDistribution.NotReported != 1 {
		t.Fatalf("distribution mismatch: %+v", overview.CacheStatusDistribution)
	}
	if overview.Tokens.CacheCreationTokens != 500 || overview.Tokens.CacheReadTokens != 0 {
		t.Fatalf("token sums mismatch: %+v", overview.Tokens)
	}
	if overview.CacheHitRatio == nil || *overview.CacheHitRatio != 0 {
		t.Fatalf("hit ratio should be 0 (reported_zero only): %v", overview.CacheHitRatio)
	}
	if overview.CacheWriteRatio == nil || *overview.CacheWriteRatio != 1.0 {
		t.Fatalf("write ratio mismatch: %v", overview.CacheWriteRatio)
	}
}

func TestCollectorErrorRequest(t *testing.T) {
	bus, service := attachTest(t, 100)
	src := service.Source()
	publishStarted(bus, "s1", "req-err", nil)
	publishFinished(bus, "s1", "req-err", false, nil)
	record, err := src.Request("s1", "req-err")
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	if record.Status != RequestStatusError || record.CacheStatus != CacheStatusError {
		t.Fatalf("status mismatch: %s/%s", record.Status, record.CacheStatus)
	}
	if record.ErrorCategory != "rate_limited" {
		t.Fatalf("error category mismatch: %s", record.ErrorCategory)
	}
}

func TestCollectorSessionTerminalFinalizesInflight(t *testing.T) {
	bus, service := attachTest(t, 100)
	src := service.Source()
	publishStarted(bus, "s1", "req-hang", nil)
	bus.Publish(runtimeevents.Event{Type: eventSessionEnd, SessionID: "s1"})
	record, err := src.Request("s1", "req-hang")
	if err != nil {
		t.Fatalf("inflight request lost on session_end: %v", err)
	}
	if record.Status != RequestStatusError || record.ErrorCategory != errorCategoryInterrupted {
		t.Fatalf("expected interrupted error, got %s/%s", record.Status, record.ErrorCategory)
	}
}

func TestProjectorRingBufferOverflowMarksPartial(t *testing.T) {
	bus, service := attachTest(t, 2)
	src := service.Source()
	for i, id := range []string{"r1", "r2", "r3"} {
		publishStarted(bus, "s1", id, map[string]interface{}{"step": i + 1})
		publishFinished(bus, "s1", id, true, nil)
	}
	overview, err := src.Overview("s1")
	if err != nil {
		t.Fatalf("overview: %v", err)
	}
	if overview.RequestsTotal != 2 {
		t.Fatalf("expected 2 retained, got %d", overview.RequestsTotal)
	}
	if !overview.Coverage.Partial {
		t.Fatal("expected partial coverage after eviction")
	}
	found := false
	for _, reason := range overview.Coverage.PartialReasons {
		if reason == "ring_overflow" {
			found = true
		}
	}
	if !found {
		t.Fatalf("missing ring_overflow reason: %v", overview.Coverage.PartialReasons)
	}
	if _, err := src.Request("s1", "r1"); err == nil {
		t.Fatal("expected evicted record r1 to be gone")
	}
	if _, err := src.Request("s1", "r3"); err != nil {
		t.Fatalf("r3 should be retained: %v", err)
	}
}

func TestRequestsFilterAndPagination(t *testing.T) {
	bus, service := attachTest(t, 100)
	src := service.Source()
	for i, id := range []string{"a", "b", "c"} {
		publishStarted(bus, "s1", id, map[string]interface{}{"step": i + 1, "logical_turn_id": "turn-1"})
		publishFinished(bus, "s1", id, true, nil)
	}
	publishStarted(bus, "s1", "d", map[string]interface{}{"logical_turn_id": "turn-2"})
	publishFinished(bus, "s1", "d", false, nil)

	response, err := src.Requests("s1", RequestQuery{Status: RequestStatusSuccess, Limit: 2})
	if err != nil {
		t.Fatalf("requests: %v", err)
	}
	if response.Total != 3 || len(response.Requests) != 2 {
		t.Fatalf("filter mismatch: total=%d len=%d", response.Total, len(response.Requests))
	}
	// 倒序：最新的 c 在前。
	if response.Requests[0].LLMRequestID != "c" {
		t.Fatalf("expected newest first, got %s", response.Requests[0].LLMRequestID)
	}
	byTurn, err := src.Requests("s1", RequestQuery{TurnID: "turn-2"})
	if err != nil {
		t.Fatalf("requests by turn: %v", err)
	}
	if byTurn.Total != 1 || byTurn.Requests[0].LLMRequestID != "d" {
		t.Fatalf("turn filter mismatch: %+v", byTurn)
	}
}

func TestUnderscoreAliasEventsAccepted(t *testing.T) {
	bus, service := attachTest(t, 100)
	src := service.Source()
	bus.Publish(runtimeevents.Event{
		Type:      eventLLMRequestStartedAlias,
		SessionID: "s1",
		Payload: map[string]interface{}{
			"llm_request_id": "req-alias",
		},
	})
	bus.Publish(runtimeevents.Event{
		Type:      eventLLMRequestFinishedAlias,
		SessionID: "s1",
		Payload: map[string]interface{}{
			"llm_request_id":            "req-alias",
			"success":                   true,
			"usage_prompt_tokens":       10,
			"usage_cache_read_reported": false,
		},
	})
	record, err := src.Request("s1", "req-alias")
	if err != nil {
		t.Fatalf("alias events should project: %v", err)
	}
	if record.CacheStatus != CacheStatusNotReported {
		t.Fatalf("expected not_reported, got %s", record.CacheStatus)
	}
}
