package agent

import (
	"fmt"
	"strings"
	"testing"

	runtimeerrors "github.com/wwsheng009/ai-agent-runtime/internal/errors"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolresult"
)

func TestClassifyGenericToolExecutionError_SpawnDepthLimit(t *testing.T) {
	err := fmt.Errorf("[SPAWN_DEPTH_LIMIT] agent spawn depth limit reached before child creation")
	classified := classifyGenericToolExecutionError(err)
	if classified == nil {
		t.Fatal("expected classified runtime error")
	}
	if classified.Code != runtimeerrors.ErrAgentSpawnDepthLimit {
		t.Fatalf("code=%s want SPAWN_DEPTH_LIMIT", classified.Code)
	}

	// Typed RuntimeError path must preserve the structured code.
	typed := runtimeerrors.Newf(runtimeerrors.ErrAgentSpawnDepthLimit, "depth limit")
	result := &toolExecutionResult{}
	metadata := map[string]interface{}{}
	recordToolExecutionOutcome(result, metadata, nil, nil, typed)
	if code, _ := metadata["error_code"].(string); code != string(runtimeerrors.ErrAgentSpawnDepthLimit) {
		t.Fatalf("metadata error_code=%v want SPAWN_DEPTH_LIMIT meta=%#v", metadata["error_code"], metadata)
	}
	if !strings.Contains(result.Error, "SPAWN_DEPTH_LIMIT") {
		t.Fatalf("result error should include SPAWN_DEPTH_LIMIT, got %q", result.Error)
	}
}

func TestRecordToolExecutionOutcome_PreservesToolAuthoredStaleContext(t *testing.T) {
	// edit/apply_patch stamp STALE_CONTEXT + next_action in tool metadata while
	// returning a plain error string. Generic classification must not overwrite
	// that to TOOL_EXECUTION, and disposition fields must promote to top-level.
	result := &toolExecutionResult{}
	metadata := map[string]interface{}{"step": 3, "trace_id": "trace-stale"}
	rawMeta := map[string]interface{}{
		"error_code":                 string(runtimeerrors.ErrToolStaleContext),
		"retryable":                  false,
		"failure_class":              "stale_context",
		"file_path":                  `E:\projects\demo\file.go`,
		"suggested_view_offset":      12,
		"suggested_view_limit":       40,
		"current_snippet":            "func Hello() {}\n",
		"current_snippet_start_line": 13,
		"next_action":                "STALE_CONTEXT: copy current_snippet then rebuild; do not retry the same stale old_string unchanged.",
		toolresult.MetadataKey:       toolresult.KindText,
	}
	err := fmt.Errorf("old_string 未在文件中找到；edit 只执行精确匹配。next_action: 先用 view/grep 获取文件中的最新片段后重试")

	recordToolExecutionOutcome(result, metadata, "partial", rawMeta, err)

	if code, _ := metadata["error_code"].(string); code != string(runtimeerrors.ErrToolStaleContext) {
		t.Fatalf("top-level error_code=%v want STALE_CONTEXT meta=%#v", metadata["error_code"], metadata)
	}
	nested, _ := metadata["tool_metadata"].(map[string]interface{})
	if nested == nil {
		t.Fatalf("expected tool_metadata nested map, got %#v", metadata)
	}
	if code, _ := nested["error_code"].(string); code != string(runtimeerrors.ErrToolStaleContext) {
		t.Fatalf("nested error_code=%v want STALE_CONTEXT", nested["error_code"])
	}
	next, _ := metadata["next_action"].(string)
	if !strings.Contains(next, "STALE_CONTEXT") {
		t.Fatalf("expected promoted next_action with STALE_CONTEXT, got %q", next)
	}
	if retryable, _ := metadata["retryable"].(bool); retryable {
		t.Fatalf("STALE_CONTEXT must promote retryable=false, got %#v", metadata["retryable"])
	}
	if offset, _ := metadata["suggested_view_offset"].(int); offset != 12 {
		t.Fatalf("expected promoted suggested_view_offset=12, got %#v", metadata["suggested_view_offset"])
	}
	if snippet, _ := metadata["current_snippet"].(string); !strings.Contains(snippet, "func Hello") {
		t.Fatalf("expected promoted current_snippet, got %#v", metadata["current_snippet"])
	}
	if start, _ := metadata["current_snippet_start_line"].(int); start != 13 {
		t.Fatalf("expected promoted current_snippet_start_line=13, got %#v", metadata["current_snippet_start_line"])
	}
	if !strings.Contains(result.Error, "old_string") {
		t.Fatalf("result error should keep tool message, got %q", result.Error)
	}
}

func TestClassifyGenericToolExecutionError_StaleContextFromMessage(t *testing.T) {
	err := fmt.Errorf("old_string 未在文件中找到；edit 只执行精确匹配")
	classified := classifyGenericToolExecutionError(err)
	if classified == nil {
		t.Fatal("expected classified runtime error")
	}
	if classified.Code != runtimeerrors.ErrToolStaleContext {
		t.Fatalf("code=%s want STALE_CONTEXT", classified.Code)
	}
}

func TestRecordToolExecutionOutcome_DoesNotOverwriteExistingTopLevelCode(t *testing.T) {
	result := &toolExecutionResult{}
	metadata := map[string]interface{}{
		"error_code": string(runtimeerrors.ErrToolTimeout),
	}
	// Plain error would classify as TOOL_EXECUTION, but existing top-level code wins.
	recordToolExecutionOutcome(result, metadata, nil, map[string]interface{}{
		"shell_type": "pwsh",
	}, fmt.Errorf("something failed"))
	if code, _ := metadata["error_code"].(string); code != string(runtimeerrors.ErrToolTimeout) {
		t.Fatalf("existing top-level error_code must win, got %v meta=%#v", code, metadata)
	}
}
