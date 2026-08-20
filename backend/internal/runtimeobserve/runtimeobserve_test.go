package runtimeobserve

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"testing"
	"time"

	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
	"github.com/wwsheng009/ai-agent-runtime/internal/llm"
)

// ---------- Cursor ----------

func TestCursorRoundTrip(t *testing.T) {
	src := Cursor{SchemaVersion: SchemaVersionCursor, InstanceEpoch: "epoch-1", Seq: 42}
	enc := src.Encode()
	if enc == "" {
		t.Fatal("expected non-empty cursor")
	}
	decoded, err := DecodeCursor(enc, SchemaVersionCursor, "epoch-1")
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if decoded != src {
		t.Fatalf("round trip mismatch: got %+v want %+v", decoded, src)
	}
}

func TestCursorRejectsInvalid(t *testing.T) {
	if _, err := DecodeCursor("!!not-base64!!", SchemaVersionCursor, "epoch-1"); err == nil {
		t.Fatal("expected error for malformed base64")
	}
	bad := Cursor{SchemaVersion: SchemaVersionCursor, InstanceEpoch: "epoch-1", Seq: 99}.Encode()
	if _, err := DecodeCursor(bad, "runtime.observe.cursor.v9", "epoch-1"); err == nil {
		t.Fatal("expected schema mismatch error")
	}
	other := Cursor{SchemaVersion: SchemaVersionCursor, InstanceEpoch: "epoch-2", Seq: 99}.Encode()
	if _, err := DecodeCursor(other, SchemaVersionCursor, "epoch-1"); err == nil {
		t.Fatal("expected epoch mismatch error")
	}
	if _, err := DecodeCursor("", SchemaVersionCursor, "epoch-1"); err != nil {
		t.Fatalf("empty cursor should decode to zero value: %v", err)
	}
}

// ---------- Redaction ----------

func TestRedactSensitiveFields(t *testing.T) {
	r := NewRedactor([]byte("deploy-key"), "v1", "")
	input := map[string]interface{}{
		"provider":      "provider_a",
		"model":         "model-x",
		"authorization": "Bearer sk-abc",
		"api_key":       "sk-1234",
		"nested": map[string]interface{}{
			"prompt":       "hello secret world",
			"tool_arguments": map[string]interface{}{"query": "SELECT * FROM users"},
		},
		"count": 7,
	}
	out, omitted := r.RedactMap(input)
	if len(omitted) == 0 {
		t.Fatalf("expected omitted sensitive fields, got none; out=%v", out)
	}
	raw, _ := json.Marshal(out)
	serialized := string(raw)
	for _, forbidden := range []string{"sk-abc", "sk-1234", "hello secret world", "SELECT * FROM users"} {
		if strings.Contains(serialized, forbidden) {
			t.Fatalf("sensitive content leaked: %q in %s", forbidden, serialized)
		}
	}
	if out["provider"] != "provider_a" || out["count"] != 7 {
		t.Fatalf("non-sensitive fields lost: %v", out)
	}
	if _, ok := out["authorization"]; ok {
		t.Fatalf("authorization should be removed")
	}
}

func TestRedactDeepNesting(t *testing.T) {
	r := NewRedactor([]byte("k"), "v1", "")
	var deep interface{} = "leaf"
	for i := 0; i < 30; i++ {
		deep = map[string]interface{}{"a": deep}
	}
	out := r.Redact(deep)
	b, _ := json.Marshal(out)
	// 深度必须被 maxRedactDepth 约束，叶子收敛为 omitted。
	if !strings.Contains(string(b), "omitted") {
		t.Fatalf("leaf should be omitted marker: %s", string(b))
	}
	if n := strings.Count(string(b), `"a":{`); n > maxRedactDepth {
		t.Fatalf("deep structure not capped: depth=%d body=%s", n, string(b))
	}
}

func TestHMACFingerprint(t *testing.T) {
	r := NewRedactor([]byte("deploy-key"), "v1", "")
	a := r.HMACFingerprint(FingerprintDomainPrompt, "hello")
	b := r.HMACFingerprint(FingerprintDomainPrompt, "hello")
	if a != b {
		t.Fatal("fingerprint must be deterministic for same input")
	}
	c := r.HMACFingerprint(FingerprintDomainContent, "hello")
	if a == c {
		t.Fatal("domain separation failed: different domains must differ")
	}
	if !strings.HasPrefix(a, "hmac:v1:v1:") {
		t.Fatalf("unexpected scheme: %s", a)
	}
}

