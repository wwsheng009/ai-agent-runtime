package cacheanalytics

import (
	"errors"
	"sort"
	"sync"
	"testing"
	"time"

	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
)

// fakeRequestStore 内存版 RequestStore：记录 SaveRequest 调用并支持回放，
// 用于验证终态落盘、查询期回放与幂等语义（不依赖 sqlite）。
type fakeRequestStore struct {
	mu        sync.Mutex
	saved     map[string]CacheRequestRecord
	saveErr   error
	loadErr   error
	loadCalls map[string]int
}

func newFakeRequestStore() *fakeRequestStore {
	return &fakeRequestStore{
		saved:     make(map[string]CacheRequestRecord),
		loadCalls: make(map[string]int),
	}
}

func (f *fakeRequestStore) SaveRequest(record CacheRequestRecord) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.saveErr != nil {
		return f.saveErr
	}
	f.saved[record.LLMRequestID] = record
	return nil
}

func (f *fakeRequestStore) LoadSessionRequests(sessionID string) ([]CacheRequestRecord, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.loadCalls[sessionID]++
	if f.loadErr != nil {
		return nil, f.loadErr
	}
	out := []CacheRequestRecord{}
	for _, record := range f.saved {
		if record.SessionID == sessionID {
			out = append(out, record)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].StartedAt.Before(out[j].StartedAt)
	})
	return out, nil
}

func (f *fakeRequestStore) saveCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.saved)
}

func (f *fakeRequestStore) loadCount(sessionID string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.loadCalls[sessionID]
}

// attachWithStore 以注入的 RequestStore 构建测试服务（Attach 全链路）。
func attachWithStore(t *testing.T, max int, store RequestStore) (*runtimeevents.Bus, *Service) {
	t.Helper()
	bus := newTestBus()
	clock := 0
	service := Attach(bus, Options{MaxRequestsPerSession: max, Store: store, Now: func() time.Time {
		clock++
		return time.Date(2026, 1, 1, 0, 0, clock, 0, time.UTC)
	}}, nil)
	if service == nil {
		t.Fatal("expected service")
	}
	t.Cleanup(service.Close)
	return bus, service
}

// TestCollectorPersistsTerminalRecords 终态记录（success/error/interrupted）
// 全部同步落库（Phase 3 镜像表，方案 §375）。
func TestCollectorPersistsTerminalRecords(t *testing.T) {
	store := newFakeRequestStore()
	bus, _ := attachWithStore(t, 100, store)

	publishStarted(bus, "s1", "req-ok", nil)
	publishFinished(bus, "s1", "req-ok", true, map[string]interface{}{
		"usage_prompt_tokens":       1000,
		"usage_completion_tokens":   50,
		"usage_total_tokens":        1050,
		"usage_cache_read_tokens":   800,
		"usage_cache_read_reported": true,
	})
	publishStarted(bus, "s1", "req-err", nil)
	publishFinished(bus, "s1", "req-err", false, nil)
	// in-flight 请求随会话终结被标记 interrupted 并落库（§16.1 边界 3）。
	publishStarted(bus, "s1", "req-orphan", nil)
	bus.Publish(runtimeevents.Event{
		Type:      eventSessionEnd,
		SessionID: "s1",
		Timestamp: nextTestTimestamp(),
	})

	if got := store.saveCount(); got != 3 {
		t.Fatalf("saved records = %d, want 3", got)
	}
	store.mu.Lock()
	okRecord := store.saved["req-ok"]
	errRecord := store.saved["req-err"]
	orphanRecord := store.saved["req-orphan"]
	store.mu.Unlock()

	if okRecord.Status != RequestStatusSuccess || okRecord.CacheStatus != CacheStatusHit {
		t.Fatalf("ok record status/cache = %q/%q", okRecord.Status, okRecord.CacheStatus)
	}
	if okRecord.Usage == nil || okRecord.Usage.CacheReadTokens != 800 {
		t.Fatalf("ok record usage = %+v", okRecord.Usage)
	}
	if errRecord.Status != RequestStatusError || errRecord.CacheStatus != CacheStatusError {
		t.Fatalf("error record status/cache = %q/%q", errRecord.Status, errRecord.CacheStatus)
	}
	if orphanRecord.Status != RequestStatusError || orphanRecord.ErrorCategory != errorCategoryInterrupted {
		t.Fatalf("orphan record = %+v", orphanRecord)
	}
	for _, record := range []CacheRequestRecord{okRecord, errRecord, orphanRecord} {
		if record.SchemaVersion != SchemaVersion || record.SessionID != "s1" {
			t.Fatalf("record identity = %+v", record)
		}
	}
}

