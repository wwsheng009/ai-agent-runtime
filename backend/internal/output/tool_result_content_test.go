package output

import (
	"fmt"
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/toolresult"
)

func TestRenderFullToolResultContent(t *testing.T) {
	testCases := []struct {
		name    string
		content interface{}
		toolErr string
		want    string
	}{
		{
			name:    "raw text stays intact",
			content: "line 1\nline 2\nline 3",
			want:    "line 1\nline 2\nline 3",
		},
		{
			name:    "empty success becomes plain text",
			content: "",
			want:    "Tool returned no output.",
		},
		{
			name:    "error without output becomes failure line",
			toolErr: "exit status 1",
			want:    "Tool execution failed: exit status 1",
		},
		{
			name:    "error keeps full output",
			content: "stderr line 1\nstderr line 2",
			toolErr: "exit status 1",
			want:    "Tool execution failed: exit status 1\nstderr line 1\nstderr line 2",
		},
		{
			name: "structured output becomes json",
			content: map[string]interface{}{
				"success": true,
				"id":      "job-1",
			},
			want: "{\n  \"id\": \"job-1\",\n  \"success\": true\n}",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			got := RenderFullToolResultContent(tc.content, tc.toolErr)
			if got != tc.want {
				t.Fatalf("expected %q, got %q", tc.want, got)
			}
		})
	}
}

func TestRenderToolResultContentForModel_PreservesStructuredEnvelopeSummary(t *testing.T) {
	envelope := &Envelope{
		Summary: "Created team run with 3 teammates and 3 tasks",
		Metadata: map[string]interface{}{
			toolresult.MetadataKey: toolresult.KindStructured,
		},
	}
	got := RenderToolResultContentForModel(map[string]interface{}{
		"team_id": "team-1",
		"task_id": "task-1",
	}, "", envelope)
	if got != "Created team run with 3 teammates and 3 tasks" {
		t.Fatalf("expected envelope summary, got %q", got)
	}
}

func TestRenderToolResultContentForModel_TaskOutputPreservesRawStructuredOutput(t *testing.T) {
	envelope := &Envelope{
		ToolName: "task_output",
		Summary:  "Parsed JSON object with 5 keys.",
		Metadata: map[string]interface{}{
			toolresult.MetadataKey: toolresult.KindStructured,
		},
	}
	content := map[string]interface{}{
		"job_id":      "job_ref_42",
		"next_offset": 128,
		"output":      "line 1\nline 2",
		"status":      "completed",
	}

	got := RenderToolResultContentForModel(content, "", envelope)
	want := RenderFullToolResultContent(content, "")

	if got != want {
		t.Fatalf("expected task_output to preserve raw structured output, got %q", got)
	}
	if got == envelope.Summary {
		t.Fatalf("expected task_output to bypass envelope summary, got %q", got)
	}
	if !strings.Contains(got, `"job_id": "job_ref_42"`) {
		t.Fatalf("expected job_id in raw output, got %q", got)
	}
	if !strings.Contains(got, `"next_offset": 128`) {
		t.Fatalf("expected next_offset in raw output, got %q", got)
	}
	if !strings.Contains(got, `"output": "line 1\nline 2"`) {
		t.Fatalf("expected output payload in raw output, got %q", got)
	}
	if !strings.Contains(got, `"status": "completed"`) {
		t.Fatalf("expected status in raw output, got %q", got)
	}
}

func TestRenderToolResultContentForModel_WaitAgentPreservesCompleteFinalOutput(t *testing.T) {
	finalOutput := strings.Repeat("analysis detail ", 80) + "FINAL_REPORT_END"
	content := map[string]interface{}{
		"agent": map[string]interface{}{
			"session_id": "child-1",
			"status":     "idle",
			"output":     finalOutput,
		},
		"ready_count": 1,
	}
	envelope := &Envelope{
		ToolName: "wait_agent",
		Summary:  "Waited on child agents. Output: " + finalOutput[:200] + "...",
		Metadata: map[string]interface{}{toolresult.MetadataKey: toolresult.KindStructured},
	}

	got := RenderToolResultContentForModel(content, "", envelope)
	if !strings.Contains(got, "FINAL_REPORT_END") {
		t.Fatalf("expected complete child final output, got %q", got)
	}
	if got == envelope.Summary {
		t.Fatal("wait_agent must not replace the final output with its cache-safe preview")
	}
}

