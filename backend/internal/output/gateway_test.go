package output

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/artifact"
	"github.com/wwsheng009/ai-agent-runtime/internal/observability"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolbroker"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolresult"
)

func TestGateway_StoresRawOutputAndReturnsReducedEnvelope(t *testing.T) {
	store, err := artifact.NewStore(nil)
	if err != nil {
		t.Fatalf("create artifact store: %v", err)
	}
	defer func() { _ = store.Close() }()

	gateway := NewGateway(store, NewTextReducer(80, 3))
	rawOutput := strings.Join([]string{
		"line 1: preparing",
		"line 2: unique-needle",
		"line 3: details",
		"line 4: more details",
		"line 5: tail",
	}, "\n")

	envelope, err := gateway.Process(context.Background(), RawToolResult{
		SessionID:  "session-1",
		ToolName:   "run_command_readonly",
		ToolCallID: "call-1",
		Content:    rawOutput,
		Metadata: map[string]interface{}{
			"source": "test",
		},
	})
	if err != nil {
		t.Fatalf("process tool output: %v", err)
	}
	if envelope == nil {
		t.Fatal("expected envelope, got nil")
	}
	if envelope.Metadata["reducer"] != "text_truncation" {
		t.Fatalf("expected text_truncation reducer, got %v", envelope.Metadata["reducer"])
	}
	if len(envelope.ArtifactIDs) != 1 {
		t.Fatalf("expected 1 artifact id, got %d", len(envelope.ArtifactIDs))
	}
	if envelope.Metadata["artifact_id"] != envelope.ArtifactIDs[0] {
		t.Fatalf("expected canonical artifact_id metadata, got %#v", envelope.Metadata["artifact_id"])
	}
	if envelope.Metadata["byte_count"] != len(rawOutput) {
		t.Fatalf("expected byte_count=%d, got %#v", len(rawOutput), envelope.Metadata["byte_count"])
	}
	if digest, _ := envelope.Metadata["sha256"].(string); len(digest) != 64 {
		t.Fatalf("expected sha256 digest, got %#v", envelope.Metadata["sha256"])
	}
	if strings.Contains(envelope.Summary, "line 5: tail") {
		t.Fatal("expected summary to be truncated before the last line")
	}
	if strings.Contains(envelope.Render(), "artifact_refs:") {
		t.Fatalf("expected rendered envelope to omit artifact refs, got %q", envelope.Render())
	}

	record, err := store.Get(context.Background(), envelope.ArtifactIDs[0])
	if err != nil {
		t.Fatalf("load artifact: %v", err)
	}
	if record == nil {
		t.Fatal("expected stored artifact record")
	}
	if record.Content != rawOutput {
		t.Fatalf("expected raw output to be stored intact, got %q", record.Content)
	}

	hits, err := store.Search(context.Background(), "session-1", "unique-needle", 5)
	if err != nil {
		t.Fatalf("search artifacts: %v", err)
	}
	if len(hits) == 0 {
		t.Fatal("expected artifact search to find stored record")
	}
	if hits[0].ID != envelope.ArtifactIDs[0] {
		t.Fatalf("expected hit id %s, got %s", envelope.ArtifactIDs[0], hits[0].ID)
	}
}

func TestGateway_DefaultReducers_HandleCommonFormats(t *testing.T) {
	store, err := artifact.NewStore(nil)
	if err != nil {
		t.Fatalf("create artifact store: %v", err)
	}
	defer func() { _ = store.Close() }()

	gateway := NewGateway(store)
	testCases := []struct {
		name            string
		content         string
		expectedReducer string
	}{
		{
			name: "json",
			content: `{
  "status": "ok",
  "items": [{"id":"a"},{"id":"b"}]
}`,
			expectedReducer: "json_summary",
		},
		{
			name: "table",
			content: strings.Join([]string{
				"NAME\tSTATUS",
				"job-a\tpassed",
				"job-b\tfailed",
			}, "\n"),
			expectedReducer: "table_summary",
		},
		{
			name: "log",
			content: strings.Join([]string{
				"2026-03-14 10:00:01 INFO starting worker",
				"2026-03-14 10:00:02 ERROR failed to fetch artifact",
			}, "\n"),
			expectedReducer: "log_summary",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			envelope, err := gateway.Process(context.Background(), RawToolResult{
				SessionID:  "session-1",
				ToolName:   "test_tool",
				ToolCallID: "call-" + tc.name,
				Content:    tc.content,
			})
			if err != nil {
				t.Fatalf("process %s output: %v", tc.name, err)
			}
			if envelope == nil {
				t.Fatalf("expected envelope for %s", tc.name)
			}
			if envelope.Metadata["reducer"] != tc.expectedReducer {
				t.Fatalf("expected reducer %s, got %v", tc.expectedReducer, envelope.Metadata["reducer"])
			}
			if len(envelope.ArtifactIDs) != 1 {
				t.Fatalf("expected artifact refs for %s, got %v", tc.name, envelope.ArtifactIDs)
			}
			if strings.TrimSpace(envelope.Summary) == "" {
				t.Fatalf("expected non-empty summary for %s", tc.name)
			}
		})
	}
}

