package agent

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	runtimeerrors "github.com/wwsheng009/ai-agent-runtime/internal/errors"
	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
	"github.com/wwsheng009/ai-agent-runtime/internal/output"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolresult"
	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

func TestToolRequestedEventPayloadIncludesArgPreview(t *testing.T) {
	payload := toolRequestedEventPayload(types.ToolCall{
		ID:   "call-1",
		Name: "bash",
		Args: map[string]interface{}{
			"command": "Get-ChildItem -Force",
		},
	}, 3, "trace-1", nil)

	if got := payload["arg_preview"]; got != "command=Get-ChildItem -Force" {
		t.Fatalf("expected command preview, got %#v", got)
	}
	if got := payload["command_text"]; got != "Get-ChildItem -Force" {
		t.Fatalf("expected command text, got %#v", got)
	}
}

func TestToolRequestedEventPayloadIncludesMultipleGenericArgs(t *testing.T) {
	payload := toolRequestedEventPayload(types.ToolCall{
		ID:   "call-view",
		Name: "view",
		Args: map[string]interface{}{
			"file_path":            "main.go",
			"offset":               40,
			"limit":                20,
			"include_line_numbers": false,
			"workdir":              "E:/repo",
		},
	}, 1, "trace-view", nil)

	want := "file_path=main.go include_line_numbers=false limit=20 offset=40"
	if got := payload["arg_preview"]; got != want {
		t.Fatalf("expected complete generic preview %q, got %#v", want, got)
	}
	if got := payload["workdir"]; got != "E:/repo" {
		t.Fatalf("expected workdir context, got %#v", got)
	}
	if strings.Contains(payload["arg_preview"].(string), "workdir") {
		t.Fatalf("workdir must not be duplicated in arg preview: %#v", payload["arg_preview"])
	}
}

func TestToolRequestedEventPayloadPreservesLongFilename(t *testing.T) {
	base := t.TempDir()
	filename := strings.Repeat("very-long-file-name-", 4) + "component.generated.tsx"
	absPath := filepath.Join(base, "apps", "portal-modern", "src", filename)
	wantPath := filepath.Join("apps", "portal-modern", "src", filename)

	payload := toolRequestedEventPayload(types.ToolCall{
		ID:   "call-long-view",
		Name: "view",
		Args: map[string]interface{}{
			"file_path": absPath,
			"workdir":   base,
			"limit":     20,
		},
	}, 1, "trace-long-view", nil)

	if got := payload["arg_preview"]; got != "limit=20" {
		t.Fatalf("long file path must leave the compact preview, got %#v", got)
	}
	if got := payload["display_file_path"]; got != wantPath {
		t.Fatalf("expected complete relative file path %q, got %#v", wantPath, got)
	}
	if !strings.HasSuffix(payload["display_file_path"].(string), filename) {
		t.Fatalf("filename was not preserved: %#v", payload["display_file_path"])
	}
}

func TestToolCompletedEventPayloadRedactsSensitiveArgs(t *testing.T) {
	payload := toolCompletedEventPayload(toolExecutionResult{
		Call: types.ToolCall{
			ID:   "call-fetch",
			Name: "fetch",
			Args: map[string]interface{}{
				"url":          "https://example.test",
				"api_key":      "should-not-leak",
				"timeout_ms":   3000,
				"token_budget": 4096,
			},
		},
	}, 1, "trace-fetch", nil)

	want := "url=https://example.test api_key=<redacted> timeout_ms=3000 token_budget=4096"
	if got := payload["arg_preview"]; got != want {
		t.Fatalf("expected redacted generic preview %q, got %#v", want, got)
	}
	if strings.Contains(payload["arg_preview"].(string), "should-not-leak") {
		t.Fatalf("sensitive value leaked in arg preview: %#v", payload["arg_preview"])
	}
}

func TestToolRequestedEventPayloadIncludesCompleteGlobPreview(t *testing.T) {
	payload := toolRequestedEventPayload(types.ToolCall{
		ID:   "call-glob",
		Name: "glob",
		Args: map[string]interface{}{
			"pattern":          "**/*.tsx",
			"path":             "apps/portal-modern/src",
			"case_insensitive": false,
			"limit":            50,
		},
	}, 1, "trace-glob", nil)

	if got := payload["arg_preview"]; got != "pattern=**/*.tsx path=apps/portal-modern/src case_insensitive=false limit=50" {
		t.Fatalf("expected complete glob preview, got %#v", got)
	}
	if got := payload["logical_tool"]; got != "glob" {
		t.Fatalf("expected logical_tool=glob, got %#v", got)
	}
}

func TestToolRequestedEventPayloadIncludesCompleteGrepPreview(t *testing.T) {
	payload := toolRequestedEventPayload(types.ToolCall{
		ID:   "call-grep",
		Name: "grep",
		Args: map[string]interface{}{
			"patterns": []interface{}{"Popover", "DialogTrigger"},
			"paths":    []interface{}{"apps/portal-modern/src"},
			"glob":     "*.tsx",
			"context":  2,
		},
	}, 1, "trace-grep", nil)

	want := `patterns=["Popover","DialogTrigger"] paths=["apps/portal-modern/src"] glob=*.tsx context=2`
	if got := payload["arg_preview"]; got != want {
		t.Fatalf("expected complete grep preview %q, got %#v", want, got)
	}
}

