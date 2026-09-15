package toolprotocol

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/toolkit"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolresult"
)

func TestResultFromPartsSuccess(t *testing.T) {
	result := ResultFromParts("view", "call-1", "hello world", "", map[string]interface{}{
		toolresult.MetadataKey: toolresult.KindText,
		toolresult.SourceKey:   toolresult.SourceToolkit,
	})
	if !result.OK {
		t.Fatalf("expected OK, got %+v", result)
	}
	if result.Outcome != toolresult.OutcomeSuccess {
		t.Fatalf("outcome=%q", result.Outcome)
	}
	if result.OutputKind != OutputKindText {
		t.Fatalf("output kind=%q", result.OutputKind)
	}
	if result.TextContent() != "hello world" {
		t.Fatalf("text=%q", result.TextContent())
	}
	if result.Source != toolresult.SourceToolkit {
		t.Fatalf("source=%q", result.Source)
	}
}

func TestResultFromPartsError(t *testing.T) {
	result := ResultFromParts("edit", "call-2", "", "path not found", map[string]interface{}{
		toolresult.MetadataErrorCodeKey: string(ErrorCodePathNotFound),
		toolresult.MetadataRetryableKey: false,
	})
	if result.OK {
		t.Fatal("expected not OK")
	}
	if result.Error == nil || result.Error.Code != ErrorCodePathNotFound {
		t.Fatalf("error=%+v", result.Error)
	}
	if result.Outcome != toolresult.OutcomeFailed {
		t.Fatalf("outcome=%q", result.Outcome)
	}
}

func TestResultEventMapIsCompact(t *testing.T) {
	result := ResultFromParts("view", "call-map", strings.Repeat("x", 500), "", map[string]interface{}{
		toolresult.MetadataKey:      toolresult.KindText,
		toolresult.SourceKey:        toolresult.SourceToolkit,
		toolresult.MetadataOutcomeKey: toolresult.OutcomeSuccess,
		"noisy_internal":            "should-not-appear",
	})
	eventMap := result.EventMap()
	if eventMap["ok"] != true {
		t.Fatalf("ok=%#v", eventMap["ok"])
	}
	if eventMap["tool_id"] != "view" || eventMap["call_id"] != "call-map" {
		t.Fatalf("ids=%#v", eventMap)
	}
	if _, hasContent := eventMap["content"]; hasContent {
		t.Fatalf("EventMap must omit content blocks: %#v", eventMap)
	}
	if summary, _ := eventMap["summary"].(string); summary == "" {
		t.Fatalf("expected summary, got %#v", eventMap["summary"])
	}
	meta, _ := eventMap["metadata"].(map[string]interface{})
	if meta == nil {
		t.Fatalf("expected thin metadata")
	}
	if _, ok := meta["noisy_internal"]; ok {
		t.Fatalf("noisy metadata leaked: %#v", meta)
	}
	full := result.Map()
	if _, hasContent := full["content"]; !hasContent {
		t.Fatalf("Map should include content blocks")
	}
}

func TestResultFromPartsPromotesStaleSnippetIntoThinMetadata(t *testing.T) {
	errBody := "old_string 未在文件中找到\n" +
		"最接近的当前内容（第 5 行附近）:\n" +
		"     5|\treturn true\n" +
		"next_action: rebuild"
	result := ResultFromParts("edit", "call-stale-wire", "", errBody, map[string]interface{}{
		toolresult.MetadataErrorCodeKey: string(ErrorCodeExecution),
		"noisy_internal":                "drop-me",
	})
	if result.OK {
		t.Fatal("expected failed result")
	}
	eventMap := result.EventMap()
	meta, _ := eventMap["metadata"].(map[string]interface{})
	if meta == nil {
		t.Fatalf("expected thin metadata, got %#v", eventMap)
	}
	if _, ok := meta["noisy_internal"]; ok {
		t.Fatalf("noisy key leaked: %#v", meta)
	}
	snip, _ := meta["current_snippet"].(string)
	if !strings.Contains(snip, "return true") {
		t.Fatalf("thin metadata missing current_snippet: %#v", meta)
	}
	if code, _ := meta[toolresult.MetadataErrorCodeKey].(string); code != string(ErrorCodeStaleContext) &&
		code != "STALE_CONTEXT" {
		// Error code may live on result.Error; metadata should still have recovery.
		if snip == "" {
			t.Fatalf("expected recovery fields even without code promote, meta=%#v err=%+v", meta, result.Error)
		}
	}
}

func TestFromToolkitResultRoundTrip(t *testing.T) {
	original := &toolkit.ToolResult{
		Success:    true,
		OutputKind: toolresult.KindText,
		Content:    "file contents",
		Metadata: map[string]interface{}{
			"file_path": "a.go",
		},
	}
	wire := FromToolkitResult("view", "call-9", original)
	if !wire.OK || wire.ToolID != "view" || wire.CallID != "call-9" {
		t.Fatalf("wire=%+v", wire)
	}
	back := ToToolkitResult(wire)
	if !back.Success || back.Content != "file contents" {
		t.Fatalf("back=%+v", back)
	}
}