func TestGateway_AskUserQuestionResultPreservesAnswerInJSONSummary(t *testing.T) {
	gateway := NewGateway(nil)

	envelope, err := gateway.Process(context.Background(), RawToolResult{
		SessionID:  "session-question",
		ToolName:   toolbroker.ToolAskUserQuestion,
		ToolCallID: "call-question",
		Content: toolbroker.AskUserQuestionResult{
			QuestionID: "question-1",
			Answer:     "provided answer 42",
		},
	})
	if err != nil {
		t.Fatalf("process ask_user_question result: %v", err)
	}
	if envelope == nil {
		t.Fatal("expected envelope")
	}
	if envelope.Metadata["reducer"] != "json_summary" {
		t.Fatalf("expected json_summary reducer, got %v", envelope.Metadata["reducer"])
	}
	if !strings.Contains(envelope.Summary, "answer=provided answer 42") {
		t.Fatalf("expected reducer summary to preserve answer, got %q", envelope.Summary)
	}
	if strings.TrimSpace(envelope.Render()) == "" {
		t.Fatalf("expected rendered envelope to stay non-empty, got %+v", envelope)
	}
}

func TestGateway_BackgroundTaskResultPreservesJobIDInJSONSummary(t *testing.T) {
	gateway := NewGateway(nil)

	envelope, err := gateway.Process(context.Background(), RawToolResult{
		SessionID:  "session-background",
		ToolName:   toolbroker.ToolBackgroundTask,
		ToolCallID: "call-background",
		Content: toolbroker.BackgroundTaskResult{
			JobID:         "job_test123",
			Status:        "pending",
			RestartPolicy: "fail",
		},
	})
	if err != nil {
		t.Fatalf("process background_task result: %v", err)
	}
	if envelope == nil {
		t.Fatal("expected envelope")
	}
	if envelope.Metadata["reducer"] != "json_summary" {
		t.Fatalf("expected json_summary reducer, got %v", envelope.Metadata["reducer"])
	}
	if !strings.Contains(envelope.Summary, "job_id=job_test123") {
		t.Fatalf("expected reducer summary to preserve job_id, got %q", envelope.Summary)
	}
	if !strings.Contains(envelope.Render(), "job_id=job_test123") {
		t.Fatalf("expected rendered envelope to preserve job_id, got %q", envelope.Render())
	}
}

func TestGateway_UsesCacheSafeSummaryOverrideFromToolMetadata(t *testing.T) {
	gateway := NewGateway(nil)

	envelope, err := gateway.Process(context.Background(), RawToolResult{
		SessionID:  "session-cache-safe",
		ToolName:   toolbroker.ToolSpawnTeam,
		ToolCallID: "call-cache-safe",
		Content: map[string]interface{}{
			"team_id":  "team-dynamic-123",
			"task_ids": []string{"task-dynamic-456"},
		},
		Metadata: map[string]interface{}{
			"tool_metadata": map[string]interface{}{
				"cache_safe_summary": "Created team run with 0 teammates and 1 tasks. Background orchestration not auto-started.",
			},
		},
	})
	if err != nil {
		t.Fatalf("process cache-safe summary result: %v", err)
	}
	if envelope == nil {
		t.Fatal("expected envelope")
	}
	if envelope.Render() != "Created team run with 0 teammates and 1 tasks. Background orchestration not auto-started." {
		t.Fatalf("unexpected rendered envelope: %q", envelope.Render())
	}
	if strings.Contains(envelope.Render(), "team-dynamic-123") || strings.Contains(envelope.Render(), "task-dynamic-456") {
		t.Fatalf("expected rendered envelope to omit dynamic ids, got %q", envelope.Render())
	}
	if override, ok := envelope.Metadata["summary_override"].(bool); !ok || !override {
		t.Fatalf("expected summary_override metadata, got %+v", envelope.Metadata)
	}
}