func TestRenderToolResultContentForModel_BackgroundTaskPreservesJobContract(t *testing.T) {
	content := map[string]interface{}{
		"job_id": "job_ref_42",
		"status": "pending",
	}
	envelope := &Envelope{
		ToolName: "background_task",
		Summary:  "Background task queued.",
		Metadata: map[string]interface{}{toolresult.MetadataKey: toolresult.KindStructured},
	}

	got := RenderToolResultContentForModel(content, "", envelope)
	if !strings.Contains(got, `"job_id": "job_ref_42"`) || !strings.Contains(got, `"status": "pending"`) {
		t.Fatalf("expected reusable background task contract, got %q", got)
	}
}

func TestRenderToolResultContentForModel_PreservesFullTextWhenExplicitlyMarkedText(t *testing.T) {
	envelope := &Envelope{
		Summary: "line 1",
		Metadata: map[string]interface{}{
			toolresult.MetadataKey: toolresult.KindText,
		},
	}
	got := RenderToolResultContentForModel("line 1\nline 2\nline 3", "", envelope)
	if got != "line 1\nline 2\nline 3" {
		t.Fatalf("expected full text output, got %q", got)
	}
}

func TestRenderToolResultContentForModel_SynthesizesEmptyMutationSuccess(t *testing.T) {
	envelope := &Envelope{
		ToolName: "apply_patch",
		Metadata: map[string]interface{}{
			toolresult.MetadataKey: toolresult.KindText,
			"tool_metadata": map[string]interface{}{
				"mutated_paths": []string{"changed.go"},
			},
		},
	}

	got := RenderToolResultContentForModel("", "", envelope)
	want := "Tool completed successfully; changed 1 file: changed.go."
	if got != want {
		t.Fatalf("expected mutation success fallback %q, got %q", want, got)
	}
}

func TestRenderToolResultContentForModel_TruncatesLargeToolkitTextForHistory(t *testing.T) {
	envelope := &Envelope{
		Metadata: map[string]interface{}{
			toolresult.MetadataKey: toolresult.KindText,
			"mcp_name":             "toolkit",
		},
	}
	var builder strings.Builder
	for i := 0; i < 600; i++ {
		builder.WriteString(fmt.Sprintf("line-%03d-0123456789abcdefghijklmnopqrstuvwxyz\n", i))
	}
	content := builder.String()

	got := RenderToolResultContentForModel(content, "", envelope)

	if got == content {
		t.Fatal("expected large toolkit text to be truncated for model history")
	}
	if !strings.Contains(got, "Total output lines: 600") {
		t.Fatalf("expected total line count header, got %q", got)
	}
	if !strings.Contains(got, "output truncated for history safety") {
		t.Fatalf("expected truncation marker, got %q", got)
	}
	if !strings.Contains(got, "line-000-") {
		t.Fatalf("expected output head to be preserved, got %q", got)
	}
	if !strings.Contains(got, "line-599-") {
		t.Fatalf("expected output tail to be preserved, got %q", got)
	}
	if len(got) <= modelToolTextByteBudget/2 {
		t.Fatalf("expected meaningful head/tail payload, got only %d bytes", len(got))
	}
}

func TestRenderToolResultContentForModel_TruncatesLargeEditingToolOutput(t *testing.T) {
	envelope := &Envelope{
		ToolName: "edit",
		Metadata: map[string]interface{}{
			toolresult.MetadataKey: toolresult.KindText,
			"mcp_name":             "toolkit",
		},
	}
	var builder strings.Builder
	builder.WriteString("成功替换了 1 处匹配项\n\n文件差异:\n```diff\n")
	for i := 0; i < 700; i++ {
		builder.WriteString(fmt.Sprintf("@@ hunk-%03d @@\n-old-%03d\n+new-%03d\n", i, i, i))
	}
	builder.WriteString("```")
	content := builder.String()

	got := RenderToolResultContentForModel(content, "", envelope)

	if got == content {
		t.Fatal("expected large editing output to be truncated for model history")
	}
	if !strings.Contains(got, "@@ hunk-699 @@") || !strings.Contains(got, "+new-699") {
		t.Fatalf("expected tail diff lines to be preserved, got %q", got)
	}
	if !strings.Contains(got, "output truncated for history safety") {
		t.Fatalf("expected editing output truncation marker, got %q", got)
	}
}