func TestToolCompletedEventPayloadPromotesSearchBackend(t *testing.T) {
	payload := toolCompletedEventPayload(toolExecutionResult{
		Call: types.ToolCall{
			ID:   "call-grep-backend",
			Name: "grep",
			Args: map[string]interface{}{"pattern": "Popover", "path": "src"},
		},
		Envelope: &output.Envelope{
			Metadata: map[string]interface{}{
				"tool_metadata": map[string]interface{}{
					"engine":          "rg",
					"backend_command": "rg",
					"backend_path":    `C:\tools\rg.exe`,
				},
			},
		},
	}, 1, "trace-grep-backend", nil)

	if got := payload["execution_backend"]; got != "rg" {
		t.Fatalf("expected execution_backend=rg, got %#v", got)
	}
	if got := payload["backend_command"]; got != "rg" {
		t.Fatalf("expected backend command, got %#v", got)
	}
	if got := payload["backend_path"]; got != `C:\tools\rg.exe` {
		t.Fatalf("expected backend path, got %#v", got)
	}
}

func TestToolRequestedEventPayloadIncludesWorkdir(t *testing.T) {
	payload := toolRequestedEventPayload(types.ToolCall{
		ID:   "call-workdir",
		Name: "execute_shell_command",
		Args: map[string]interface{}{
			"command": "git status",
			"workdir": "E:/projects/ai/ai-agent-runtime",
		},
	}, 1, "trace-workdir", nil)

	if got := payload["workdir"]; got != "E:/projects/ai/ai-agent-runtime" {
		t.Fatalf("expected workdir, got %#v", got)
	}
}

func TestToolRequestedEventPayloadIncludesBackgroundCwd(t *testing.T) {
	payload := toolRequestedEventPayload(types.ToolCall{
		ID:   "call-cwd",
		Name: "background_task",
		Args: map[string]interface{}{
			"command": "git status",
			"cwd":     "E:/projects/ai/ai-agent-runtime",
		},
	}, 1, "trace-cwd", nil)

	if got := payload["cwd"]; got != "E:/projects/ai/ai-agent-runtime" {
		t.Fatalf("expected cwd, got %#v", got)
	}
}

func TestToolCompletedEventPayloadPrefersStructuredSummary(t *testing.T) {
	payload := toolCompletedEventPayload(toolExecutionResult{
		Call: types.ToolCall{
			ID:   "call-2",
			Name: "ls",
			Args: map[string]interface{}{"path": "."},
		},
		Output: "目录: .\n\n📁 a/\n📁 b/\n📄 main.go\n\n统计: 1 个文件, 2 个目录",
		Envelope: &output.Envelope{
			Summary: "directory listing summary that should not win",
			Metadata: map[string]interface{}{
				"tool_metadata": map[string]interface{}{
					"file_count": 2,
					"dir_count":  1,
				},
			},
		},
	}, 2, "trace-2", nil)

	summaryLines, ok := payload["summary_lines"].([]string)
	if !ok {
		t.Fatalf("expected summary lines, got %#v", payload["summary_lines"])
	}
	expected := []string{
		"目录: .",
		"📁 a/ · 📁 b/ · 📄 main.go",
		"统计: 1 个文件, 2 个目录",
	}
	if len(summaryLines) != len(expected) {
		t.Fatalf("expected %d summary lines, got %#v", len(expected), summaryLines)
	}
	for i, line := range expected {
		if summaryLines[i] != line {
			t.Fatalf("expected summary line %d to be %q, got %q", i, line, summaryLines[i])
		}
	}
	if got := payload["arg_preview"]; got != "path=." {
		t.Fatalf("expected path preview, got %#v", got)
	}
}

func TestToolCompletedEventPayloadIncludesProtocolResult(t *testing.T) {
	payload := toolCompletedEventPayload(toolExecutionResult{
		Call: types.ToolCall{
			ID:   "call-protocol",
			Name: "view",
			Args: map[string]interface{}{"file_path": "a.go"},
		},
		Output: "file body line one\nfile body line two",
		Envelope: &output.Envelope{
			Metadata: map[string]interface{}{
				toolresult.MetadataOKKey:      true,
				toolresult.MetadataOutcomeKey: toolresult.OutcomeSuccess,
				toolresult.MetadataKey:        toolresult.KindText,
				toolresult.SourceKey:          toolresult.SourceToolkit,
			},
		},
	}, 1, "trace-protocol", nil)

	raw, ok := payload["protocol_result"].(map[string]interface{})
	if !ok || raw == nil {
		t.Fatalf("expected protocol_result map, got %#v", payload["protocol_result"])
	}
	if raw["ok"] != true {
		t.Fatalf("protocol_result.ok=%#v", raw["ok"])
	}
	if raw["tool_id"] != "view" || raw["call_id"] != "call-protocol" {
		t.Fatalf("protocol_result ids=%#v", raw)
	}
	if raw["outcome"] != toolresult.OutcomeSuccess {
		t.Fatalf("protocol_result.outcome=%#v", raw["outcome"])
	}
	if _, hasContent := raw["content"]; hasContent {
		t.Fatalf("protocol_result must stay compact without content: %#v", raw)
	}
	// Flat disposition fields remain for offline analyzers.
	if payload[toolresult.MetadataOKKey] != true || payload[toolresult.MetadataOutcomeKey] != toolresult.OutcomeSuccess {
		t.Fatalf("flat disposition missing: %#v", payload)
	}
}