func TestGateway_PromotesToolSourceFromNestedToolMetadata(t *testing.T) {
	gateway := NewGateway(nil)

	envelope, err := gateway.Process(context.Background(), RawToolResult{
		SessionID:  "session-source",
		ToolName:   "view",
		ToolCallID: "call-source",
		Content:    "hello",
		Metadata: map[string]interface{}{
			"tool_metadata": map[string]interface{}{
				toolresult.SourceKey: toolresult.SourceToolkit,
			},
		},
	})
	if err != nil {
		t.Fatalf("process tool output: %v", err)
	}
	if envelope == nil {
		t.Fatal("expected envelope")
	}
	if got := envelope.Metadata[toolresult.SourceKey]; got != toolresult.SourceToolkit {
		t.Fatalf("expected %s=%q, got %#v", toolresult.SourceKey, toolresult.SourceToolkit, got)
	}
}

func TestGateway_PrefersReducerSummaryForLargeGoTestOutput(t *testing.T) {
	gateway := NewGateway(nil)
	raw := strings.Repeat("verbose log line\n", 1000) +
		"--- FAIL: TestRecovery (0.01s)\nFAIL\tgithub.com/demo/agent\t0.1s\n"
	envelope, err := gateway.Process(context.Background(), RawToolResult{
		ToolName: "bash", ToolCallID: "call-go-test", Content: raw, Error: "exit status 1",
		Metadata: map[string]interface{}{"command": "go test ./internal/agent -count=1"},
	})
	if err != nil || envelope == nil {
		t.Fatalf("process go test output: envelope=%#v err=%v", envelope, err)
	}
	if envelope.Metadata["reducer"] != "go_test_text" {
		t.Fatalf("expected go_test_text reducer, got %#v", envelope.Metadata)
	}
	if preferred, _ := envelope.Metadata["model_summary_preferred"].(bool); !preferred {
		t.Fatalf("expected model summary preference for large test output, got %#v", envelope.Metadata)
	}
}

func TestGateway_AddsActionableInvalidArgumentDiagnostic(t *testing.T) {
	gateway := NewGateway(nil)
	envelope, err := gateway.Process(context.Background(), RawToolResult{
		ToolName:   "background_task",
		ToolCallID: "call-invalid",
		Error:      "command is required",
	})
	if err != nil || envelope == nil {
		t.Fatalf("process invalid tool result: envelope=%#v err=%v", envelope, err)
	}
	if envelope.OK || envelope.Metadata["ok"] != false {
		t.Fatalf("expected failed invocation diagnostic, got %#v", envelope)
	}
	if envelope.ErrorCode != "TOOL_INVALID_ARGS" || envelope.Metadata["error_code"] != "TOOL_INVALID_ARGS" {
		t.Fatalf("expected invalid-args code, got %#v", envelope)
	}
	if envelope.Retryable || envelope.Metadata["retryable"] != false {
		t.Fatalf("invalid arguments must not be blindly retried: %#v", envelope.Metadata)
	}
	if strings.TrimSpace(envelope.NextAction) == "" || envelope.Metadata["next_action"] != envelope.NextAction {
		t.Fatalf("expected corrective next action, got %#v", envelope.Metadata)
	}
}