func TestRenderToolResultContentForModel_PreservesArtifactNoticeWhenToolkitTextIsTruncated(t *testing.T) {
	envelope := &Envelope{
		Metadata: map[string]interface{}{
			toolresult.MetadataKey:     toolresult.KindText,
			"mcp_name":                 "toolkit",
			"raw_output_artifact_path": `C:\temp\shell-output\toolkit\git_123.txt`,
		},
	}
	content := strings.Repeat("git-diff-line-abcdefghijklmnopqrstuvwxyz0123456789\n", 600)

	got := RenderToolResultContentForModel(content, "", envelope)

	if !strings.Contains(got, "output truncated for history safety") {
		t.Fatalf("expected truncation marker, got %q", got)
	}
	if !strings.Contains(got, `Full raw output artifact: C:\temp\shell-output\toolkit\git_123.txt`) {
		t.Fatalf("expected artifact notice to be preserved, got %q", got)
	}
}

func TestRenderToolResultContentForModel_TruncatesLargeErrorOutputForHistory(t *testing.T) {
	envelope := &Envelope{
		Metadata: map[string]interface{}{
			toolresult.MetadataKey: toolresult.KindText,
		},
	}
	content := strings.Repeat("stderr detail line for failure\n", 700)

	got := RenderToolResultContentForModel(content, "exit status 1", envelope)

	if !strings.Contains(got, "Tool execution failed: exit status 1") {
		t.Fatalf("expected failure prefix to be preserved, got %q", got)
	}
	if !strings.Contains(got, "output truncated for history safety") {
		t.Fatalf("expected truncation marker, got %q", got)
	}
}

func TestRenderToolResultContentForModel_ExposesActionableFailureContract(t *testing.T) {
	envelope := &Envelope{
		ToolName:   "task_output",
		ToolCallID: "call-missing-job",
		Metadata: map[string]interface{}{
			"error_code": "JOB_NOT_FOUND",
		},
	}

	got := RenderToolResultContentForModel(nil, "background job not found: guessed-id", envelope)
	for _, expected := range []string{
		`"ok":false`,
		`"tool_name":"task_output"`,
		`"tool_call_id":"call-missing-job"`,
		`"error_code":"JOB_NOT_FOUND"`,
		`"retryable":false`,
		`"next_action":"Use the exact job_id returned by background_task; do not guess or synthesize an id."`,
		"Tool execution failed: background job not found: guessed-id",
	} {
		if !strings.Contains(got, expected) {
			t.Fatalf("expected %q in model-visible result, got %q", expected, got)
		}
	}
	if strings.Contains(got, `"retryable":true`) {
		t.Fatalf("job-not-found must not request a blind retry: %q", got)
	}
}

func TestRenderToolResultContentForModel_ExposesStaleViewHints(t *testing.T) {
	snippet := "\tfunc Hello() {\n\t\treturn 1\n\t}"
	envelope := &Envelope{
		ToolName:   "edit",
		ToolCallID: "call-stale-edit",
		Metadata: map[string]interface{}{
			"error_code":                 "STALE_CONTEXT",
			"failure_class":              "stale_context",
			"file_path":                  "snippet.go",
			"suggested_view_offset":      0,
			"suggested_view_limit":       40,
			"current_snippet":            snippet,
			"current_snippet_start_line": 1,
			"next_action":                "STALE_CONTEXT: copy current_snippet then retry",
			"retryable":                  false,
		},
	}
	got := RenderToolResultContentForModel(nil, "old_string 未在文件中找到", envelope)
	for _, expected := range []string{
		`"error_code":"STALE_CONTEXT"`,
		`"file_path":"snippet.go"`,
		`"suggested_view_offset":0`,
		`"suggested_view_limit":40`,
		`"current_snippet_start_line":1`,
		`"retryable":false`,
		"current_snippet",
		// Exact file window must appear in the contract JSON (escaped), not only
		// as a next_action mention — models often read contract fields first.
		`\tfunc Hello()`,
		`return 1`,
	} {
		if !strings.Contains(got, expected) {
			t.Fatalf("expected %q in model-visible STALE contract, got %q", expected, got)
		}
	}
}