func TestToolCompletedEventPayloadPreservesTodosListLines(t *testing.T) {
	output := strings.Join([]string{
		"任务列表已更新: 2 待处理, 1 进行中, 0 已完成",
		"任务列表更新状态: 新增 3, 状态变更 0, 保持 0, 移除 0",
		"当前任务列表:",
		"1. [待处理] 分析需求 (新增)",
		"2. [进行中] 修改实现 (新增)",
		"3. [待处理] 运行测试 (新增)",
	}, "\n")

	payload := toolCompletedEventPayload(toolExecutionResult{
		Call: types.ToolCall{
			ID:   "call-todos",
			Name: "todos",
			Args: map[string]interface{}{
				"todos": []interface{}{1, 2, 3},
			},
		},
		Output: output,
	}, 1, "trace-todos", nil)

	summaryLines, ok := payload["summary_lines"].([]string)
	if !ok {
		t.Fatalf("expected summary lines, got %#v", payload["summary_lines"])
	}
	expected := []string{
		"任务列表已更新: 2 待处理, 1 进行中, 0 已完成",
		"任务列表更新状态: 新增 3, 状态变更 0, 保持 0, 移除 0",
		"当前任务列表:",
		"1. [待处理] 分析需求 (新增)",
		"2. [进行中] 修改实现 (新增)",
		"3. [待处理] 运行测试 (新增)",
	}
	if len(summaryLines) != len(expected) {
		t.Fatalf("expected %d summary lines, got %#v", len(expected), summaryLines)
	}
	for i, line := range expected {
		if summaryLines[i] != line {
			t.Fatalf("expected summary line %d to be %q, got %q", i, line, summaryLines[i])
		}
	}
	if got := payload["arg_preview"]; got != "todos=[3]" {
		t.Fatalf("expected todos arg preview, got %#v", got)
	}
}

func TestToolCompletedEventPayloadFallsBackToEnvelopeSummary(t *testing.T) {
	payload := toolCompletedEventPayload(toolExecutionResult{
		Call: types.ToolCall{
			ID:   "call-3",
			Name: "bash",
			Args: map[string]interface{}{"command": "git status"},
		},
		Envelope: &output.Envelope{
			Summary: "On branch main\nnothing to commit, working tree clean",
		},
	}, 1, "trace-3", nil)

	summaryLines, ok := payload["summary_lines"].([]string)
	if !ok {
		t.Fatalf("expected summary lines, got %#v", payload["summary_lines"])
	}
	expected := []string{
		"On branch main",
		"nothing to commit, working tree clean",
	}
	if len(summaryLines) != len(expected) {
		t.Fatalf("expected %d summary lines, got %#v", len(expected), summaryLines)
	}
	for i, line := range expected {
		if summaryLines[i] != line {
			t.Fatalf("expected summary line %d to be %q, got %q", i, line, summaryLines[i])
		}
	}
	if got := payload["command_text"]; got != "git status" {
		t.Fatalf("expected command text, got %#v", got)
	}
}

func TestToolCompletedEventPayloadCarriesCompleteGitDiffForRendering(t *testing.T) {
	diff := "--- a/app.go\n+++ b/app.go\n@@ -1 +1 @@\n-old\n+new\n"
	payload := toolCompletedEventPayload(toolExecutionResult{
		Call: types.ToolCall{
			ID:   "call-diff",
			Name: "bash",
			Args: map[string]interface{}{"command": "git -C repo diff -- app.go"},
		},
		Output: diff,
		Envelope: &output.Envelope{Metadata: map[string]interface{}{
			"output_capture_complete": true,
		}},
	}, 1, "trace-diff", nil)

	if got := payload["render_output_format"]; got != "diff" {
		t.Fatalf("render_output_format=%#v, want diff", got)
	}
	if got := payload["render_output"]; got != strings.TrimSpace(diff) {
		t.Fatalf("unexpected diff render output: %#v", got)
	}
	if got := payload["render_output_untruncated"]; got != true {
		t.Fatalf("render_output_untruncated=%#v, want true", got)
	}
}

func TestToolCompletedEventPayloadDoesNotRenderTruncatedGitDiff(t *testing.T) {
	payload := toolCompletedEventPayload(toolExecutionResult{
		Call: types.ToolCall{
			ID:   "call-diff-truncated",
			Name: "bash",
			Args: map[string]interface{}{"command": "git diff"},
		},
		Output: "--- a/app.go\n+++ b/app.go\n@@ -1 +1 @@\n-old",
		Envelope: &output.Envelope{Metadata: map[string]interface{}{
			"output_capture_complete": false,
			"capture_limit_reached":   true,
		}},
	}, 1, "trace-diff-truncated", nil)

	if _, ok := payload["render_output"]; ok {
		t.Fatalf("truncated diff must not be marked renderable: %#v", payload)
	}
}