func TestBoundUTF8String(t *testing.T) {
	s := "你好，世界 abcdef"
	if got := boundUTF8String(s, 100); got != s {
		t.Fatalf("short string must not be truncated")
	}
	// 在 3 字节 utf8 序列中间截断时应被安全回退到完整 rune。
	cut := boundUTF8String(s, 7)
	if cut != "你好" {
		t.Fatalf("utf8-safe cut failed: %q", cut)
	}
}

func TestScrubURL(t *testing.T) {
	cases := map[string]string{
		"https://api.example.com/v1/chat/completions?api_key=abc&x=1": "https://api.example.com",
		"http://host:8080/path": "http://host:8080",
		"  ": "",
		"not a url": "",
	}
	for input, want := range cases {
		if got := ScrubURL(input); got != want {
			t.Fatalf("ScrubURL(%q) = %q want %q", input, got, want)
		}
	}
}

func TestHeaderPresence(t *testing.T) {
	out := HeaderPresence(map[string]string{
		"Authorization": "Bearer x",
		"Content-Type":  "application/json",
		"X-Custom":      "secret",
	})
	if out == nil || out["content-type"] != "present" {
		t.Fatalf("expected content-type presence: %v", out)
	}
	if _, ok := out["authorization"]; ok {
		t.Fatal("authorization presence must not be exported")
	}
}

// ---------- Projector ----------

func TestProjectRuntimeEventAllowlist(t *testing.T) {
	p := NewProjector(NewRedactor([]byte("k"), "v1", ""), false, 0)
	evt := runtimeevents.Event{
		Type:      EventLLMRequestStart,
		TraceID:   "trace_1",
		SessionID: "session_1",
		AgentName: "agent_1",
		Payload: map[string]interface{}{
			"provider":      "provider_a",
			"model":         "model-x",
			"prompt":        "super secret prompt with PII",
			"authorization": "Bearer sk-1",
			"message_count": 14,
			"mount_path":    "/tmp/secret-file",
		},
	}
	proj, ok := p.ProjectRuntimeEvent(evt)
	if !ok {
		t.Fatal("allowed event must project ok")
	}
	if proj.Type != EventLLMRequestStart || proj.Source != "agent_loop" {
		t.Fatalf("unexpected projection: %+v", proj)
	}
	if proj.Correlation.SessionID != "session_1" || proj.Correlation.TraceID != "trace_1" || proj.Correlation.AgentID != "agent_1" {
		t.Fatalf("correlation lost: %+v", proj.Correlation)
	}
	raw, _ := json.Marshal(proj.Payload)
	s := string(raw)
	for _, forbidden := range []string{"super secret prompt", "sk-1", "mount_path", "/tmp/secret-file"} {
		if strings.Contains(s, forbidden) {
			t.Fatalf("sensitive leaked in projection: %q in %s", forbidden, s)
		}
	}
	if proj.Payload["provider"] != "provider_a" || proj.Payload["message_count"] != 14 {
		t.Fatalf("allowed fields lost: %v", proj.Payload)
	}
	if proj.Payload["content_present"] != true {
		t.Fatalf("content_present missing: %v", proj.Payload)
	}
	if fp, ok := proj.Payload["prompt_fingerprint"]; !ok || fp == "" {
		t.Fatalf("prompt fingerprint missing: %v", proj.Payload)
	}
}

func TestProjectRuntimeEventUnknownDropped(t *testing.T) {
	p := NewProjector(NewRedactor([]byte("k"), "v1", ""), false, 0)
	_, ok := p.ProjectRuntimeEvent(runtimeevents.Event{
		Type:    "internal.secret.handler",
		Payload: map[string]interface{}{"anything": "leak"},
	})
	if ok {
		t.Fatal("unknown event type must be dropped")
	}
}

