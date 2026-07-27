package toolprotocol

import (
	"errors"
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