func TestRenderToolResultContentForModel_PathCandidatesInFailureContract(t *testing.T) {
	envelope := &Envelope{
		ToolName:   "view",
		ToolCallID: "call-path-miss",
		ErrorCode:  "TOOL_PATH_NOT_FOUND",
		Retryable:  false,
		NextAction: "Path not found: missing.go. Nearby candidates: missing_file.go. Correct the path or working directory, then call the tool again. Do not retry the same missing path unchanged.",
		Metadata: map[string]interface{}{
			toolresult.MetadataErrorCodeKey:      "TOOL_PATH_NOT_FOUND",
			toolresult.MetadataRetryableKey:      false,
			toolresult.MetadataNextActionKey:     "Path not found: missing.go. Nearby candidates: missing_file.go. Correct the path or working directory, then call the tool again. Do not retry the same missing path unchanged.",
			toolresult.MetadataPathCandidatesKey: []string{"missing_file.go", "Missing.go"},
			toolresult.MetadataAttemptedArgsKey: map[string]interface{}{
				"file_path": "missing.go",
			},
		},
	}
	got := RenderToolResultContentForModel(nil, "path not found: missing.go (candidates: missing_file.go, Missing.go)", envelope)
	for _, expected := range []string{
		"Runtime tool result contract:",
		`"ok":false`,
		`"error_code":"TOOL_PATH_NOT_FOUND"`,
		`"path_candidates":["missing_file.go","Missing.go"]`,
		`"attempted_args":{"file_path":"missing.go"}`,
		"path not found: missing.go",
	} {
		if !strings.Contains(got, expected) {
			t.Fatalf("expected %q in path-miss contract, got %q", expected, got)
		}
	}
	if strings.Contains(got, `"retryable":true`) {
		t.Fatalf("path miss must not be blindly retryable: %q", got)
	}
}

func TestRenderToolResultContentForModel_EmptySuccessContract(t *testing.T) {
	envelope := &Envelope{
		ToolName:   "grep",
		ToolCallID: "call-empty",
		Metadata: map[string]interface{}{
			toolresult.MetadataKey: toolresult.KindText,
		},
	}
	got := RenderToolResultContentForModel("", "", envelope)
	for _, expected := range []string{
		"Runtime tool result contract:",
		`"ok":true`,
		`"outcome":"empty"`,
		`"empty_result":true`,
		"successful empty result, not a failure",
		"Empty successful result is valid evidence",
	} {
		if !strings.Contains(got, expected) {
			t.Fatalf("expected %q in empty success contract, got %q", expected, got)
		}
	}
}

func TestRenderToolResultContentForModel_EmptySuccessIncludesAttemptedArgs(t *testing.T) {
	envelope := &Envelope{
		ToolName:   "grep",
		ToolCallID: "call-empty-args",
		Metadata: map[string]interface{}{
			toolresult.MetadataKey:            toolresult.KindText,
			toolresult.MetadataEmptyResultKey: true,
			toolresult.MetadataOutcomeKey:     toolresult.OutcomeEmpty,
			toolresult.MetadataAttemptedArgsKey: map[string]interface{}{
				"pattern": "NoSuchSymbolXYZ",
				"path":    "backend/internal",
			},
		},
	}
	got := RenderToolResultContentForModel("", "", envelope)
	for _, expected := range []string{
		`"outcome":"empty"`,
		`"empty_result":true`,
		`"attempted_args":`,
		`"pattern":"NoSuchSymbolXYZ"`,
		`"path":"backend/internal"`,
		"Empty successful result is valid evidence",
	} {
		if !strings.Contains(got, expected) {
			t.Fatalf("expected %q in empty attempted-args contract, got %q", expected, got)
		}
	}
}