func TestFromToolkitResultFailure(t *testing.T) {
	original := &toolkit.ToolResult{
		Success:    false,
		OutputKind: toolresult.KindText,
		Error:      errors.New("boom"),
		Metadata: map[string]interface{}{
			toolresult.MetadataErrorCodeKey:  string(ErrorCodeExecution),
			toolresult.MetadataNextActionKey: "retry with different args",
		},
	}
	wire := FromToolkitResult("shell", "call-x", original)
	if wire.OK || wire.Error == nil {
		t.Fatalf("expected failure wire, got %+v", wire)
	}
	if wire.Error.Message != "boom" {
		t.Fatalf("message=%q", wire.Error.Message)
	}
	if wire.Error.NextAction != "retry with different args" {
		t.Fatalf("next_action=%q", wire.Error.NextAction)
	}
}

func TestFromToolkitResultNil(t *testing.T) {
	wire := FromToolkitResult("x", "y", nil)
	if wire.OK || wire.Error == nil {
		t.Fatalf("expected nil failure, got %+v", wire)
	}
}

// TestResultEventMapWithMetadataKeysScopesPassthrough covers the per-result
// opt-in used for tool-scoped structured metadata (todos -> todo_snapshot): the
// shared thin allowlist stays untouched, while explicitly named keys pass
// through for that result only.
func TestResultEventMapWithMetadataKeysScopesPassthrough(t *testing.T) {
	result := ResultFromParts("todos", "call-scoped", "任务列表已更新", "", map[string]interface{}{
		toolresult.MetadataKey:        toolresult.KindText,
		toolresult.SourceKey:          toolresult.SourceToolkit,
		toolresult.MetadataOutcomeKey: toolresult.OutcomeSuccess,
		"todo_snapshot": map[string]interface{}{
			"items": []map[string]interface{}{
				{"content": "运行测试", "status": "pending", "active_form": "运行测试中"},
			},
		},
		"noisy_internal": "should-not-appear",
	})

	// Default EventMap must not widen the allowlist.
	plain := result.EventMap()
	plainMeta, _ := plain["metadata"].(map[string]interface{})
	if _, leaked := plainMeta["todo_snapshot"]; leaked {
		t.Fatalf("EventMap leaked a scoped key: %#v", plainMeta)
	}

	scoped := result.EventMapWithMetadataKeys("todo_snapshot", "  ", "missing_key")
	scopedMeta, _ := scoped["metadata"].(map[string]interface{})
	if scopedMeta == nil {
		t.Fatalf("expected thin metadata, got %#v", scoped)
	}
	snapshot, ok := scopedMeta["todo_snapshot"].(map[string]interface{})
	if !ok {
		t.Fatalf("scoped key missing: %#v", scopedMeta)
	}
	if _, ok := snapshot["items"]; !ok {
		t.Fatalf("scoped value mutated: %#v", snapshot)
	}
	if _, leaked := scopedMeta["noisy_internal"]; leaked {
		t.Fatalf("unlisted key leaked: %#v", scopedMeta)
	}
	if _, ok := scopedMeta["missing_key"]; ok {
		t.Fatalf("absent key must be skipped: %#v", scopedMeta)
	}
	if scopedMeta[toolresult.MetadataOutcomeKey] != toolresult.OutcomeSuccess {
		t.Fatalf("thin allowlist keys lost: %#v", scopedMeta)
	}

	// No scoped keys behaves exactly like EventMap.
	empty := result.EventMapWithMetadataKeys()
	if !reflect.DeepEqual(empty, plain) {
		t.Fatalf("EventMapWithMetadataKeys()=%#v want %#v", empty, plain)
	}

	// A result whose only metadata is the scoped key still emits a metadata map.
	onlyScoped := ResultFromParts("todos", "call-scoped-2", "", "", map[string]interface{}{
		"todo_snapshot": map[string]interface{}{"items": []interface{}{}},
	}).EventMapWithMetadataKeys("todo_snapshot")
	onlyScopedMeta, _ := onlyScoped["metadata"].(map[string]interface{})
	if _, ok := onlyScopedMeta["todo_snapshot"]; !ok {
		t.Fatalf("scoped-only metadata dropped: %#v", onlyScoped)
	}

	// Nil metadata / bare Result shapes must not panic or invent metadata.
	bare := Result{ToolID: "todos"}.EventMapWithMetadataKeys("todo_snapshot")
	if _, ok := bare["metadata"]; ok {
		t.Fatalf("expected no metadata map: %#v", bare)
	}
}