func TestGateway_ShellFailureGetsMessageAwareNextAction(t *testing.T) {
	gateway := NewGateway(nil)
	// Dominant efficiency-report shape: bare exit status with no structured code.
	envelope, err := gateway.Process(context.Background(), RawToolResult{
		ToolName:   "bash",
		ToolCallID: "call-shell-exit",
		Error:      "exit status 1",
		Args: map[string]interface{}{
			"command": "go test ./...",
		},
	})
	if err != nil || envelope == nil {
		t.Fatalf("process shell failure: envelope=%#v err=%v", envelope, err)
	}
	if envelope.OK || envelope.ErrorCode != "TOOL_EXECUTION" {
		t.Fatalf("expected TOOL_EXECUTION shell failure, got %#v", envelope)
	}
	if envelope.Retryable {
		t.Fatalf("bare shell exit must not be blindly retryable: %#v", envelope)
	}
	if !strings.Contains(envelope.NextAction, "Do not replay the identical command blindly") {
		t.Fatalf("expected bare-exit next_action, got %q", envelope.NextAction)
	}
	if envelope.Metadata["next_action"] != envelope.NextAction {
		t.Fatalf("expected envelope/metadata next_action sync, got %#v", envelope.Metadata)
	}
	// Recovery-relevant failed disposition should surface compact attempted_args.
	if args, ok := envelope.Metadata[toolresult.MetadataAttemptedArgsKey].(map[string]interface{}); !ok || args["command"] == nil {
		t.Fatalf("expected attempted_args on shell failure, got %#v", envelope.Metadata)
	}

	// Windows dialect mismatch should promote TOOL_SHELL_COMPAT.
	compat, err := gateway.Process(context.Background(), RawToolResult{
		ToolName:   "bash",
		ToolCallID: "call-shell-head",
		Error:      "head : The term 'head' is not recognized as a name of a cmdlet",
	})
	if err != nil || compat == nil {
		t.Fatalf("process shell-compat: envelope=%#v err=%v", compat, err)
	}
	if compat.ErrorCode != "TOOL_SHELL_COMPAT" {
		t.Fatalf("expected TOOL_SHELL_COMPAT, got %#v", compat)
	}
	if !strings.Contains(compat.NextAction, "Select-Object") && !strings.Contains(compat.NextAction, "shell-native") {
		t.Fatalf("expected shell-compat next_action, got %q", compat.NextAction)
	}
}

func TestGateway_SuccessfulTaskOutputKeepsUnderlyingFailureSeparate(t *testing.T) {
	gateway := NewGateway(nil)
	envelope, err := gateway.Process(context.Background(), RawToolResult{
		ToolName:   "task_output",
		ToolCallID: "call-output",
		Content:    map[string]interface{}{"status": "timed_out", "error_code": "TOOL_TIMEOUT"},
		Metadata:   map[string]interface{}{"error_code": "TOOL_TIMEOUT"},
	})
	if err != nil || envelope == nil {
		t.Fatalf("process task output: envelope=%#v err=%v", envelope, err)
	}
	if !envelope.OK || envelope.ErrorCode != "" || envelope.Metadata["ok"] != true {
		t.Fatalf("query success must stay separate from job failure: %#v", envelope)
	}
	if envelope.Metadata["error_code"] != "TOOL_TIMEOUT" {
		t.Fatalf("expected underlying job code to remain available, got %#v", envelope.Metadata)
	}
}

func TestGateway_MarksEmptySuccessfulResult(t *testing.T) {
	gateway := NewGateway(nil)
	envelope, err := gateway.Process(context.Background(), RawToolResult{
		ToolName:   "grep",
		ToolCallID: "call-empty",
		Content:    "",
	})
	if err != nil || envelope == nil {
		t.Fatalf("process empty success: envelope=%#v err=%v", envelope, err)
	}
	if !envelope.OK {
		t.Fatalf("empty success must remain ok: %#v", envelope)
	}
	if envelope.Metadata["empty_result"] != true {
		t.Fatalf("expected empty_result metadata, got %#v", envelope.Metadata)
	}
	if envelope.Metadata["outcome"] != toolresult.OutcomeEmpty {
		t.Fatalf("expected outcome=empty, got %#v", envelope.Metadata)
	}
	if next, _ := envelope.Metadata["next_action"].(string); !strings.Contains(next, "Empty successful result") {
		t.Fatalf("expected empty-success next_action, got %#v", envelope.Metadata)
	}
	if !strings.Contains(envelope.NextAction, "Empty successful result") {
		t.Fatalf("expected envelope.NextAction for empty success, got %q", envelope.NextAction)
	}
}