func TestProjectRetryAndDebug(t *testing.T) {
	p := NewProjector(NewRedactor([]byte("k"), "v1", ""), false, 0)
	retry, ok := p.ProjectRetryEvent(llm.RetryEvent{
		Provider:    "provider_a",
		Model:       "model-x",
		Attempt:     2,
		MaxAttempts: 3,
		Error:       "upstream 429 rate limit exceeded please slow down",
		RetryReason: "rate limited",
	})
	if !ok {
		t.Fatal("retry event must project ok")
	}
	if retry.Payload["retry_reason_category"] != "rate_limit" {
		t.Fatalf("unexpected category: %v", retry.Payload["retry_reason_category"])
	}
	if strings.Contains(strings.ToLower(renderPayload(retry.Payload)), "please slow down") {
		t.Fatal("raw error text leaked")
	}
}

// renderPayload 是测试辅助：把 payload 序列化为字符串检查泄漏。
func renderPayload(p map[string]interface{}) string {
	b, _ := json.Marshal(p)
	return string(b)
}

// ---------- Collector ----------

func testConfig() Config {
	cfg := DefaultConfig()
	cfg.Enabled = true
	cfg.RetentionEvents = 64
	cfg.RetentionTTL = time.Hour // 测试期间不要被 TTL 淘汰
	cfg.IngressQueueEvents = 16
	cfg.DefaultQueryLimit = 10
	cfg.MaxQueryLimit = 50
	return cfg
}

func publishLLMRequest(bus *runtimeevents.Bus, evtType, reqID string) {
	bus.Publish(runtimeevents.Event{
		Type:      evtType,
		SessionID: "session_1",
		TraceID:   "trace_1",
		AgentName: "agent_1",
		Payload: map[string]interface{}{
			"provider":      "provider_a",
			"model":         "model-x",
			"llm_request_id": reqID,
			"usage": map[string]interface{}{
				"prompt_tokens":     10,
				"completion_tokens": 5,
				"total_tokens":      15,
			},
		},
	})
}

