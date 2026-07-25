package agent

import (
	"context"
	"fmt"
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