func TestGateway_PartialBatchOutcomeAndNextAction(t *testing.T) {
	gateway := NewGateway(nil)
	envelope, err := gateway.Process(context.Background(), RawToolResult{
		ToolName:   "bash",
		ToolCallID: "call-batch",
		Content:    "===== command 1/2 [ok] =====\nok\n===== command 2/2 [failed] =====\nbad",
		Error:      "bash command batch completed with 1 failure(s)",
		Metadata: map[string]interface{}{
			"batch":           true,
			"failed_count":    1,
			"requested_count": 2,
		},
	})
	if err != nil || envelope == nil {
		t.Fatalf("process partial batch: envelope=%#v err=%v", envelope, err)
	}
	if envelope.OK {
		t.Fatalf("partial batch should keep overall failure: %#v", envelope)
	}
	if envelope.Metadata["outcome"] != toolresult.OutcomePartial {
		t.Fatalf("expected outcome=partial, got %#v", envelope.Metadata)
	}
	if !strings.Contains(envelope.NextAction, "Reuse successful") {
		t.Fatalf("expected partial next_action on envelope, got %q", envelope.NextAction)
	}
	if !strings.Contains(fmt.Sprint(envelope.Metadata["next_action"]), "Do not re-run the entire batch") {
		t.Fatalf("expected batch recovery metadata, got %#v", envelope.Metadata)
	}
}

func TestGateway_PartialBatchFromSuccessPathAliases(t *testing.T) {
	gateway := NewGateway(nil)
	envelope, err := gateway.Process(context.Background(), RawToolResult{
		ToolName:   "view",
		ToolCallID: "call-view-partial",
		Content:    "===== a.go =====\nok\n\n===== errors =====\nb.go: missing",
		Metadata: map[string]interface{}{
			"batch":           true,
			"request_count":   3,
			"succeeded_count": 2,
			"failed_count":    1,
			"partial_failure": true,
		},
	})
	if err != nil || envelope == nil {
		t.Fatalf("process success-path partial: envelope=%#v err=%v", envelope, err)
	}
	if !envelope.OK {
		t.Fatalf("success-path partial should remain ok: %#v", envelope)
	}
	if envelope.Metadata["outcome"] != toolresult.OutcomePartial {
		t.Fatalf("expected outcome=partial, got %#v", envelope.Metadata)
	}
	if envelope.Metadata["requested_count"] != 3 || envelope.Metadata["failed_count"] != 1 || envelope.Metadata["succeeded_count"] != 2 {
		t.Fatalf("expected normalized batch counts, got %#v", envelope.Metadata)
	}
	if !strings.Contains(fmt.Sprint(envelope.Metadata["next_action"]), "Reuse successful") {
		t.Fatalf("expected partial recovery metadata, got %#v", envelope.Metadata)
	}
}

func TestGateway_PartialBatchPromotesFailedItems(t *testing.T) {
	gateway := NewGateway(nil)
	envelope, err := gateway.Process(context.Background(), RawToolResult{
		ToolName:   "bash",
		ToolCallID: "call-batch-items",
		Content:    "===== command 1/2 [ok] =====\nok\n===== command 2/2 [failed] =====\nbad",
		Error:      "bash command batch completed with 1 failure(s)",
		Metadata: map[string]interface{}{
			"batch":           true,
			"failed_count":    1,
			"requested_count": 2,
			"items": []interface{}{
				map[string]interface{}{"index": 0, "command": "echo ok", "success": true},
				map[string]interface{}{"index": 1, "command": "bad-cmd", "success": false, "error": "exit status 1"},
			},
		},
	})
	if err != nil || envelope == nil {
		t.Fatalf("process partial batch items: envelope=%#v err=%v", envelope, err)
	}
	if envelope.Metadata["outcome"] != toolresult.OutcomePartial {
		t.Fatalf("expected outcome=partial, got %#v", envelope.Metadata)
	}
	if envelope.Metadata[toolresult.MetadataFailedItemsKey] == nil {
		t.Fatalf("expected failed_items promotion, got %#v", envelope.Metadata)
	}
	if !strings.Contains(envelope.NextAction, "bad-cmd") {
		t.Fatalf("expected failed command in next_action, got %q", envelope.NextAction)
	}
}

