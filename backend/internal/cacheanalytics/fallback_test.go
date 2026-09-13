package cacheanalytics

import (
	"testing"
)

// stubSource 可编程 Source：命中预置结果返回数据，未命中返回 missing
// （nil 表示"空结果但无错误"，用于区分"空"与"会话不存在"）。
type stubSource struct {
	capabilities Capabilities
	overview     map[string]CacheOverview
	requests     map[string]RequestListResponse
	record       map[string]CacheRequestRecord
	trace        map[string]MessageTrace
	missing      error
}

func newStubSource() *stubSource {
	return &stubSource{
		overview: make(map[string]CacheOverview),
		requests: make(map[string]RequestListResponse),
		record:   make(map[string]CacheRequestRecord),
		trace:    make(map[string]MessageTrace),
	}
}

func (s *stubSource) Capabilities() Capabilities { return s.capabilities }

func (s *stubSource) Overview(sessionID string) (CacheOverview, error) {
	if overview, ok := s.overview[sessionID]; ok {
		return overview, nil
	}
	return CacheOverview{}, s.missing
}

func (s *stubSource) Requests(sessionID string, _ RequestQuery) (RequestListResponse, error) {
	if list, ok := s.requests[sessionID]; ok {
		return list, nil
	}
	return RequestListResponse{}, s.missing
}

func (s *stubSource) Request(sessionID, llmRequestID string) (CacheRequestRecord, error) {
	if record, ok := s.record[sessionID+"/"+llmRequestID]; ok {
		return record, nil
	}
	if s.missing == nil {
		return CacheRequestRecord{}, ErrNotFound
	}
	return CacheRequestRecord{}, s.missing
}

func (s *stubSource) MessageTrace(sessionID, messageID string) (MessageTrace, error) {
	if trace, ok := s.trace[sessionID+"/"+messageID]; ok {
		return trace, nil
	}
	if s.missing == nil {
		return MessageTrace{}, ErrNotFound
	}
	return MessageTrace{}, s.missing
}

// TestSessionFallbackSourceFallsBackPerSession primary（统一用量库）没有该会话
// 的行时改用 secondary（runtime 镜像/实时投影）——"会话恢复后历史为空"的直接修复。
func TestSessionFallbackSourceFallsBackPerSession(t *testing.T) {
	primary := newStubSource() // 用量库：存在但该会话 0 行（空结果、无错误）
	secondary := newStubSource()
	secondary.capabilities = Capabilities{SchemaVersion: SchemaVersion, DataSource: DataSourceLive, Persisted: true}
	secondary.overview["s1"] = CacheOverview{SessionID: "s1", RequestsTotal: 3}
	secondary.requests["s1"] = RequestListResponse{
		Total: 3,
		Requests: []CacheRequestRecord{
			{LLMRequestID: "req-a"}, {LLMRequestID: "req-b"}, {LLMRequestID: "req-c"},
		},
	}
	secondary.record["s1/req-a"] = CacheRequestRecord{LLMRequestID: "req-a", SessionID: "s1"}
	secondary.trace["s1/msg-1"] = MessageTrace{
		SessionID:  "s1",
		MessageID:  "msg-1",
		ProducedBy: &ProducedBy{LLMRequestID: "req-a"},
	}

	src := NewSessionFallbackSource(primary, secondary)

	overview, err := src.Overview("s1")
	if err != nil {
		t.Fatalf("overview: %v", err)
	}
	if overview.RequestsTotal != 3 {
		t.Fatalf("requests_total = %d, want 3 (mirror fallback)", overview.RequestsTotal)
	}
	list, err := src.Requests("s1", RequestQuery{})
	if err != nil {
		t.Fatalf("requests: %v", err)
	}
	if list.Total != 3 || len(list.Requests) != 3 {
		t.Fatalf("requests = total %d len %d, want 3/3", list.Total, len(list.Requests))
	}
	record, err := src.Request("s1", "req-a")
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	if record.LLMRequestID != "req-a" {
		t.Fatalf("record = %+v", record)
	}
	trace, err := src.MessageTrace("s1", "msg-1")
	if err != nil {
		t.Fatalf("trace: %v", err)
	}
	if trace.ProducedBy == nil {
		t.Fatalf("trace = %+v, want mirror fallback content", trace)
	}
	// 能力发现：镜像已持久化时不得上报 Persisted=false（否则前端判定历史不可靠）。
	if capabilities := src.Capabilities(); !capabilities.Persisted {
		t.Fatalf("capabilities = %+v, want persisted union", capabilities)
	}
}