func TestRenderToolResultContentForModel_PreservesSourceEmptyWithNoMatchBody(t *testing.T) {
	// Source tools emit short "no matches" text while stamping empty_result.
	// Model contracts must keep outcome=empty (not drop it for non-empty body).
	envelope := &Envelope{
		ToolName:   "grep",
		ToolCallID: "call-no-match-text",
		Metadata: map[string]interface{}{
			toolresult.MetadataKey:            toolresult.KindText,
			toolresult.MetadataEmptyResultKey: true,
			toolresult.MetadataOutcomeKey:     toolresult.OutcomeEmpty,
			"match_count":                     0,
			toolresult.MetadataAttemptedArgsKey: map[string]interface{}{
				"pattern": "NoSuchSymbolXYZ",
			},
		},
	}
	got := RenderToolResultContentForModel("未找到匹配的内容", "", envelope)
	for _, expected := range []string{
		`"outcome":"empty"`,
		`"empty_result":true`,
		"未找到匹配的内容",
		"Empty successful result is valid evidence",
		`"pattern":"NoSuchSymbolXYZ"`,
	} {
		if !strings.Contains(got, expected) {
			t.Fatalf("expected %q in no-match body empty contract, got %q", expected, got)
		}
	}
}

func TestRenderToolResultContentForModel_PartialBatchContract(t *testing.T) {
	envelope := &Envelope{
		ToolName:   "bash",
		ToolCallID: "call-batch",
		Error:      "bash command batch completed with 1 failure(s)",
		ErrorCode:  "TOOL_EXECUTION",
		Retryable:  false,
		NextAction: "Batch finished with 1/3 item failure(s). Reuse successful item outputs; fix or re-run only the failed items with corrected inputs. Do not re-run the entire batch unchanged.",
		Metadata: map[string]interface{}{
			toolresult.MetadataKey:           toolresult.KindText,
			toolresult.MetadataOutcomeKey:    toolresult.OutcomePartial,
			"batch":                          true,
			"failed_count":                   1,
			"requested_count":                3,
			toolresult.MetadataNextActionKey: "Batch finished with 1/3 item failure(s). Reuse successful item outputs; fix or re-run only the failed items with corrected inputs. Do not re-run the entire batch unchanged.",
		},
	}
	got := RenderToolResultContentForModel("===== command 1/3 [ok] =====\nok\n===== command 2/3 [failed] =====\nbad", "bash command batch completed with 1 failure(s)", envelope)
	for _, expected := range []string{
		"Runtime tool result contract:",
		`"ok":false`,
		`"outcome":"partial"`,
		"Reuse successful item outputs",
		"command 1/3 [ok]",
	} {
		if !strings.Contains(got, expected) {
			t.Fatalf("expected %q in partial batch contract, got %q", expected, got)
		}
	}
}

func TestRenderToolResultContentForModel_PartialSuccessBatchCountsInContract(t *testing.T) {
	envelope := &Envelope{
		ToolName:   "view",
		ToolCallID: "call-view-partial",
		Metadata: map[string]interface{}{
			toolresult.MetadataKey: toolresult.KindText,
			"batch":                true,
			"request_count":        3,
			"succeeded_count":      2,
			"failed_count":         1,
			"partial_failure":      true,
		},
	}
	got := RenderToolResultContentForModel("===== a.go =====\nok\n\n===== errors =====\nb.go: missing", "", envelope)
	for _, expected := range []string{
		"Runtime tool result contract:",
		`"ok":true`,
		`"outcome":"partial"`,
		`"requested_count":3`,
		`"failed_count":1`,
		`"succeeded_count":2`,
		`"partial_failure":true`,
		"Reuse successful item outputs",
		"===== a.go =====",
	} {
		if !strings.Contains(got, expected) {
			t.Fatalf("expected %q in partial success contract, got %q", expected, got)
		}
	}
}