func TestGateway_PartialSuccessPathFailedItemsFromViewShape(t *testing.T) {
	gateway := NewGateway(nil)
	envelope, err := gateway.Process(context.Background(), RawToolResult{
		ToolName:   "view",
		ToolCallID: "call-view-failed-items",
		Content:    "===== a.go =====\nok\n\n===== errors =====\nb.go: missing",
		Metadata: map[string]interface{}{
			"batch":           true,
			"request_count":   2,
			"succeeded_count": 1,
			"failed_count":    1,
			"partial_failure": true,
			toolresult.MetadataFailedItemsKey: []interface{}{
				map[string]interface{}{"index": 1, "path": "b.go", "error": "missing"},
			},
		},
	})
	if err != nil || envelope == nil {
		t.Fatalf("process view partial: envelope=%#v err=%v", envelope, err)
	}
	if envelope.Metadata[toolresult.MetadataFailedItemsKey] == nil {
		t.Fatalf("expected failed_items, got %#v", envelope.Metadata)
	}
	if !strings.Contains(fmt.Sprint(envelope.Metadata["next_action"]), "b.go") {
		t.Fatalf("expected path-aware next_action, got %#v", envelope.Metadata)
	}
}

func TestGateway_PartialBatchFromNestedToolMetadata(t *testing.T) {
	// Live path: agent loop wraps toolkit metadata under tool_metadata.
	// Gateway must still promote outcome=partial for mixed view batches.
	gateway := NewGateway(nil)
	envelope, err := gateway.Process(context.Background(), RawToolResult{
		ToolName:   "view",
		ToolCallID: "call-view-nested-partial",
		Content:    "===== a.go =====\nok\n\n===== errors =====\nb.go: missing",
		Metadata: map[string]interface{}{
			"tool_metadata": map[string]interface{}{
				"batch":           true,
				"request_count":   2,
				"succeeded_count": 1,
				"failed_count":    1,
				"partial_failure": true,
				toolresult.MetadataFailedItemsKey: []interface{}{
					map[string]interface{}{"index": 1, "path": "b.go", "error": "missing"},
				},
			},
		},
	})
	if err != nil || envelope == nil {
		t.Fatalf("process nested partial: envelope=%#v err=%v", envelope, err)
	}
	if !envelope.OK {
		t.Fatalf("nested success-path partial should remain ok: %#v", envelope)
	}
	if envelope.Metadata["outcome"] != toolresult.OutcomePartial {
		t.Fatalf("expected outcome=partial from nested tool_metadata, got %#v", envelope.Metadata)
	}
	if envelope.Metadata["requested_count"] != 2 || envelope.Metadata["failed_count"] != 1 || envelope.Metadata["succeeded_count"] != 1 {
		t.Fatalf("expected promoted nested counts, got %#v", envelope.Metadata)
	}
	if envelope.Metadata[toolresult.MetadataPartialFailureKey] != true {
		t.Fatalf("expected partial_failure, got %#v", envelope.Metadata)
	}
	if !strings.Contains(fmt.Sprint(envelope.Metadata["next_action"]), "b.go") &&
		!strings.Contains(envelope.NextAction, "b.go") {
		t.Fatalf("expected path-aware next_action, got envelope=%q meta=%#v", envelope.NextAction, envelope.Metadata)
	}
}

func TestGateway_MutationEmptyBodyIsNotEmptyResult(t *testing.T) {
	gateway := NewGateway(nil)
	envelope, err := gateway.Process(context.Background(), RawToolResult{
		ToolName:   "apply_patch",
		ToolCallID: "call-mutation",
		Content:    "",
		Metadata: map[string]interface{}{
			"tool_metadata": map[string]interface{}{
				"mutated_paths": []string{"changed.go"},
			},
		},
	})
	if err != nil || envelope == nil {
		t.Fatalf("process mutation: envelope=%#v err=%v", envelope, err)
	}
	if envelope.Metadata["empty_result"] == true {
		t.Fatalf("mutation success must not be labeled empty_result: %#v", envelope.Metadata)
	}
	if envelope.Metadata["outcome"] == toolresult.OutcomeEmpty {
		t.Fatalf("mutation success must not be outcome=empty: %#v", envelope.Metadata)
	}
}