func TestToolCompletedEventPayloadSynthesizesEmptyMutationSummary(t *testing.T) {
	payload := toolCompletedEventPayload(toolExecutionResult{
		Call: types.ToolCall{
			ID:   "call-empty-patch",
			Name: "apply_patch",
		},
		Envelope: &output.Envelope{
			ToolName: "apply_patch",
			Metadata: map[string]interface{}{
				"tool_metadata": map[string]interface{}{
					"mutated_paths": []string{"changed.go"},
				},
			},
		},
	}, 1, "trace-empty-patch", nil)

	want := "Tool completed successfully; changed 1 file: changed.go."
	summaryLines, ok := payload["summary_lines"].([]string)
	if !ok || len(summaryLines) != 1 || summaryLines[0] != want {
		t.Fatalf("expected mutation summary line %q, got %#v", want, payload["summary_lines"])
	}
	if got := payload["render_output"]; got != want {
		t.Fatalf("expected mutation render fallback %q, got %#v", want, got)
	}
}

func TestToolCompletedEventPayloadPrefersErrorOverGenericFallbackSummary(t *testing.T) {
	payload := toolCompletedEventPayload(toolExecutionResult{
		Call: types.ToolCall{
			ID:   "call-3b",
			Name: "execute_shell_command",
			Args: map[string]interface{}{"command": "git status"},
		},
		Error: "exit status 128",
		Envelope: &output.Envelope{
			Summary: "Tool execute_shell_command failed before producing output.",
			Error:   "exit status 128",
		},
	}, 1, "trace-3b", nil)

	summaryLines, ok := payload["summary_lines"].([]string)
	if !ok {
		t.Fatalf("expected summary lines, got %#v", payload["summary_lines"])
	}
	expected := []string{"failed: exit status 128"}
	if len(summaryLines) != len(expected) {
		t.Fatalf("expected %d summary lines, got %#v", len(expected), summaryLines)
	}
	for i, line := range expected {
		if summaryLines[i] != line {
			t.Fatalf("expected summary line %d to be %q, got %q", i, line, summaryLines[i])
		}
	}
}

func TestToolCompletedEventPayloadSkipsToolMetadataAppendix(t *testing.T) {
	payload := toolCompletedEventPayload(toolExecutionResult{
		Call: types.ToolCall{
			ID:   "call-4",
			Name: "view",
			Args: map[string]interface{}{"file_path": "README.md"},
		},
		Output: "line 1\nline 2\nline 3\n\nMetadata:\n{\"file_path\":\"README.md\"}",
	}, 1, "trace-4", nil)

	summaryLines, ok := payload["summary_lines"].([]string)
	if !ok {
		t.Fatalf("expected summary lines, got %#v", payload["summary_lines"])
	}
	expected := []string{"line 1", "line 2", "line 3"}
	if len(summaryLines) != len(expected) {
		t.Fatalf("expected %d summary lines, got %#v", len(expected), summaryLines)
	}
	for i, line := range expected {
		if summaryLines[i] != line {
			t.Fatalf("expected summary line %d to be %q, got %q", i, line, summaryLines[i])
		}
	}
}

func TestToolCompletedEventPayloadMergesAwaitingModelHint(t *testing.T) {
	payload := toolCompletedEventPayload(toolExecutionResult{
		Call: types.ToolCall{
			ID:   "call-5",
			Name: "web_search",
			Args: map[string]interface{}{"query": "weather"},
		},
		Output: "result 1\nresult 2",
	}, 1, "trace-5", map[string]interface{}{
		"awaiting_model": true,
	})

	if got := payload["awaiting_model"]; got != true {
		t.Fatalf("expected awaiting_model=true, got %#v", got)
	}
}

func TestToolCompletedEventPayloadIncludesToolSource(t *testing.T) {
	payload := toolCompletedEventPayload(toolExecutionResult{
		Call: types.ToolCall{
			ID:   "call-source",
			Name: "view",
			Args: map[string]interface{}{"file_path": "README.md"},
		},
		Output: "line 1",
		Envelope: &output.Envelope{
			Metadata: map[string]interface{}{
				toolresult.SourceKey: toolresult.SourceToolkit,
			},
		},
	}, 1, "trace-source", nil)

	if got := payload[toolresult.SourceKey]; got != toolresult.SourceToolkit {
		t.Fatalf("expected %s=%q, got %#v", toolresult.SourceKey, toolresult.SourceToolkit, got)
	}
}

func TestToolCompletedEventPayloadIncludesShellMetadata(t *testing.T) {
	payload := toolCompletedEventPayload(toolExecutionResult{
		Call: types.ToolCall{
			ID:   "call-shell",
			Name: "execute_shell_command",
			Args: map[string]interface{}{"command": "git status"},
		},
		Output: "On branch main",
		Envelope: &output.Envelope{
			Metadata: map[string]interface{}{
				toolresult.SourceKey:   toolresult.SourceToolkit,
				toolresult.MetadataKey: toolresult.KindText,
				"shell_type":           "pwsh",
				"shell_path":           `C:\Program Files\PowerShell\7\pwsh.exe`,
				"shell_display":        `pwsh (C:\Program Files\PowerShell\7\pwsh.exe)`,
			},
		},
	}, 1, "trace-shell", nil)

	if got := payload[toolresult.MetadataKey]; got != toolresult.KindText {
		t.Fatalf("expected %s=%q, got %#v", toolresult.MetadataKey, toolresult.KindText, got)
	}
	if got := payload["shell_type"]; got != "pwsh" {
		t.Fatalf("expected shell_type=pwsh, got %#v", got)
	}
	if got := payload["shell_path"]; got != `C:\Program Files\PowerShell\7\pwsh.exe` {
		t.Fatalf("expected shell_path to be preserved, got %#v", got)
	}
	if got := payload["shell_display"]; got != `pwsh (C:\Program Files\PowerShell\7\pwsh.exe)` {
		t.Fatalf("expected shell_display to be preserved, got %#v", got)
	}
}