func TestRenderToolResultContentForModel_PartialContractIncludesFailedItems(t *testing.T) {
	envelope := &Envelope{
		ToolName:   "bash",
		ToolCallID: "call-batch-failed-items",
		Error:      "bash command batch completed with 1 failure(s)",
		Metadata: map[string]interface{}{
			toolresult.MetadataKey:        toolresult.KindText,
			toolresult.MetadataOutcomeKey: toolresult.OutcomePartial,
			"batch":                       true,
			"failed_count":                1,
			"requested_count":             2,
			"items": []interface{}{
				map[string]interface{}{"index": 0, "command": "echo ok", "success": true},
				map[string]interface{}{"index": 1, "command": "bad-cmd", "success": false, "error": "exit status 1"},
			},
		},
	}
	got := RenderToolResultContentForModel("===== command 1/2 [ok] =====\nok", "batch completed with 1 failure(s)", envelope)
	for _, expected := range []string{
		"Runtime tool result contract:",
		`"outcome":"partial"`,
		`"failed_items"`,
		"bad-cmd",
	} {
		if !strings.Contains(got, expected) {
			t.Fatalf("expected %q in failed_items contract, got %q", expected, got)
		}
	}
}

func TestRenderToolResultContentForModel_PreservesArtifactNoticeWithFailureContract(t *testing.T) {
	envelope := &Envelope{
		ToolName:   "bash",
		ToolCallID: "call-artifact-fail",
		ErrorCode:  "TOOL_EXECUTION",
		Retryable:  false,
		NextAction: "Inspect the error details, correct the cause, and retry only when the operation is safe.",
		Metadata: map[string]interface{}{
			toolresult.MetadataKey:     toolresult.KindText,
			"mcp_name":                 "toolkit",
			"raw_output_artifact_path": `C:\temp\shell-output\toolkit\fail_123.txt`,
		},
	}
	content := strings.Repeat("stderr failure detail line for truncation budget\n", 500)
	got := RenderToolResultContentForModel(content, "exit status 1", envelope)
	if !strings.Contains(got, "Runtime tool result contract:") {
		t.Fatalf("expected failure contract, got %q", got)
	}
	if !strings.Contains(got, `Full raw output artifact: C:\temp\shell-output\toolkit\fail_123.txt`) {
		t.Fatalf("expected artifact notice preserved under contract, got %q", got)
	}
}

func TestRenderToolResultContentForModel_PreservesArtifactNoticeWithPartialContract(t *testing.T) {
	envelope := &Envelope{
		ToolName:   "bash",
		ToolCallID: "call-artifact-partial",
		Error:      "bash command batch completed with 1 failure(s)",
		ErrorCode:  "TOOL_EXECUTION",
		NextAction: "Batch finished with 1/2 item failure(s). Reuse successful item outputs; fix or re-run only the failed items with corrected inputs. Do not re-run the entire batch unchanged.",
		Metadata: map[string]interface{}{
			toolresult.MetadataKey:           toolresult.KindText,
			toolresult.MetadataOutcomeKey:    toolresult.OutcomePartial,
			"batch":                          true,
			"failed_count":                   1,
			"requested_count":                2,
			"raw_output_artifact_path":       `C:\temp\shell-output\toolkit\batch_partial.txt`,
			toolresult.MetadataNextActionKey: "Batch finished with 1/2 item failure(s). Reuse successful item outputs; fix or re-run only the failed items with corrected inputs. Do not re-run the entire batch unchanged.",
		},
	}
	content := strings.Repeat("batch partial detail line for truncation budget\n", 500)
	got := RenderToolResultContentForModel(content, "bash command batch completed with 1 failure(s)", envelope)
	if !strings.Contains(got, `"outcome":"partial"`) {
		t.Fatalf("expected partial contract, got %q", got)
	}
	if !strings.Contains(got, `Full raw output artifact: C:\temp\shell-output\toolkit\batch_partial.txt`) {
		t.Fatalf("expected artifact notice preserved under partial contract, got %q", got)
	}
}

func TestRenderToolResultContentForModel_OrdinarySuccessOmitsContract(t *testing.T) {
	envelope := &Envelope{
		ToolName: "view",
		Metadata: map[string]interface{}{
			toolresult.MetadataKey: toolresult.KindText,
		},
	}
	got := RenderToolResultContentForModel("package main\n", "", envelope)
	if strings.Contains(got, "Runtime tool result contract:") {
		t.Fatalf("ordinary success must stay compact, got %q", got)
	}
	if got != "package main" {
		t.Fatalf("expected raw body, got %q", got)
	}
}