func TestGateway_MarksEmptyFromZeroMatchCountWithNoMatchBody(t *testing.T) {
	gateway := NewGateway(nil)
	envelope, err := gateway.Process(context.Background(), RawToolResult{
		ToolName:   "grep",
		ToolCallID: "call-no-match-body",
		Content:    "未找到匹配的内容",
		Metadata: map[string]interface{}{
			"match_count": 0,
			"pattern":     "NoSuchSymbolXYZ",
			"path":        "backend",
		},
		Args: map[string]interface{}{
			"pattern": "NoSuchSymbolXYZ",
			"path":    "backend",
		},
	})
	if err != nil || envelope == nil {
		t.Fatalf("process no-match body: envelope=%#v err=%v", envelope, err)
	}
	if !envelope.OK {
		t.Fatalf("no-match success must remain ok: %#v", envelope)
	}
	if envelope.Metadata["empty_result"] != true {
		t.Fatalf("expected empty_result from match_count=0, got %#v", envelope.Metadata)
	}
	if envelope.Metadata["outcome"] != toolresult.OutcomeEmpty {
		t.Fatalf("expected outcome=empty, got %#v", envelope.Metadata)
	}
	if next, _ := envelope.Metadata["next_action"].(string); !strings.Contains(next, "Empty successful result") {
		t.Fatalf("expected empty-success next_action, got %#v", envelope.Metadata)
	}
	if args, _ := envelope.Metadata[toolresult.MetadataAttemptedArgsKey].(map[string]interface{}); args["pattern"] != "NoSuchSymbolXYZ" {
		t.Fatalf("expected attempted_args on empty no-match, got %#v", envelope.Metadata)
	}
}

func TestGateway_InjectsAttemptedArgsOnEmptyFromArgs(t *testing.T) {
	gateway := NewGateway(nil)
	envelope, err := gateway.Process(context.Background(), RawToolResult{
		ToolName:   "grep",
		ToolCallID: "call-empty-args",
		Content:    "",
		Args: map[string]interface{}{
			"pattern": "NoSuchSymbolXYZ",
			"path":    "backend/internal",
		},
	})
	if err != nil || envelope == nil {
		t.Fatalf("process empty with args: envelope=%#v err=%v", envelope, err)
	}
	if envelope.Metadata["outcome"] != toolresult.OutcomeEmpty {
		t.Fatalf("expected outcome=empty, got %#v", envelope.Metadata)
	}
	args, ok := envelope.Metadata[toolresult.MetadataAttemptedArgsKey].(map[string]interface{})
	if !ok || args["pattern"] != "NoSuchSymbolXYZ" || args["path"] != "backend/internal" {
		t.Fatalf("expected attempted_args from Args, got %#v", envelope.Metadata)
	}
	// Model contract should surface attempted_args.
	rendered := RenderToolResultContentForModel("", "", envelope)
	for _, expected := range []string{
		`"outcome":"empty"`,
		`"attempted_args":`,
		`"pattern":"NoSuchSymbolXYZ"`,
	} {
		if !strings.Contains(rendered, expected) {
			t.Fatalf("expected %q in model contract, got %q", expected, rendered)
		}
	}
	if !strings.Contains(envelope.NextAction, "pattern") || !strings.Contains(envelope.NextAction, "path") {
		t.Fatalf("expected empty next_action to mention attempted arg keys, got %q", envelope.NextAction)
	}
}

func TestGateway_InjectsAttemptedArgsOnPathMissFromArgs(t *testing.T) {
	gateway := NewGateway(nil)
	envelope, err := gateway.Process(context.Background(), RawToolResult{
		ToolName:   "view",
		ToolCallID: "call-path-args",
		Error:      "path not found: missing.go",
		Metadata: map[string]interface{}{
			toolresult.MetadataErrorCodeKey: "TOOL_PATH_NOT_FOUND",
			toolresult.MetadataRetryableKey: false,
		},
		Args: map[string]interface{}{
			"file_path": "missing.go",
		},
	})
	if err != nil || envelope == nil {
		t.Fatalf("process path miss: envelope=%#v err=%v", envelope, err)
	}
	if envelope.OK {
		t.Fatalf("path miss must not be ok: %#v", envelope)
	}
	args, ok := envelope.Metadata[toolresult.MetadataAttemptedArgsKey].(map[string]interface{})
	if !ok || args["file_path"] != "missing.go" {
		t.Fatalf("expected attempted_args file_path, got %#v", envelope.Metadata)
	}
}