func TestToolCompletedEventPayloadIncludesStructuredTimeoutMetadata(t *testing.T) {
	metadata := map[string]interface{}{}
	result := toolExecutionResult{
		Call: types.ToolCall{ID: "call-timeout", Name: "bash"},
	}
	recordToolExecutionOutcome(&result, metadata, "partial output", map[string]interface{}{
		"shell_type": "pwsh",
	}, runtimeerrors.WrapWithContext(
		runtimeerrors.ErrTurnDeadlineExceeded,
		"turn deadline exceeded",
		context.DeadlineExceeded,
		map[string]interface{}{
			"timeout_requested_ms": int64(600000),
			"timeout_effective_ms": int64(30000),
			"timeout_source":       "chat_turn_deadline",
		},
	))
	result.Envelope = &output.Envelope{Metadata: metadata}

	payload := toolCompletedEventPayload(result, 1, "trace-timeout", nil)
	if got := payload["error_code"]; got != string(runtimeerrors.ErrTurnDeadlineExceeded) {
		t.Fatalf("expected structured error code, got %#v", got)
	}
	if got := payload["timeout_requested_ms"]; got != int64(600000) {
		t.Fatalf("expected requested timeout, got %#v", got)
	}
	if got := payload["timeout_effective_ms"]; got != int64(30000) {
		t.Fatalf("expected effective timeout, got %#v", got)
	}
	if got := payload["timeout_source"]; got != "chat_turn_deadline" {
		t.Fatalf("expected timeout source, got %#v", got)
	}
}

func TestToolCompletedEventPayloadPromotesStaleContextDisposition(t *testing.T) {
	// End-to-end: tool-authored STALE_CONTEXT lives in raw toolkit metadata,
	// recordToolExecutionOutcome must promote codes + recovery hints, and
	// tool.completed payload / Diagnose must surface them for chat-log export.
	metadata := map[string]interface{}{"step": 2, "trace_id": "trace-stale-event"}
	result := toolExecutionResult{
		Call: types.ToolCall{ID: "call-edit-stale", Name: "edit"},
	}
	recordToolExecutionOutcome(&result, metadata, "partial", map[string]interface{}{
		"error_code":                 string(runtimeerrors.ErrToolStaleContext),
		"retryable":                  false,
		"failure_class":              "stale_context",
		"file_path":                  `E:\projects\demo\file.go`,
		"suggested_view_offset":      12,
		"suggested_view_limit":       40,
		"current_snippet":            "func Hello() {}\n",
		"current_snippet_start_line": 13,
		"next_action":                "STALE_CONTEXT: copy current_snippet then rebuild; do not retry the same stale old_string unchanged.",
	}, fmt.Errorf("old_string 未在文件中找到；edit 只执行精确匹配"))
	result.Envelope = &output.Envelope{Metadata: metadata}

	payload := toolCompletedEventPayload(result, 2, "trace-stale-event", nil)
	if payload["error_code"] != string(runtimeerrors.ErrToolStaleContext) {
		t.Fatalf("payload error_code=%v want STALE_CONTEXT meta=%#v", payload["error_code"], metadata)
	}
	if payload["ok"] != false {
		t.Fatalf("expected ok=false, got %#v", payload)
	}
	if payload["retryable"] != false {
		t.Fatalf("expected retryable=false, got %#v", payload)
	}
	next, _ := payload["next_action"].(string)
	if !strings.Contains(next, "STALE_CONTEXT") || !strings.Contains(next, "current_snippet") {
		t.Fatalf("expected tool-authored STALE_CONTEXT next_action, got %q", next)
	}
	if payload["failure_class"] != "stale_context" {
		t.Fatalf("expected failure_class=stale_context, got %#v", payload["failure_class"])
	}
	if payload["file_path"] != `E:\projects\demo\file.go` {
		t.Fatalf("expected file_path promoted, got %#v", payload["file_path"])
	}
	if offset, _ := payload["suggested_view_offset"].(int); offset != 12 {
		t.Fatalf("expected suggested_view_offset=12, got %#v", payload["suggested_view_offset"])
	}
	if limit, _ := payload["suggested_view_limit"].(int); limit != 40 {
		t.Fatalf("expected suggested_view_limit=40, got %#v", payload["suggested_view_limit"])
	}
	if snippet, _ := payload["current_snippet"].(string); !strings.Contains(snippet, "func Hello") {
		t.Fatalf("expected current_snippet on payload, got %#v", payload["current_snippet"])
	}
	if start, _ := payload["current_snippet_start_line"].(int); start != 13 {
		t.Fatalf("expected current_snippet_start_line=13, got %#v", payload["current_snippet_start_line"])
	}
	if payload[toolresult.MetadataOutcomeKey] != toolresult.OutcomeFailed {
		t.Fatalf("expected outcome=failed, got %#v", payload[toolresult.MetadataOutcomeKey])
	}
}