func waitCollector(t *testing.T, c *Collector, want int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		_, latest := c.RingBounds()
		if latest >= int64(want) {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("collector did not ingest %d events; latest=%d", want, latestOrZero(c))
}

func latestOrZero(c *Collector) int64 {
	_, latest := c.RingBounds()
	return latest
}

func TestCollectorIngestQueryAndAggregates(t *testing.T) {
	bus := runtimeevents.NewBusWithRetention(2048)
	cfg := testConfig()
	c := NewCollector(cfg, bus, nil)
	if c == nil {
		t.Fatal("expected non-nil collector")
	}
	c.Start()
	defer c.Stop()

	publishLLMRequest(bus, EventLLMRequestStart, "req_1")
	publishLLMRequest(bus, EventLLMRequestDone, "req_1")
	publishLLMRequest(bus, EventLLMRetry, "req_1")
	publishLLMRequest(bus, EventLLMStreamSummary, "req_1")
	waitCollector(t, c, 4)

	res, err := c.Query(EventQuery{Limit: 50})
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if len(res.Events) != 4 {
		t.Fatalf("expected 4 events, got %d", len(res.Events))
	}
	if res.OldestAvailableSeq != 1 || res.LatestSeq != 4 {
		t.Fatalf("unexpected bounds: oldest=%d latest=%d", res.OldestAvailableSeq, res.LatestSeq)
	}
	// 序列单调递增。
	for i := 1; i < len(res.Events); i++ {
		prev, cur := res.Events[i-1], res.Events[i]
		if prev.ObservationSeq >= cur.ObservationSeq {
			t.Fatalf("sequence not monotonic: %d >= %d", prev.ObservationSeq, cur.ObservationSeq)
		}
	}

	// 聚合。
	rt, llmSum := c.Stats()
	if llmSum.RequestsTotal != 1 {
		t.Fatalf("requests_total=%d want 1", llmSum.RequestsTotal)
	}
	if llmSum.RetriesTotal != 1 {
		t.Fatalf("retries_total=%d want 1", llmSum.RetriesTotal)
	}
	if llmSum.StreamCount != 1 {
		t.Fatalf("stream_count=%d want 1", llmSum.StreamCount)
	}
	if rt.LastEventAt == nil {
		t.Fatal("last_event_at should be set")
	}
}

func TestCollectorQueryFilters(t *testing.T) {
	bus := runtimeevents.NewBusWithRetention(2048)
	cfg := testConfig()
	c := NewCollector(cfg, bus, nil)
	c.Start()
	defer c.Stop()

	// 两个 session。
	bus.Publish(runtimeevents.Event{Type: EventLLMRequestDone, SessionID: "sess_a", Payload: map[string]interface{}{"provider": "provider_a", "model": "m1"}})
	bus.Publish(runtimeevents.Event{Type: EventLLMRequestDone, SessionID: "sess_b", Payload: map[string]interface{}{"provider": "provider_b", "model": "m2"}})
	waitCollector(t, c, 2)

	res, err := c.Query(EventQuery{SessionID: "sess_a", Limit: 10})
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if len(res.Events) != 1 || res.Events[0].Correlation.SessionID != "sess_a" {
		t.Fatalf("session filter failed: %+v", res.Events)
	}

	res, err = c.Query(EventQuery{Provider: "provider_b", Limit: 10})
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if len(res.Events) != 1 || res.Events[0].Payload["provider"] != "provider_b" {
		t.Fatalf("provider filter failed: %+v", res.Events)
	}

	// after_seq 语义：exclusive 之后。
	res, err = c.Query(EventQuery{AfterSeq: 1, Limit: 10})
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if len(res.Events) != 1 || res.Events[0].ObservationSeq != 2 {
		t.Fatalf("after_seq filter failed: %+v", res.Events)
	}
}

func TestCollectorDedup(t *testing.T) {
	bus := runtimeevents.NewBusWithRetention(2048)
	cfg := testConfig()
	c := NewCollector(cfg, bus, nil)
	c.Start()
	defer c.Stop()

	// 同一事实双到达（例如 bus + durable replay）。
	publishLLMRequest(bus, EventLLMRequestDone, "req_dup")
	publishLLMRequest(bus, EventLLMRequestDone, "req_dup")
	waitCollector(t, c, 1)
	time.Sleep(50 * time.Millisecond) // 给第二个事件处理机会

	_, latest := c.RingBounds()
	if latest != 1 {
		t.Fatalf("dedup failed: expected seq 1, got %d", latest)
	}
}

func TestCollectorDisabledReturnsNil(t *testing.T) {
	bus := runtimeevents.NewBusWithRetention(2048)
	cfg := DefaultConfig()
	cfg.Enabled = false
	if c := NewCollector(cfg, bus, nil); c != nil {
		t.Fatal("disabled collector should be nil")
	}
}

func TestCollectorRingRetention(t *testing.T) {
	bus := runtimeevents.NewBusWithRetention(2048)
	cfg := testConfig()
	cfg.RetentionEvents = 4
	c := NewCollector(cfg, bus, nil)
	c.Start()
	defer c.Stop()

	for i := 0; i < 10; i++ {
		bus.Publish(runtimeevents.Event{Type: EventLLMRequestStart, SessionID: "session_r", Payload: map[string]interface{}{"llm_request_id": "r_unique_" + strconv.Itoa(i), "provider": "provider_a", "model": "m"}})
	}
	waitCollector(t, c, 10)

	res, _ := c.Query(EventQuery{Limit: 100})
	if len(res.Events) != 4 {
		t.Fatalf("ring retention: expected 4, got %d (oldest=%d)", len(res.Events), res.OldestAvailableSeq)
	}
	if res.OldestAvailableSeq != 7 || res.LatestSeq != 10 {
		t.Fatalf("bounds wrong: oldest=%d latest=%d", res.OldestAvailableSeq, res.LatestSeq)
	}
	// 事件内带 timestamp 且 schema 正确。
	for _, evt := range res.Events {
		if evt.SchemaVersion != SchemaVersionEvent {
			t.Fatalf("bad schema version: %s", evt.SchemaVersion)
		}
		if evt.Timestamp.IsZero() {
			t.Fatal("timestamp must be set")
		}
	}
}

// ---------- Service ----------

type fakeProcessSource struct{ process ProcessSummary }

func (f fakeProcessSource) ObservationProcessSummary() ProcessSummary {
	if f.process.ObservationEnabled {
		return f.process
	}
	return ProcessSummary{InstanceID: "runtime-test", PID: 1, UptimeMS: 100, ObservationEnabled: true}
}

type fakeSessionSource struct {
	items []SessionSummary
}

func (f *fakeSessionSource) ObservationSessionSummaries(ctx context.Context, limit int) ([]SessionSummary, error) {
	return f.items, nil
}

func (f *fakeSessionSource) ObservationSession(ctx context.Context, sessionID string) (SessionSummary, bool, error) {
	for _, it := range f.items {
		if it.SessionID == sessionID {
			return it, true, nil
		}
	}
	return SessionSummary{}, false, nil
}

func TestServiceDisabled(t *testing.T) {
	svc := NewService(DefaultConfig(), nil, nil, fakeProcessSource{}, nil)
	if svc.Enabled() {
		t.Fatal("service must be disabled by default")
	}
	if _, err := svc.BuildSnapshot(context.Background(), true); err == nil {
		t.Fatal("expected error when disabled")
	}
	capabilities := svc.Capabilities()
	if capabilities.Enabled {
		t.Fatal("capabilities must report disabled")
	}
}

func TestServiceSnapshot(t *testing.T) {
	cfg := testConfig()
	bus := runtimeevents.NewBusWithRetention(2048)
	c := NewCollector(cfg, bus, nil)
	c.Start()
	defer c.Stop()

	publishLLMRequest(bus, EventLLMRequestStart, "req_s")
	publishLLMRequest(bus, EventLLMRequestDone, "req_s")
	waitCollector(t, c, 2)

	sessions := &fakeSessionSource{items: []SessionSummary{
		{SessionID: "session_1", State: "running", RuntimeStateRevision: 4, LastEventSeq: 100},
	}}
	svc := NewService(cfg, c, nil, fakeProcessSource{}, sessions)

	snap, err := svc.BuildSnapshot(context.Background(), true)
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if snap.SchemaVersion != SchemaVersionResponse {
		t.Fatalf("bad schema: %s", snap.SchemaVersion)
	}
	if snap.Process.InstanceID != "runtime-test" {
		t.Fatalf("process instance missing: %+v", snap.Process)
	}
	if snap.Process.HeapBytes == 0 {
		t.Fatal("heap bytes should be sampled")
	}
	if snap.LLM.RequestsTotal != 1 {
		t.Fatalf("llm.requests_total=%d want 1", snap.LLM.RequestsTotal)
	}
	if snap.Runtime.EventIngressDropped != 0 {
		t.Fatalf("unexpected ingress drops: %d", snap.Runtime.EventIngressDropped)
	}
	if snap.Sessions.Count != 1 || snap.Sessions.Items[0].SessionID != "session_1" {
		t.Fatalf("sessions missing: %+v", snap.Sessions)
	}
	if snap.Cursor.ObservationSeq != 2 {
		t.Fatalf("cursor seq wrong: %d", snap.Cursor.ObservationSeq)
	}
	if snap.Consistency.Partial {
		t.Fatal("expected consistent snapshot")
	}
	if snap.SnapshotRevision != 2 {
		t.Fatalf("snapshot revision wrong: %d", snap.SnapshotRevision)
	}

	// 单 session 查询。
	single, err := svc.SessionFor(context.Background(), "session_1")
	if err != nil || single.State != "running" {
		t.Fatalf("session_for: %+v err=%v", single, err)
	}
}

func TestServiceQueryLimits(t *testing.T) {
	cfg := testConfig()
	bus := runtimeevents.NewBusWithRetention(2048)
	c := NewCollector(cfg, bus, nil)
	c.Start()
	defer c.Stop()
	for i := 0; i < 5; i++ {
		bus.Publish(runtimeevents.Event{Type: EventLLMRequestStart, SessionID: "session_l", Payload: map[string]interface{}{"llm_request_id": "l_unique_" + strconv.Itoa(i), "provider": "provider_a", "model": "m"}})
	}
	waitCollector(t, c, 5)
	svc := NewService(cfg, c, nil, fakeProcessSource{}, nil)
	// 超限请求应被钳制到 MaxQueryLimit。
	res, err := svc.QueryEvents(EventQuery{Limit: 9999})
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if len(res.Events) > cfg.MaxQueryLimit {
		t.Fatalf("limit not clamped: %d", len(res.Events))
	}
}

// 确保服务关闭后 bus 订阅被退订（可重复调用）。
func TestCollectorStopIdempotent(t *testing.T) {
	cfg := testConfig()
	bus := runtimeevents.NewBusWithRetention(2048)
	c := NewCollector(cfg, bus, nil)
	c.Start()
	c.Stop()
	c.Stop()
}