// TestSessionFallbackSourcePrefersPrimaryWithRecords primary 有数据时不做额外查询，
// 直接返回 primary（避免镜像覆盖/双查开销）。
func TestSessionFallbackSourcePrefersPrimaryWithRecords(t *testing.T) {
	primary := newStubSource()
	primary.overview["s1"] = CacheOverview{SessionID: "s1", RequestsTotal: 7}
	primary.requests["s1"] = RequestListResponse{Total: 7, Requests: []CacheRequestRecord{{LLMRequestID: "req-1"}}}
	primary.record["s1/req-1"] = CacheRequestRecord{LLMRequestID: "req-1"}
	primary.trace["s1/msg-1"] = MessageTrace{ProducedBy: &ProducedBy{LLMRequestID: "req-1"}}
	secondary := newStubSource()
	secondary.overview["s1"] = CacheOverview{SessionID: "s1", RequestsTotal: 3}

	src := NewSessionFallbackSource(primary, secondary)

	overview, err := src.Overview("s1")
	if err != nil {
		t.Fatalf("overview: %v", err)
	}
	if overview.RequestsTotal != 7 {
		t.Fatalf("requests_total = %d, want 7 (primary)", overview.RequestsTotal)
	}
	list, err := src.Requests("s1", RequestQuery{})
	if err != nil {
		t.Fatalf("requests: %v", err)
	}
	if list.Total != 7 {
		t.Fatalf("requests total = %d, want 7 (primary)", list.Total)
	}
	if _, err := src.Request("s1", "req-1"); err != nil {
		t.Fatalf("request: %v", err)
	}
	if trace, err := src.MessageTrace("s1", "msg-1"); err != nil || trace.ProducedBy == nil {
		t.Fatalf("trace = %+v err = %v", trace, err)
	}
}

// TestSessionFallbackSourceSecondaryNotFoundKeepsPrimaryResult 两个源都没有该
// 会话时保留 primary 的错误码（HTTP 层据此返回稳定 404/200 语义）。
func TestSessionFallbackSourceSecondaryNotFoundKeepsPrimaryResult(t *testing.T) {
	primary := newStubSource()
	primary.missing = ErrSessionNotFound
	secondary := newStubSource()
	secondary.missing = ErrSessionNotFound

	src := NewSessionFallbackSource(primary, secondary)
	if _, err := src.Overview("ghost"); err != ErrSessionNotFound {
		t.Fatalf("overview err = %v, want ErrSessionNotFound", err)
	}
	if _, err := src.Requests("ghost", RequestQuery{}); err != ErrSessionNotFound {
		t.Fatalf("requests err = %v, want ErrSessionNotFound", err)
	}
}

// TestNewSessionFallbackSourceNilHandling 单源/双空退化语义。
func TestNewSessionFallbackSourceNilHandling(t *testing.T) {
	only := newStubSource()
	only.overview["s1"] = CacheOverview{SessionID: "s1", RequestsTotal: 1}

	if got := NewSessionFallbackSource(nil, nil); got != nil {
		t.Fatalf("both nil = %v, want nil", got)
	}
	if got := NewSessionFallbackSource(only, nil); got != Source(only) {
		t.Fatalf("primary-only should degrade to the single source")
	}
	if got := NewSessionFallbackSource(nil, only); got != Source(only) {
		t.Fatalf("secondary-only should degrade to the single source")
	}
}