func TestToolCompletedEventPayloadRefinesGenericToolExecutionEditMiss(t *testing.T) {
	// Mirrors live chat-log shape: TOOL_EXECUTION + generic next_action stamped
	// on envelope metadata while the error body is an edit old_string miss.
	// Diagnose must refine so chat-log export stops counting bare TOOL_EXECUTION.
	result := toolExecutionResult{
		Call:  types.ToolCall{ID: "call-edit-generic", Name: "edit"},
		Error: "old_string 未在文件中找到；edit 只执行精确匹配（包括空格、缩进和换行），不会自动模糊定位。",
		Envelope: &output.Envelope{Metadata: map[string]interface{}{
			"error_code":  string(runtimeerrors.ErrToolExecution),
			"retryable":   true,
			"next_action": toolresult.DefaultToolExecutionNextAction,
		}},
	}
	payload := toolCompletedEventPayload(result, 3, "trace-edit-refine", nil)
	if payload["error_code"] != string(runtimeerrors.ErrToolStaleContext) {
		t.Fatalf("payload error_code=%v want STALE_CONTEXT payload=%#v", payload["error_code"], payload)
	}
	if payload["ok"] != false {
		t.Fatalf("expected ok=false, got %#v", payload)
	}
	if payload["retryable"] != false {
		t.Fatalf("refined STALE_CONTEXT must not be retryable, got %#v", payload["retryable"])
	}
	next, _ := payload["next_action"].(string)
	if next == toolresult.DefaultToolExecutionNextAction || strings.HasPrefix(strings.ToLower(next), "inspect the error details") {
		t.Fatalf("generic next_action must yield to stale recovery, got %q", next)
	}
	if payload[toolresult.MetadataOutcomeKey] != toolresult.OutcomeFailed {
		t.Fatalf("expected outcome=failed, got %#v", payload[toolresult.MetadataOutcomeKey])
	}
}

func TestToolCompletedEventPayloadRehydratesSnippetFromErrorBody(t *testing.T) {
	// Live residual: toolkit metadata lost current_snippet; only error body has
	// multi-line closest block. tool.completed / chat-log must still export it.
	errBody := "old_string 未在文件中找到；已尝试 CRLF/LF。\n" +
		"建议从第 10 行附近用 view 重读（suggested_view_offset=9）。\n" +
		"最接近的当前内容（第 10 行附近，可直接据此重建 old_string）:\n" +
		"    10|\tfunc Hello() {\n" +
		"    11|\t\treturn\n" +
		"    12|\t}\n" +
		"next_action: 优先用上方“最接近的当前内容”"
	result := toolExecutionResult{
		Call:  types.ToolCall{ID: "call-edit-rehydrate", Name: "edit"},
		Error: errBody,
		Envelope: &output.Envelope{Metadata: map[string]interface{}{
			"error_code":  string(runtimeerrors.ErrToolExecution),
			"next_action": toolresult.DefaultToolExecutionNextAction,
		}},
	}
	payload := toolCompletedEventPayload(result, 4, "trace-rehydrate", nil)
	if payload["error_code"] != string(runtimeerrors.ErrToolStaleContext) {
		t.Fatalf("error_code=%v want STALE_CONTEXT", payload["error_code"])
	}
	snip, _ := payload["current_snippet"].(string)
	if !strings.Contains(snip, "func Hello()") {
		t.Fatalf("expected rehydrated current_snippet, got %#v", payload["current_snippet"])
	}
	if !strings.Contains(snip, "\tfunc Hello") {
		t.Fatalf("expected indent preserved in snippet, got %q", snip)
	}
	if offset, _ := payload["suggested_view_offset"].(int); offset != 9 {
		t.Fatalf("suggested_view_offset=%#v want 9", payload["suggested_view_offset"])
	}
	// Nested protocol_result thin metadata should also carry snippet.
	proto, _ := payload["protocol_result"].(map[string]interface{})
	if proto == nil {
		t.Fatalf("expected protocol_result, payload=%#v", payload)
	}
	meta, _ := proto["metadata"].(map[string]interface{})
	if meta == nil {
		t.Fatalf("expected protocol_result.metadata, got %#v", proto)
	}
	if psnip, _ := meta["current_snippet"].(string); !strings.Contains(psnip, "func Hello") {
		t.Fatalf("protocol_result.metadata.current_snippet missing: %#v", meta)
	}
}

func TestToolCompletedEventPayloadIncludesActionableDiagnostic(t *testing.T) {
	result := toolExecutionResult{
		Call:  types.ToolCall{ID: "call-missing", Name: "task_output"},
		Error: "background job not found",
		Envelope: &output.Envelope{Metadata: map[string]interface{}{
			"ok":          false,
			"error_code":  "JOB_NOT_FOUND",
			"retryable":   false,
			"next_action": "Use the exact job_id returned by background_task.",
		}},
	}

	payload := toolCompletedEventPayload(result, 1, "trace-diagnostic", nil)
	if payload["ok"] != false || payload["error_code"] != "JOB_NOT_FOUND" || payload["retryable"] != false {
		t.Fatalf("expected structured failure diagnostic, got %#v", payload)
	}
	if payload["next_action"] != "Use the exact job_id returned by background_task." {
		t.Fatalf("expected next action, got %#v", payload)
	}
	if payload[toolresult.MetadataOutcomeKey] != toolresult.OutcomeFailed {
		t.Fatalf("expected outcome=failed on hard failure, got %#v", payload[toolresult.MetadataOutcomeKey])
	}
}