func TestGateway_PromotesNestedAttemptedArgsOnPartial(t *testing.T) {
	gateway := NewGateway(nil)
	envelope, err := gateway.Process(context.Background(), RawToolResult{
		ToolName:   "bash",
		ToolCallID: "call-partial-args",
		Content:    "===== command 1/2 [ok] =====\nok\n===== command 2/2 [failed] =====\nbad",
		Error:      "bash command batch completed with 1 failure(s)",
		Metadata: map[string]interface{}{
			"batch":           true,
			"failed_count":    1,
			"requested_count": 2,
			"tool_invocation": map[string]interface{}{
				toolresult.MetadataAttemptedArgsKey: map[string]interface{}{
					"commands": []interface{}{"echo ok", "false"},
				},
			},
		},
	})
	if err != nil || envelope == nil {
		t.Fatalf("process partial: envelope=%#v err=%v", envelope, err)
	}
	if envelope.Metadata["outcome"] != toolresult.OutcomePartial {
		t.Fatalf("expected partial outcome, got %#v", envelope.Metadata)
	}
	args, ok := envelope.Metadata[toolresult.MetadataAttemptedArgsKey].(map[string]interface{})
	if !ok {
		t.Fatalf("expected promoted attempted_args, got %#v", envelope.Metadata)
	}
	if args["commands"] == nil {
		t.Fatalf("expected commands in attempted_args, got %#v", args)
	}
}

func TestGateway_OrdinarySuccessDoesNotInjectAttemptedArgs(t *testing.T) {
	gateway := NewGateway(nil)
	envelope, err := gateway.Process(context.Background(), RawToolResult{
		ToolName:   "view",
		ToolCallID: "call-ok",
		Content:    "package main\n",
		Args: map[string]interface{}{
			"file_path": "main.go",
		},
	})
	if err != nil || envelope == nil {
		t.Fatalf("process success: envelope=%#v err=%v", envelope, err)
	}
	if envelope.Metadata[toolresult.MetadataAttemptedArgsKey] != nil {
		t.Fatalf("ordinary success must not inject attempted_args: %#v", envelope.Metadata)
	}
	rendered := RenderToolResultContentForModel("package main\n", "", envelope)
	if strings.Contains(rendered, "Runtime tool result contract:") {
		t.Fatalf("ordinary success must stay compact, got %q", rendered)
	}
}

func TestGateway_RecordsOutcomeTelemetry(t *testing.T) {
	prev := observability.GlobalMetrics
	observability.GlobalMetrics = observability.NewRegistry()
	t.Cleanup(func() { observability.GlobalMetrics = prev })

	gateway := NewGateway(nil)

	// success
	if _, err := gateway.Process(context.Background(), RawToolResult{
		ToolName: "view",
		Content:  "package main",
	}); err != nil {
		t.Fatalf("success process: %v", err)
	}
	// empty
	if _, err := gateway.Process(context.Background(), RawToolResult{
		ToolName: "grep",
		Content:  "",
	}); err != nil {
		t.Fatalf("empty process: %v", err)
	}
	// failed with invalid args shape
	if _, err := gateway.Process(context.Background(), RawToolResult{
		ToolName: "view",
		Error:    "missing required argument(s): file_path",
		Metadata: map[string]interface{}{
			toolresult.MetadataErrorCodeKey: "TOOL_INVALID_ARGS",
		},
	}); err != nil {
		t.Fatalf("failed process: %v", err)
	}

	if got := observability.GlobalMetrics.GetOrCreateCounter(observability.MetricToolOutcomeTotal, map[string]string{
		observability.LabelOutcome:   observability.ToolOutcomeSuccess,
		observability.LabelErrorCode: "none",
	}).Get(); got != 1 {
		t.Fatalf("success outcome counter=%v", got)
	}
	if got := observability.GlobalMetrics.GetOrCreateCounter(observability.MetricToolOutcomeTotal, map[string]string{
		observability.LabelOutcome:   observability.ToolOutcomeEmpty,
		observability.LabelErrorCode: "none",
	}).Get(); got != 1 {
		t.Fatalf("empty outcome counter=%v", got)
	}
	if got := observability.GlobalMetrics.GetOrCreateCounter(observability.MetricToolOutcomeTotal, map[string]string{
		observability.LabelOutcome:   observability.ToolOutcomeFailed,
		observability.LabelErrorCode: "TOOL_INVALID_ARGS",
	}).Get(); got != 1 {
		t.Fatalf("failed outcome counter=%v", got)
	}
}
