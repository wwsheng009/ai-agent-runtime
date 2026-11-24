package cacheanalytics

import (
	"encoding/json"
	"testing"
	"time"
)

// TestBuildTerminalRecordCapturesReasoningEffort 锁定缓存请求记录对
// requested/effective reasoning effort 的映射：缓存页据此显示实际发送值，
// 并能在"配置了但未发送"时区分 requested 值。
func TestBuildTerminalRecordCapturesReasoningEffort(t *testing.T) {
	finishedAt := time.Date(2026, 11, 22, 10, 0, 0, 0, time.UTC)
	record := BuildTerminalRecord(TerminalRecordInput{
		LLMRequestID:             "req-effort",
		SessionID:                "sess-effort",
		StartedAt:                finishedAt.Add(-time.Second),
		FinishedAt:               finishedAt,
		RequestedReasoningEffort: "max",
		ReasoningEffort:          "high",
		Payload:                  map[string]interface{}{"success": true},
	})
	if record.RequestedReasoningEffort != "max" || record.ReasoningEffort != "high" {
		t.Fatalf("reasoning effort = %q/%q, want max/high", record.RequestedReasoningEffort, record.ReasoningEffort)
	}

	// record_json 往返：持久化镜像无需 schema 迁移即可保留新字段。
	encoded, err := json.Marshal(record)
	if err != nil {
		t.Fatalf("marshal record: %v", err)
	}
	var decoded CacheRequestRecord
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("unmarshal record: %v", err)
	}
	if decoded.RequestedReasoningEffort != "max" || decoded.ReasoningEffort != "high" {
		t.Fatalf("roundtrip lost reasoning effort: %+v", decoded)
	}
}

// TestBuildTerminalRecordFallsBackToStartedPayloadReasoningEffort 保证 started
// 事件丢失时，finished 载荷上的同名字段仍可补齐（与其余终态字段同口径）。
func TestBuildTerminalRecordFallsBackToStartedPayloadReasoningEffort(t *testing.T) {
	finishedAt := time.Date(2026, 11, 22, 10, 5, 0, 0, time.UTC)
	record := BuildTerminalRecord(TerminalRecordInput{
		LLMRequestID: "req-effort-payload",
		SessionID:    "sess-effort",
		StartedAt:    finishedAt.Add(-time.Second),
		FinishedAt:   finishedAt,
		Payload: map[string]interface{}{
			"success":                    true,
			"requested_reasoning_effort": "max",
			"reasoning_effort":           "medium",
		},
	})
	if record.RequestedReasoningEffort != "max" || record.ReasoningEffort != "medium" {
		t.Fatalf("payload fallback reasoning effort = %q/%q", record.RequestedReasoningEffort, record.ReasoningEffort)
	}
}

// TestBuildTerminalRecordOmitsUnobservedReasoningEffort 历史记录没有该字段时
// 保持空串（未观测），读取侧显示 "-" 而不是伪造值。
func TestBuildTerminalRecordOmitsUnobservedReasoningEffort(t *testing.T) {
	finishedAt := time.Date(2026, 11, 22, 10, 10, 0, 0, time.UTC)
	record := BuildTerminalRecord(TerminalRecordInput{
		LLMRequestID: "req-effort-legacy",
		SessionID:    "sess-effort",
		StartedAt:    finishedAt.Add(-time.Second),
		FinishedAt:   finishedAt,
		Payload:      map[string]interface{}{"success": true},
	})
	if record.RequestedReasoningEffort != "" || record.ReasoningEffort != "" {
		t.Fatalf("unobserved reasoning effort must stay empty: %+v", record)
	}
}