func TestToolCompletedEventPayloadExportsEmptySuccessDisposition(t *testing.T) {
	payload := toolCompletedEventPayload(toolExecutionResult{
		Call: types.ToolCall{
			ID:   "call-empty-grep",
			Name: "grep",
			Args: map[string]interface{}{"pattern": "no-such-token"},
		},
		Output: "No matches found.",
		Envelope: &output.Envelope{
			Metadata: map[string]interface{}{
				toolresult.MetadataOKKey:          true,
				toolresult.MetadataEmptyResultKey: true,
				toolresult.MetadataOutcomeKey:     toolresult.OutcomeEmpty,
				toolresult.MetadataNextActionKey:  "Broaden the query or confirm the search scope; empty success is valid evidence.",
				"match_count":                     0,
			},
		},
	}, 1, "trace-empty", nil)

	if payload[toolresult.MetadataOKKey] != true {
		t.Fatalf("expected ok=true for empty success, got %#v", payload)
	}
	if payload[toolresult.MetadataOutcomeKey] != toolresult.OutcomeEmpty {
		t.Fatalf("expected outcome=empty, got %#v", payload[toolresult.MetadataOutcomeKey])
	}
	if payload[toolresult.MetadataEmptyResultKey] != true {
		t.Fatalf("expected empty_result=true, got %#v", payload[toolresult.MetadataEmptyResultKey])
	}
	if got := strings.TrimSpace(fmt.Sprint(payload[toolresult.MetadataNextActionKey])); got == "" || got == "<nil>" {
		t.Fatalf("expected empty-success next_action, got %#v", payload[toolresult.MetadataNextActionKey])
	}
}

func TestToolCompletedEventPayloadExportsPartialBatchDisposition(t *testing.T) {
	payload := toolCompletedEventPayload(toolExecutionResult{
		Call: types.ToolCall{
			ID:   "call-partial-view",
			Name: "view",
			Args: map[string]interface{}{
				"files": []interface{}{
					map[string]interface{}{"file_path": "a.go"},
					map[string]interface{}{"file_path": "missing.go"},
				},
			},
		},
		Output: "partial batch body",
		Envelope: &output.Envelope{
			Metadata: map[string]interface{}{
				toolresult.MetadataOKKey:             true,
				toolresult.MetadataOutcomeKey:        toolresult.OutcomePartial,
				toolresult.MetadataPartialFailureKey: true,
				toolresult.MetadataRequestedCountKey: 2,
				toolresult.MetadataFailedCountKey:    1,
				toolresult.MetadataSucceededCountKey: 1,
				toolresult.MetadataFailedItemsKey: []map[string]interface{}{
					toolresult.FailedItemMap(toolresult.IntPtr(1), "missing.go", "", "path does not exist"),
				},
			},
		},
	}, 2, "trace-partial", nil)

	if payload[toolresult.MetadataOKKey] != true {
		t.Fatalf("expected ok=true for partial batch, got %#v", payload)
	}
	if payload[toolresult.MetadataOutcomeKey] != toolresult.OutcomePartial {
		t.Fatalf("expected outcome=partial, got %#v", payload[toolresult.MetadataOutcomeKey])
	}
	if payload[toolresult.MetadataPartialFailureKey] != true {
		t.Fatalf("expected partial_failure=true, got %#v", payload[toolresult.MetadataPartialFailureKey])
	}
	if payload[toolresult.MetadataRequestedCountKey] != 2 {
		t.Fatalf("expected requested_count=2, got %#v", payload[toolresult.MetadataRequestedCountKey])
	}
	if payload[toolresult.MetadataFailedCountKey] != 1 {
		t.Fatalf("expected failed_count=1, got %#v", payload[toolresult.MetadataFailedCountKey])
	}
	if payload[toolresult.MetadataSucceededCountKey] != 1 {
		t.Fatalf("expected succeeded_count=1, got %#v", payload[toolresult.MetadataSucceededCountKey])
	}
	rawItems, ok := payload[toolresult.MetadataFailedItemsKey].([]map[string]interface{})
	if !ok || len(rawItems) == 0 {
		t.Fatalf("expected failed_items on payload, got %#v", payload[toolresult.MetadataFailedItemsKey])
	}
	if got := fmt.Sprint(rawItems[0]["path"]); got != "missing.go" {
		t.Fatalf("expected failed path missing.go, got %#v", rawItems[0])
	}
	if got := strings.TrimSpace(fmt.Sprint(payload[toolresult.MetadataNextActionKey])); got == "" || got == "<nil>" {
		t.Fatalf("expected partial next_action, got %#v", payload[toolresult.MetadataNextActionKey])
	}
}