// TestLiveSourceReplaysPersistedRecords 查询期惰性回放：重启/换进程后
// 同一 store 重建服务，总览与明细完整恢复，且每会话只回放一次。
func TestLiveSourceReplaysPersistedRecords(t *testing.T) {
	store := newFakeRequestStore()
	base := time.Date(2026, 3, 15, 8, 0, 0, 0, time.UTC)
	for i, id := range []string{"req-a", "req-b"} {
		if err := store.SaveRequest(CacheRequestRecord{
			SchemaVersion: SchemaVersion,
			LLMRequestID:  id,
			SessionID:     "s1",
			Status:        RequestStatusSuccess,
			CacheStatus:   CacheStatusHit,
			StartedAt:     base.Add(time.Duration(i) * time.Second),
			Usage: &CacheUsage{
				PromptTokens: 1000, CompletionTokens: 10, TotalTokens: 1010,
				CacheReadTokens: 800, CacheReadReported: true,
			},
		}); err != nil {
			t.Fatalf("seed store: %v", err)
		}
	}

	_, service := attachWithStore(t, 100, store)
	src := service.Source()

	overview, err := src.Overview("s1")
	if err != nil {
		t.Fatalf("overview: %v", err)
	}
	if overview.RequestsTotal != 2 {
		t.Fatalf("requests_total = %d, want 2", overview.RequestsTotal)
	}
	if overview.Tokens.CacheReadTokens != 1600 || overview.Tokens.PromptTokens != 2000 {
		t.Fatalf("tokens = %+v", overview.Tokens)
	}
	if overview.CacheStatusDistribution.Hit != 2 {
		t.Fatalf("distribution = %+v", overview.CacheStatusDistribution)
	}

	requests, err := src.Requests("s1", RequestQuery{})
	if err != nil {
		t.Fatalf("requests: %v", err)
	}
	if requests.Total != 2 || len(requests.Requests) != 2 {
		t.Fatalf("requests = total %d len %d", requests.Total, len(requests.Requests))
	}
	record, err := src.Request("s1", "req-b")
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	if record.LLMRequestID != "req-b" || record.Usage == nil {
		t.Fatalf("record = %+v", record)
	}

	// 重复查询不重复回放。
	for i := 0; i < 3; i++ {
		if _, err := src.Overview("s1"); err != nil {
			t.Fatalf("overview repeat: %v", err)
		}
	}
	if got := store.loadCount("s1"); got != 1 {
		t.Fatalf("load calls = %d, want 1", got)
	}
	// 未查询的会话不触发回放。
	if got := store.loadCount("s2"); got != 0 {
		t.Fatalf("load calls for s2 = %d, want 0", got)
	}
}

// TestReplayDeduplicatesLiveRecords 回放与在线事件幂等：镜像里已有的
// llm_request_id 再次终态时不重复计数（projector.append 去重）。
func TestReplayDeduplicatesLiveRecords(t *testing.T) {
	store := newFakeRequestStore()
	base := time.Date(2026, 3, 15, 8, 0, 0, 0, time.UTC)
	if err := store.SaveRequest(CacheRequestRecord{
		SchemaVersion: SchemaVersion,
		LLMRequestID:  "req-dup",
		SessionID:     "s1",
		Status:        RequestStatusSuccess,
		CacheStatus:   CacheStatusHit,
		StartedAt:     base,
		Usage:         &CacheUsage{PromptTokens: 500, TotalTokens: 500, CacheReadTokens: 400, CacheReadReported: true},
	}); err != nil {
		t.Fatalf("seed store: %v", err)
	}

	bus, service := attachWithStore(t, 100, store)
	// 同一请求的在线终态事件（重复投递场景）。
	publishStarted(bus, "s1", "req-dup", nil)
	publishFinished(bus, "s1", "req-dup", true, map[string]interface{}{
		"usage_prompt_tokens":       500,
		"usage_cache_read_tokens":   400,
		"usage_cache_read_reported": true,
	})

	overview, err := service.Source().Overview("s1")
	if err != nil {
		t.Fatalf("overview: %v", err)
	}
	if overview.RequestsTotal != 1 {
		t.Fatalf("requests_total = %d, want 1 (dedup)", overview.RequestsTotal)
	}
	if overview.Tokens.PromptTokens != 500 {
		t.Fatalf("prompt tokens = %d, want 500", overview.Tokens.PromptTokens)
	}
}

// TestPersistedProjectorDoesNotEvict 持久化模式下不淘汰：超过环形上限
// 也不丢记录、不标 partial（消除 §710 降级）；纯内存对照行为由
// TestProjectorRingBufferOverflowMarksPartial 覆盖。
func TestPersistedProjectorDoesNotEvict(t *testing.T) {
	store := newFakeRequestStore()
	bus, service := attachWithStore(t, 5, store)
	for i := 0; i < 8; i++ {
		id := "req-" + string(rune('a'+i))
		publishStarted(bus, "s1", id, nil)
		publishFinished(bus, "s1", id, true, nil)
	}
	overview, err := service.Source().Overview("s1")
	if err != nil {
		t.Fatalf("overview: %v", err)
	}
	if overview.RequestsTotal != 8 {
		t.Fatalf("requests_total = %d, want 8 (no eviction)", overview.RequestsTotal)
	}
	if overview.Coverage.Partial {
		t.Fatalf("coverage partial = true, want false; reasons=%v", overview.Coverage.PartialReasons)
	}
}

// TestPersistSaveFailureDoesNotBlockFlow 镜像写入失败（best-effort）不影响
// 在线投影与 SSE 增量。
func TestPersistSaveFailureDoesNotBlockFlow(t *testing.T) {
	store := newFakeRequestStore()
	store.saveErr = errors.New("disk full")
	bus, service := attachWithStore(t, 100, store)

	received := make(chan struct{}, 1)
	unsub := bus.SubscribeCancelable(EventCacheRequestFinished, func(runtimeevents.Event) {
		received <- struct{}{}
	})
	defer unsub()

	publishStarted(bus, "s1", "req-x", nil)
	publishFinished(bus, "s1", "req-x", true, map[string]interface{}{"usage_prompt_tokens": 10})

	select {
	case <-received:
	case <-time.After(time.Second):
		t.Fatal("cache_request_finished not published on save failure")
	}
	overview, err := service.Source().Overview("s1")
	if err != nil {
		t.Fatalf("overview: %v", err)
	}
	if overview.RequestsTotal != 1 {
		t.Fatalf("requests_total = %d, want 1", overview.RequestsTotal)
	}
}
