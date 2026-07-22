package output

import (
	"context"
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/artifact"
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