func TestRenderToolResultContentForModel_PrefersLargeTestSummaryAndArtifact(t *testing.T) {
	envelope := &Envelope{
		ToolName: "bash",
		Summary:  "Parsed go test output: failed.\nFailed tests: TestRecovery",
		Error:    "exit status 1",
		Metadata: map[string]interface{}{
			toolresult.MetadataKey:     toolresult.KindText,
			"model_summary_preferred":  true,
			"raw_output_artifact_path": `C:\temp\local-shell\go-test.txt`,
		},
	}
	raw := strings.Repeat("noisy test log\n", 2000)

	got := RenderToolResultContentForModel(raw, "exit status 1", envelope)

	if strings.Contains(got, "noisy test log") {
		t.Fatalf("expected reduced test summary instead of raw log, got %q", got)
	}
	if !strings.Contains(got, "Failed tests: TestRecovery") || !strings.Contains(got, "Tool execution failed: exit status 1") {
		t.Fatalf("expected actionable failure summary, got %q", got)
	}
	if !strings.Contains(got, `Full raw output artifact: C:\temp\local-shell\go-test.txt`) {
		t.Fatalf("expected raw artifact reference, got %q", got)
	}
}

func TestRenderToolResultContentForModel_ExternalMCPPreservesFullStructuredOutput(t *testing.T) {
	envelope := &Envelope{
		Summary: "reduced summary only",
		Metadata: map[string]interface{}{
			"mcp_name": "remote-filesystem",
		},
	}
	got := RenderToolResultContentForModel(map[string]interface{}{
		"files": []string{"a.txt", "b.txt", "c.txt"},
		"count": 3,
	}, "", envelope)
	want := "{\n  \"count\": 3,\n  \"files\": [\n    \"a.txt\",\n    \"b.txt\",\n    \"c.txt\"\n  ]\n}"
	if got != want {
		t.Fatalf("expected full structured output, got %q", got)
	}
}

func TestRenderToolResultContentForModel_ToolkitMCPPreservesStructuredSummary(t *testing.T) {
	envelope := &Envelope{
		Summary: "reduced toolkit summary",
		Metadata: map[string]interface{}{
			toolresult.MetadataKey: toolresult.KindStructured,
			"mcp_name":             "toolkit",
		},
	}
	got := RenderToolResultContentForModel(map[string]interface{}{
		"count": 3,
		"files": []string{"a.txt", "b.txt", "c.txt"},
	}, "", envelope)
	if got != "reduced toolkit summary" {
		t.Fatalf("expected reduced toolkit summary, got %q", got)
	}
}

func TestRenderToolResultContentForModel_ExternalMCPTruncatesLargeTextOutput(t *testing.T) {
	envelope := &Envelope{
		Metadata: map[string]interface{}{
			toolresult.MetadataKey: toolresult.KindText,
			"mcp_name":             "remote-filesystem",
		},
	}
	content := strings.Repeat("external-mcp-line\n", 900)

	got := RenderToolResultContentForModel(content, "", envelope)

	if got == strings.TrimSpace(content) {
		t.Fatalf("expected external MCP text to be bounded for history, got %q", got)
	}
	if !strings.Contains(got, "output truncated for history safety") {
		t.Fatalf("expected external MCP content truncation marker, got %q", got)
	}
}

func TestRenderToolResultContentForModel_AppendsArtifactNoticeForSmallText(t *testing.T) {
	envelope := &Envelope{
		Metadata: map[string]interface{}{
			toolresult.MetadataKey:     toolresult.KindText,
			"mcp_name":                 "toolkit",
			"raw_output_artifact_path": `C:\temp\shell-output\toolkit\git_456.txt`,
		},
	}

	got := RenderToolResultContentForModel("short output", "", envelope)

	want := "short output\n\nFull raw output artifact: C:\\temp\\shell-output\\toolkit\\git_456.txt"
	if got != want {
		t.Fatalf("expected artifact notice for small text, got %q", got)
	}
}