func TestToolCompletedEventPayloadDerivesPartialFromNestedToolMetadata(t *testing.T) {
	// Regression for live smoke: nested toolkit batch stats were copied onto the
	// chat-log payload (partial_failure/counts) while outcome stayed success
	// because Diagnose could not read nested integer counts.
	payload := toolCompletedEventPayload(toolExecutionResult{
		Call: types.ToolCall{
			ID:   "call-nested-partial-view",
			Name: "view",
			Args: map[string]interface{}{
				"files": []interface{}{
					map[string]interface{}{"file_path": "a.go"},
					map[string]interface{}{"file_path": "missing.go"},
				},
			},
		},
		Output: "===== a.go =====\nok\n\n===== errors =====\nmissing.go: missing",
		Envelope: &output.Envelope{
			Metadata: map[string]interface{}{
				"tool_metadata": map[string]interface{}{
					"batch":           true,
					"request_count":   2,
					"succeeded_count": 1,
					"failed_count":    1,
					"partial_failure": true,
					toolresult.MetadataFailedItemsKey: []map[string]interface{}{
						toolresult.FailedItemMap(toolresult.IntPtr(1), "missing.go", "missing.go", "path does not exist"),
					},
				},
			},
		},
	}, 1, "trace-nested-partial", nil)

	if payload[toolresult.MetadataOKKey] != true {
		t.Fatalf("expected ok=true, got %#v", payload)
	}
	if payload[toolresult.MetadataOutcomeKey] != toolresult.OutcomePartial {
		t.Fatalf("expected outcome=partial from nested tool_metadata, got %#v", payload[toolresult.MetadataOutcomeKey])
	}
	if payload[toolresult.MetadataPartialFailureKey] != true {
		t.Fatalf("expected partial_failure=true, got %#v", payload[toolresult.MetadataPartialFailureKey])
	}
	if payload[toolresult.MetadataRequestedCountKey] != 2 ||
		payload[toolresult.MetadataFailedCountKey] != 1 ||
		payload[toolresult.MetadataSucceededCountKey] != 1 {
		t.Fatalf("expected nested counts on payload, got %#v", payload)
	}
	if got := strings.TrimSpace(fmt.Sprint(payload[toolresult.MetadataNextActionKey])); got == "" || got == "<nil>" {
		t.Fatalf("expected partial next_action, got %#v", payload[toolresult.MetadataNextActionKey])
	}
}

func TestToolCompletedEventPayloadExportsOrdinarySuccessOutcome(t *testing.T) {
	payload := toolCompletedEventPayload(toolExecutionResult{
		Call: types.ToolCall{
			ID:   "call-success",
			Name: "ls",
			Args: map[string]interface{}{"path": "."},
		},
		Output: "main.go",
		Envelope: &output.Envelope{
			Metadata: map[string]interface{}{
				toolresult.MetadataOKKey: true,
				"file_count":             1,
			},
		},
	}, 1, "trace-success", nil)

	if payload[toolresult.MetadataOKKey] != true {
		t.Fatalf("expected ok=true, got %#v", payload)
	}
	if payload[toolresult.MetadataOutcomeKey] != toolresult.OutcomeSuccess {
		t.Fatalf("expected outcome=success, got %#v", payload[toolresult.MetadataOutcomeKey])
	}
	if payload[toolresult.MetadataEmptyResultKey] == true {
		t.Fatalf("ordinary success must not set empty_result: %#v", payload)
	}
	if _, exists := payload[toolresult.MetadataFailedItemsKey]; exists {
		t.Fatalf("ordinary success must not set failed_items: %#v", payload)
	}
}

func TestFinalizeDeniedToolResultEmitsCompletedWithFailedOutcome(t *testing.T) {
	agent := &Agent{config: &Config{Name: "test-agent", Model: "test-model"}}
	bus := runtimeevents.NewBus()
	var completed []runtimeevents.Event
	bus.Subscribe("tool.completed", func(event runtimeevents.Event) {
		completed = append(completed, event)
	})
	var reduced []runtimeevents.Event
	bus.Subscribe("tool.reduced", func(event runtimeevents.Event) {
		reduced = append(reduced, event)
	})
	agent.SetEventBus(bus)

	loop := NewReActLoop(agent, nil, &LoopReActConfig{MaxSteps: 1, EnableToolCalls: true})
	gateway := output.NewGateway(nil)
	tc := types.ToolCall{ID: "call-denied", Name: "write_file", Args: map[string]interface{}{"path": "x.txt"}}
	result := toolExecutionResult{Call: tc, Error: "read-only policy blocks write-like tool"}
	metadata := map[string]interface{}{}

	got := loop.finalizeDeniedToolResult(context.Background(), gateway, "session-denied", tc, 1, "trace-denied", result, metadata, nil)
	if got.Envelope == nil {
		t.Fatal("expected envelope after denied finalize")
	}
	if len(completed) != 1 {
		t.Fatalf("expected 1 tool.completed, got %d", len(completed))
	}
	payload := completed[0].Payload
	if payload["denied"] != true {
		t.Fatalf("expected denied=true, got %#v", payload)
	}
	if payload[toolresult.MetadataOutcomeKey] != toolresult.OutcomeFailed {
		t.Fatalf("expected outcome=failed, got %#v", payload[toolresult.MetadataOutcomeKey])
	}
	if payload[toolresult.MetadataOKKey] != false {
		t.Fatalf("expected ok=false, got %#v", payload[toolresult.MetadataOKKey])
	}
	if len(reduced) != 1 {
		t.Fatalf("expected 1 tool.reduced, got %d", len(reduced))
	}
}
