package cacheanalytics

// fork_prefix_test.go — fork 前缀审计字段的终态记录与 JSON 往返（P2-1' G3）。
//
// 覆盖：
//   - TerminalRecordInput → CacheRequestRecord 的 fork_* 字段映射；
//   - record_json 存储路径的 JSON 往返（cache_requests 无 schema 迁移）；
//   - 未观测（nil 指针）时不产生 JSON 键，避免把"非 fork"与"false"混淆。

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestBuildTerminalRecordCarriesForkPrefixAudit(t *testing.T) {
	preserved := true
	finishedAt := time.Date(2026, 10, 16, 23, 30, 0, 0, time.UTC)
	record := BuildTerminalRecord(TerminalRecordInput{
		LLMRequestID:    "req-fork-1",
		SessionID:       "child-1",
		ParentSessionID: "parent-1",
		StartedAt:       finishedAt.Add(-time.Second),
		FinishedAt:      finishedAt,
		Payload:         map[string]interface{}{"success": true},
		// fork 首请求审计（started 载荷 → 采集侧透传）。
		ForkPrefixPreserved:    &preserved,
		ForkSnapshotMessages:   12,
		ForkSnapshotTokens:     3400,
		ForkPrefixKeptMessages: 12,
	})
	if record.ForkPrefixPreserved == nil || !*record.ForkPrefixPreserved {
		t.Fatalf("fork audit preserved flag must be carried, got %#v", record.ForkPrefixPreserved)
	}
	if record.ForkSnapshotMessages != 12 || record.ForkSnapshotTokens != 3400 || record.ForkPrefixKeptMessages != 12 {
		t.Fatalf("fork audit sizes must be carried, got %+v", record)
	}

	// record_json 存储路径：JSON 往返保真（cache_requests 镜像表直接序列化）。
	encoded, err := json.Marshal(record)
	if err != nil {
		t.Fatalf("marshal record: %v", err)
	}
	var decoded CacheRequestRecord
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("unmarshal record: %v", err)
	}
	if decoded.ForkPrefixPreserved == nil || !*decoded.ForkPrefixPreserved {
		t.Fatalf("roundtrip lost fork preserved flag: %#v", decoded.ForkPrefixPreserved)
	}
	if decoded.ForkSnapshotTokens != 3400 || decoded.ForkPrefixKeptMessages != 12 {
		t.Fatalf("roundtrip lost fork sizes: %+v", decoded)
	}
}

func TestBuildTerminalRecordOmitsUnobservedForkAudit(t *testing.T) {
	finishedAt := time.Date(2026, 10, 16, 23, 30, 0, 0, time.UTC)
	record := BuildTerminalRecord(TerminalRecordInput{
		LLMRequestID: "req-plain",
		SessionID:    "sess-plain",
		StartedAt:    finishedAt.Add(-time.Second),
		FinishedAt:   finishedAt,
		Payload:      map[string]interface{}{"success": true},
	})
	if record.ForkPrefixPreserved != nil {
		t.Fatal("non-fork requests must keep the fork audit unobserved (nil pointer)")
	}
	encoded, err := json.Marshal(record)
	if err != nil {
		t.Fatalf("marshal record: %v", err)
	}
	if strings.Contains(string(encoded), "fork_prefix_preserved") {
		t.Fatalf("unobserved fork audit must not emit JSON keys, got %s", encoded)
	}

	// 显式 false（被装配改写）必须保留为观测值，而不是折叠成未观测。
	rewritten := false
	partial := BuildTerminalRecord(TerminalRecordInput{
		LLMRequestID:           "req-fork-2",
		SessionID:              "child-2",
		ParentSessionID:        "parent-1",
		StartedAt:              finishedAt.Add(-time.Second),
		FinishedAt:             finishedAt,
		Payload:                map[string]interface{}{"success": true},
		ForkPrefixPreserved:    &rewritten,
		ForkSnapshotMessages:   8,
		ForkPrefixKeptMessages: 3,
	})
	if partial.ForkPrefixPreserved == nil || *partial.ForkPrefixPreserved {
		t.Fatalf("explicit false must be preserved, got %#v", partial.ForkPrefixPreserved)
	}
	if partial.ForkPrefixKeptMessages != 3 {
		t.Fatalf("kept messages must be carried, got %d", partial.ForkPrefixKeptMessages)
	}
}
