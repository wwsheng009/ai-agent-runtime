package output

import (
	"fmt"
	"regexp"
	"strconv"
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
	if !strings.Contains(got, "Created team run with 3 teammates and 3 tasks") {
		t.Fatalf("expected envelope summary, got %q", got)
	}
	// B4: structured envelope summaries carry a compact schema/size signal too.
	if !strings.Contains(got, "Structured output summary: kind=structured fields=2") {
		t.Fatalf("expected structured schema summary, got %q", got)
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
	if !strings.Contains(got, "Tool result lines: 600") {
		t.Fatalf("expected total line count header, got %q", got)
	}
	if !strings.Contains(got, "output truncated for history safety") {
		t.Fatalf("expected truncation marker, got %q", got)
	}
	if !strings.Contains(got, "line-000-") {
		t.Fatalf("expected output head to be preserved, got %q", got)
	}
	// Head-only fold: the tail is intentionally dropped (a hole in the middle
	// of the text cannot be paged), and the notice must say how much was
	// omitted plus what to do next.
	if strings.Contains(got, "line-599-") {
		t.Fatalf("head-only fold must not keep the tail, got %q", got)
	}
	if !strings.Contains(got, "showing the first ") || !strings.Contains(got, "of 600 lines") {
		t.Fatalf("expected first-N-of-M line notice, got %q", got)
	}
	if !strings.Contains(got, "next step:") {
		t.Fatalf("expected next-step guidance in the fold notice, got %q", got)
	}
	if len(got) > modelToolTextByteBudget {
		t.Fatalf("folded payload must fit the %d-byte budget, got %d bytes", modelToolTextByteBudget, len(got))
	}
	if len(got) <= modelToolTextByteBudget/2 {
		t.Fatalf("expected meaningful head payload, got only %d bytes", len(got))
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
	// Head-only fold keeps the beginning of the diff and drops the tail.
	if !strings.Contains(got, "@@ hunk-000 @@") || !strings.Contains(got, "+new-000") {
		t.Fatalf("expected head diff lines to be preserved, got %q", got)
	}
	if strings.Contains(got, "@@ hunk-699 @@") {
		t.Fatalf("head-only fold must not keep the tail diff, got %q", got)
	}
	if !strings.Contains(got, "output truncated for history safety") {
		t.Fatalf("expected editing output truncation marker, got %q", got)
	}
	if !strings.Contains(got, "next step:") {
		t.Fatalf("expected next-step guidance, got %q", got)
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

func TestRenderToolResultContentForModel_ExposesReadOnlyPolicyContract(t *testing.T) {
	envelope := &Envelope{
		ToolName:   "shell",
		ToolCallID: "call-read-only",
		Metadata: map[string]interface{}{
			toolresult.MetadataPolicyKey:         "read_only",
			toolresult.MetadataPolicySource:      "spawn_subagents.read_only",
			toolresult.MetadataOverridable:       false,
			toolresult.MetadataErrorCodeKey:      "AGENT_READ_ONLY",
			toolresult.MetadataRetryableKey:      false,
			toolresult.MetadataNextActionKey:     "Submit each read-only command as a separate shell.commands entry.",
			toolresult.MetadataToolCallIDKey:     "call-read-only",
			toolresult.MetadataToolNameKey:       "shell",
			toolresult.MetadataEmptyResultKey:    false,
			toolresult.MetadataPartialFailureKey: false,
			toolresult.MetadataOutcomeKey:        toolresult.OutcomeFailed,
			toolresult.MetadataAttemptedArgsKey:  map[string]interface{}{"command": "git status; git diff"},
		},
	}

	got := RenderToolResultContentForModel(
		nil,
		"read-only policy blocks compound shell command; submit one command per shell.commands entry: git status; git diff",
		envelope,
	)
	for _, expected := range []string{
		`"ok":false`,
		`"tool_name":"shell"`,
		`"tool_call_id":"call-read-only"`,
		`"error_code":"AGENT_READ_ONLY"`,
		`"retryable":false`,
		`"policy":"read_only"`,
		`"policy_source":"spawn_subagents.read_only"`,
		`"overridable":false`,
		`"command":"git status; git diff"`,
		`"next_action":"Submit each read-only command as a separate shell.commands entry."`,
	} {
		if !strings.Contains(got, expected) {
			t.Fatalf("expected %q in model-visible result, got %q", expected, got)
		}
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
	if !strings.Contains(got, "reduced toolkit summary") {
		t.Fatalf("expected reduced toolkit summary, got %q", got)
	}
	if !strings.Contains(got, "Structured output summary: kind=structured") {
		t.Fatalf("expected structured schema summary, got %q", got)
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

	want := "short output\n\nFull raw output artifact: C:\\temp\\shell-output\\toolkit\\git_456.txt kind=text"
	if got != want {
		t.Fatalf("expected artifact notice for small text, got %q", got)
	}
}

func TestModelArtifactNotice_NewFormatCarriesTailAndConsumerHint(t *testing.T) {
	envelope := &Envelope{
		ArtifactIDs: []string{"art_ef182934f39842729c57899a488a7ea3"},
		Metadata: map[string]interface{}{
			"artifact_id":          "art_ef182934f39842729c57899a488a7ea3",
			"raw_bytes":            12345,
			toolresult.MetadataKey: toolresult.KindText,
		},
	}
	notice := modelArtifactNotice(envelope)
	if !strings.HasPrefix(notice, "Full raw output artifact_id: art_ef182934f39842729c57899a488a7ea3") {
		t.Fatalf("expected id prefix, got %q", notice)
	}
	// A2: machine-parseable tail (size + kind) on the same line.
	if !strings.Contains(notice, "size=12345") {
		t.Fatalf("expected size= tail, got %q", notice)
	}
	if !strings.Contains(notice, "kind=text") {
		t.Fatalf("expected kind= tail, got %q", notice)
	}
	// A1: consumer hint names the registered reader tool and its id argument,
	// plus the never-pass-to-task_output warning.
	if !strings.Contains(notice, "artifact_read(artifact_id=") {
		t.Fatalf("expected consumer hint, got %q", notice)
	}
	if !strings.Contains(notice, "never pass this id to task_output") {
		t.Fatalf("expected task_output warning, got %q", notice)
	}
	// Single line so splitTrailingArtifactNotice can peel it.
	if strings.Contains(notice, "\n") {
		t.Fatalf("expected single-line notice, got %q", notice)
	}
}

func TestModelArtifactNotice_FallsBackToPathWithoutConsumerHint(t *testing.T) {
	envelope := &Envelope{
		Metadata: map[string]interface{}{
			"raw_output_artifact_path": `C:\temp\shell-output\toolkit\git_1.txt`,
			"raw_bytes":                2048,
		},
	}
	notice := modelArtifactNotice(envelope)
	if !strings.HasPrefix(notice, "Full raw output artifact: C:\\temp\\shell-output\\toolkit\\git_1.txt") {
		t.Fatalf("expected path prefix, got %q", notice)
	}
	if !strings.Contains(notice, "size=2048") {
		t.Fatalf("expected size= tail for path notice, got %q", notice)
	}
	if !strings.Contains(notice, "kind=raw_output") {
		t.Fatalf("expected default kind tail, got %q", notice)
	}
	if strings.Contains(notice, "task_output") {
		t.Fatalf("path notice must not carry the id-only task_output warning, got %q", notice)
	}
}

func TestFormatTruncatedToolTextForModel_ExtractsFirstErrorLine(t *testing.T) {
	content := strings.Repeat("noisy success line\n", 200) +
		"2026-03-14 10:00:02 ERROR failed to fetch artifact\n" +
		strings.Repeat("noisy tail line\n", 200)
	got := formatTruncatedToolTextForModel(content, 2*1024)
	if !strings.Contains(got, "First error line:") {
		t.Fatalf("expected First error line in truncated summary, got %q", got)
	}
	if !strings.Contains(got, "failed to fetch artifact") {
		t.Fatalf("expected failure line content in summary, got %q", got)
	}
	// The earliest failure line wins, not the tail.
	if !strings.Contains(got, "ERROR failed to fetch artifact") {
		t.Fatalf("expected earliest error line, got %q", got)
	}

	// No failure markers -> no extra header line, keep existing shape.
	clean := strings.Repeat("clean line\n", 300)
	cleanGot := formatTruncatedToolTextForModel(clean, 2*1024)
	if strings.Contains(cleanGot, "First error line:") {
		t.Fatalf("did not expect First error line without failure markers, got %q", cleanGot)
	}
	if !strings.Contains(cleanGot, "Tool result lines: 300") {
		t.Fatalf("expected total line header, got %q", cleanGot)
	}
}

// TestRenderToolTextForModelHistory_FoldNoticeCarriesNextStepGuidance pins the
// head-only fold contract: the notice must state how many lines were shown and
// omitted and how to read the rest with a narrower call, so a folded result is
// never a dead end. The notice deliberately does not promise artifact_read
// paging — that read is itself budget-folded in practice — while the separate
// artifact pointer notice stays untouched.
func TestRenderToolTextForModelHistory_FoldNoticeCarriesNextStepGuidance(t *testing.T) {
	const id = "art_ab12cd34ef56ab12cd34ef56ab12cd34"
	envelope := &Envelope{
		ToolCallID:  "call-cont-hint",
		ArtifactIDs: []string{id},
		Metadata: map[string]interface{}{
			"artifact_id":          id,
			"raw_bytes":            60000,
			toolresult.MetadataKey: toolresult.KindText,
			toolresult.SourceKey:   toolresult.SourceMCP,
		},
	}
	content := strings.Repeat("fold notice line\n", modelToolTextByteBudget/8)
	got := renderToolTextForModelHistory(content, "", envelope)
	if !strings.Contains(got, "showing the first ") || !strings.Contains(got, "next step:") {
		t.Fatalf("expected first-N-lines notice with next-step guidance, got tail %q", got[len(got)-500:])
	}
	start := strings.Index(got, "[output truncated for history safety")
	if start < 0 {
		t.Fatalf("expected fold notice, got tail %q", got[len(got)-500:])
	}
	foldNotice := got[start:]
	if end := strings.Index(foldNotice, "]\n\n"); end >= 0 {
		foldNotice = foldNotice[:end+1]
	}
	if strings.Contains(foldNotice, "artifact_read") {
		t.Fatalf("fold notice must not instruct artifact_read paging, got %q", foldNotice)
	}

	// Small untruncated successful results must NOT grow the notice.
	small := strings.Repeat("y", 100)
	envelope2 := &Envelope{
		ToolCallID:  "call-cont-hint-small",
		ArtifactIDs: []string{id},
		Metadata: map[string]interface{}{
			"artifact_id":          id,
			"raw_bytes":            100,
			toolresult.MetadataKey: toolresult.KindText,
			toolresult.SourceKey:   toolresult.SourceMCP,
		},
	}
	got2 := renderToolTextForModelHistory(small, "", envelope2)
	if strings.Contains(got2, "output truncated for history safety") {
		t.Fatalf("untruncated result must not carry the fold notice, got %q", got2)
	}
}

func TestRenderToolResultContentForModel_PreservesNoticeAcrossTruncationPaths(t *testing.T) {
	const id = "art_ef182934f39842729c57899a488a7ea3"
	envelope := &Envelope{
		ToolCallID:  "call-artifact-trunc",
		ArtifactIDs: []string{id},
		Metadata: map[string]interface{}{
			"artifact_id":          id,
			"raw_bytes":            60000,
			toolresult.MetadataKey: toolresult.KindText,
		},
	}
	body := strings.Repeat("truncation test detail line for budget checks\n", 1200)
	got := RenderToolResultContentForModel(body, "", envelope)
	if !strings.Contains(got, "Full raw output artifact_id: "+id) {
		t.Fatalf("expected artifact notice preserved, got %q", got)
	}
	if !strings.Contains(got, "output truncated for history safety") {
		t.Fatalf("expected truncation marker, got %q", got)
	}
	// size tail survives truncation too.
	if !strings.Contains(got, "size=60000") {
		t.Fatalf("expected size tail in preserved notice, got %q", got)
	}

	// Exact-budget boundary: notice still wins over body.
	smallEnvelope := &Envelope{
		ArtifactIDs: []string{id},
		Metadata: map[string]interface{}{
			"artifact_id":          id,
			toolresult.MetadataKey: toolresult.KindText,
		},
	}
	smallBody := strings.Repeat("x", modelToolTextByteBudget+len(id)+64)
	gotSmall := RenderToolResultContentForModel(smallBody, "", smallEnvelope)
	if !strings.Contains(gotSmall, "Full raw output artifact_id: "+id) {
		t.Fatalf("expected artifact notice preserved at budget boundary, got %q", gotSmall)
	}
}

// TestFormatTruncatedToolTextForModel_NeverExceedsBudget pins the hard ceiling:
// header + shown head + fold notice must stay within the requested budget for
// every budget size, including ones too small to afford the notice at all.
func TestFormatTruncatedToolTextForModel_NeverExceedsBudget(t *testing.T) {
	// Single-line content has no line-boundary snapping slack, so the head fills
	// the body budget down to the byte; that makes any notice-reserve shortfall
	// show up immediately as an over-budget render.
	singleLine := strings.Repeat("x", 2*modelToolTextByteBudget)
	multiLine := "2026-01-01 ERROR failed to process\n" +
		strings.Repeat("0123456789abcdefghijklmnopqrstuvwxyz\n", 400)

	budgets := []int{1, 64, 256, 512, 1024, 4096, 12 * 1024, 64 * 1024}
	for _, content := range []string{singleLine, multiLine} {
		for _, budget := range budgets {
			got := formatTruncatedToolTextForModel(content, budget)
			if len(got) > budget {
				t.Fatalf("budget %d: rendered %d bytes, exceeds budget by %d",
					budget, len(got), len(got)-budget)
			}
		}
	}
}

// TestFormatTruncatedToolTextForModel_HeadOnlyNoticeReportsShownAndOmittedLines
// pins the replacement for the middle-fold algorithm: the rendered text keeps
// only the first lines, and the notice states exactly how many lines are shown
// and omitted plus how to continue, so nothing is hidden mid-text.
func TestFormatTruncatedToolTextForModel_HeadOnlyNoticeReportsShownAndOmittedLines(t *testing.T) {
	var builder strings.Builder
	for i := 0; i < 2000; i++ {
		fmt.Fprintf(&builder, "payload line %04d\n", i)
	}
	content := builder.String()

	got := formatTruncatedToolTextForModel(content, 4*1024)
	if strings.Contains(got, "from the middle") {
		t.Fatalf("middle fold must be gone, got %q", got)
	}
	if !strings.Contains(got, "payload line 0000") {
		t.Fatalf("expected head lines to be shown, got %q", got)
	}
	if strings.Contains(got, "payload line 1999") {
		t.Fatalf("head-only fold must not keep the tail, got %q", got)
	}

	match := regexp.MustCompile(
		`showing the first (\d+) of 2000 lines; omitted (\d+) lines \((\d+) bytes\) from the end`,
	).FindStringSubmatch(got)
	if match == nil {
		t.Fatalf("expected first-N-of-M notice, got %q", got)
	}
	shown, _ := strconv.Atoi(match[1])
	omittedLines, _ := strconv.Atoi(match[2])
	omittedBytes, _ := strconv.Atoi(match[3])
	if shown <= 0 || shown >= 2000 {
		t.Fatalf("shown lines = %d, want a strict prefix of 2000", shown)
	}
	if shown+omittedLines != 2000 {
		t.Fatalf("shown %d + omitted %d != 2000 lines", shown, omittedLines)
	}
	if rendered := strings.Count(got, "payload line "); rendered != shown {
		t.Fatalf("notice claims %d shown lines but rendered %d", shown, rendered)
	}
	if omittedBytes <= 0 || omittedBytes >= len(content) {
		t.Fatalf("omitted bytes = %d, want 0 < omitted < %d", omittedBytes, len(content))
	}
	if !strings.Contains(got, "next step:") || !strings.Contains(got, "narrower window") {
		t.Fatalf("expected next-step guidance, got %q", got)
	}
	if len(got) > 4*1024 {
		t.Fatalf("rendered %d bytes, exceeds the 4096-byte budget", len(got))
	}
	noticeStart := strings.Index(got, "[output truncated for history safety")
	noticeEnd := strings.Index(got[noticeStart:], "]\n\n")
	t.Logf("head-only fold: rendered=%d bytes (budget 4096); shown=%d omittedLines=%d omittedBytes=%d; notice=%q",
		len(got), shown, omittedLines, omittedBytes, got[noticeStart:noticeStart+noticeEnd+1])
}

// TestFormatTruncatedToolTextForModel_PartialLineNoticeReportsByteCut pins the
// mid-line cut case: when the first line alone overflows the body budget the
// head can only be a byte prefix, and the notice must describe that cut in
// bytes. A line-count notice would contradict itself here ("0 lines omitted"
// next to thousands of dropped bytes) and its line-offset recovery cannot
// resume a mid-line cut.
func TestFormatTruncatedToolTextForModel_PartialLineNoticeReportsByteCut(t *testing.T) {
	var builder strings.Builder
	for i := 0; i < 8; i++ {
		fmt.Fprintf(&builder, "header line %02d\n", i)
	}
	builder.WriteString(strings.Repeat("abcdefghij", 4000)) // 40000 bytes on a single line
	content := builder.String()

	got := formatTruncatedToolTextForModel(content, 4*1024)
	if strings.Contains(got, "of 9 lines") {
		t.Fatalf("mid-line cut must not be reported with a line-count notice, got %q", got)
	}

	match := regexp.MustCompile(
		`showing (\d+) complete lines plus the first (\d+) bytes of line (\d+); omitted (\d+) bytes from the end`,
	).FindStringSubmatch(got)
	if match == nil {
		t.Fatalf("expected partial-line notice, got %q", got)
	}
	completeLines, _ := strconv.Atoi(match[1])
	partialBytes, _ := strconv.Atoi(match[2])
	cutLine, _ := strconv.Atoi(match[3])
	omittedBytes, _ := strconv.Atoi(match[4])
	if completeLines != 8 {
		t.Fatalf("complete lines = %d, want the 8 header lines", completeLines)
	}
	if cutLine != 9 {
		t.Fatalf("cut line = %d, want line 9 (the single long line)", cutLine)
	}
	if partialBytes <= 0 || partialBytes >= 40000 {
		t.Fatalf("partial bytes = %d, want a strict prefix of the 40000-byte line", partialBytes)
	}

	// Cross-check both notices against the bytes actually rendered.
	noticeStart := strings.Index(got, "[output truncated for history safety")
	headStart := strings.Index(got, "header line 00")
	if noticeStart < 0 || headStart < 0 || headStart > noticeStart {
		t.Fatalf("unexpected notice/head layout, got %q", got)
	}
	head := strings.TrimSuffix(got[headStart:noticeStart], "\n\n")
	if lines := strings.Count(head, "\n"); lines != completeLines {
		t.Fatalf("notice claims %d complete lines but rendered %d", completeLines, lines)
	}
	if idx := strings.LastIndex(head, "\n"); len(head)-idx-1 != partialBytes {
		t.Fatalf("notice claims %d partial bytes but rendered %d", partialBytes, len(head)-idx-1)
	}
	if len(head)+omittedBytes != len(content) {
		t.Fatalf("shown head %d + omitted %d != %d bytes", len(head), omittedBytes, len(content))
	}
	if !strings.Contains(got, "byte range") || !strings.Contains(got, "artifact_read") {
		t.Fatalf("expected byte-range recovery guidance, got %q", got)
	}
	if len(got) > 4*1024 {
		t.Fatalf("rendered %d bytes, exceeds the 4096-byte budget", len(got))
	}
	t.Logf("partial-line fold: rendered=%d bytes (budget 4096); completeLines=%d partialBytes=%d omittedBytes=%d",
		len(got), completeLines, partialBytes, omittedBytes)
}

// TestRenderToolTextForModelHistory_HonorsDeclaredModelVisibleBudget pins the
// tool-owned window contract: a tool that declares
// toolresult.MetadataModelVisibleBudgetKey keeps that window in history instead
// of being folded to the render-layer backstop, while the same body without a
// declaration is folded as before.
func TestRenderToolTextForModelHistory_HonorsDeclaredModelVisibleBudget(t *testing.T) {
	body := strings.Repeat("declared budget line for shell output\n", 600)
	if len(body) <= modelToolTextByteBudget {
		t.Fatalf("precondition: body must exceed the layer backstop, got %d bytes", len(body))
	}

	declared := &Envelope{
		ToolCallID: "call-declared-budget",
		Metadata: map[string]interface{}{
			toolresult.MetadataKey:                   toolresult.KindText,
			toolresult.MetadataModelVisibleBudgetKey: 32 * 1024,
		},
	}
	got := renderToolTextForModelHistory(body, "", declared)
	// Every tool result is whitespace-normalized before rendering (TrimSpace),
	// so "intact" means the whole body minus that pre-existing trim: a fold
	// would drop thousands of bytes and add the fold notice.
	if got != strings.TrimSpace(body) {
		t.Fatalf("declared window must keep the body intact, got %d of %d bytes", len(got), len(body))
	}
	if strings.Contains(got, "output truncated for history safety") {
		t.Fatalf("declared window must not be folded, got %q", got)
	}

	// The declaration must also move the fold point itself: the same tool's
	// oversized body folds at the declared 32 KiB window instead of the layer
	// backstop that the undeclared body below still falls back to.
	oversized := strings.Repeat("declared budget line for shell output\n", 1200)
	gotOversized := renderToolTextForModelHistory(oversized, "", declared)
	if !strings.Contains(gotOversized, "output truncated for history safety") {
		t.Fatalf("oversized body must still be folded, got %d bytes", len(gotOversized))
	}
	if len(gotOversized) > 32*1024 {
		t.Fatalf("declared window must bound the folded payload, got %d bytes", len(gotOversized))
	}
	if len(gotOversized) < 24*1024 {
		t.Fatalf("declared window must not collapse to the layer backstop, got %d bytes", len(gotOversized))
	}

	undeclared := &Envelope{
		ToolCallID: "call-undeclared-budget",
		Metadata: map[string]interface{}{
			toolresult.MetadataKey: toolresult.KindText,
		},
	}
	gotUndeclared := renderToolTextForModelHistory(body, "", undeclared)
	if !strings.Contains(gotUndeclared, "output truncated for history safety") {
		t.Fatalf("expected the undeclared body to be folded, got %d bytes", len(gotUndeclared))
	}
	if len(gotUndeclared) > modelToolTextByteBudget {
		t.Fatalf("folded payload must fit the %d-byte backstop, got %d bytes", modelToolTextByteBudget, len(gotUndeclared))
	}
}

// TestEffectiveModelToolTextBudget_ClampsDeclaredWindow pins the clamp around a
// declared window: a bogus tiny value cannot starve the model window and a huge
// one cannot blow up history.
func TestEffectiveModelToolTextBudget_ClampsDeclaredWindow(t *testing.T) {
	if got := effectiveModelToolTextBudget(nil); got != modelToolTextByteBudget {
		t.Fatalf("no declaration must use the layer budget, got %d", got)
	}
	if got := effectiveModelToolTextBudget(map[string]interface{}{
		toolresult.MetadataModelVisibleBudgetKey: 0,
	}); got != modelToolTextByteBudget {
		t.Fatalf("zero declaration must use the layer budget, got %d", got)
	}
	if got := effectiveModelToolTextBudget(map[string]interface{}{
		toolresult.MetadataModelVisibleBudgetKey: 1,
	}); got != modelToolTextByteBudget {
		t.Fatalf("tiny declaration must fall back to the layer budget, got %d", got)
	}
	if got := effectiveModelToolTextBudget(map[string]interface{}{
		toolresult.MetadataModelVisibleBudgetKey: 32 * 1024,
	}); got != 32*1024 {
		t.Fatalf("32 KiB declaration must be honored, got %d", got)
	}
	if got := effectiveModelToolTextBudget(map[string]interface{}{
		toolresult.MetadataModelVisibleBudgetKey: 1024 * 1024,
	}); got != modelToolTextBudgetCeilingBytes {
		t.Fatalf("huge declaration must clamp to the ceiling %d, got %d", modelToolTextBudgetCeilingBytes, got)
	}
	if got := effectiveModelToolTextBudget(map[string]interface{}{
		"tool_metadata": map[string]interface{}{
			toolresult.MetadataModelVisibleBudgetKey: 32 * 1024,
		},
	}); got != 32*1024 {
		t.Fatalf("nested declaration must be honored, got %d", got)
	}
}

// TestRenderToolTextForModelHistory_DropsPointerForIntactOwnedWindow pins the
// pointer gate for tools that own their window: an intact body carries no
// record-id pointer (nothing was omitted, so nothing can be dereferenced),
// while a folded body, a failed call and an artifact_read window keep it.
func TestRenderToolTextForModelHistory_DropsPointerForIntactOwnedWindow(t *testing.T) {
	const id = "art_9f8e7d6c5b4a39281706f5e4d3c2b1a0"
	newEnvelope := func(meta map[string]interface{}) *Envelope {
		merged := map[string]interface{}{
			"artifact_id":          id,
			"raw_bytes":            4096,
			toolresult.MetadataKey: toolresult.KindText,
		}
		for key, value := range meta {
			merged[key] = value
		}
		return &Envelope{ToolCallID: "call-pointer-gate", ArtifactIDs: []string{id}, Metadata: merged}
	}
	body := strings.Repeat("intact owned window body\n", 40)
	pointer := "Full raw output artifact_id: " + id

	intact := renderToolTextForModelHistory(body, "", newEnvelope(map[string]interface{}{
		toolresult.MetadataSkipRenderTruncationKey: true,
		"is_truncated": false,
	}))
	if strings.Contains(intact, pointer) {
		t.Fatalf("intact owned window must not carry a pointer, got %q", intact)
	}

	folded := renderToolTextForModelHistory(body, "", newEnvelope(map[string]interface{}{
		toolresult.MetadataSkipRenderTruncationKey: true,
		"is_truncated": true,
	}))
	if !strings.Contains(folded, pointer) {
		t.Fatalf("folded owned window must keep the pointer, got %q", folded)
	}

	failedWithErr := renderToolTextForModelHistory(body, "boom", newEnvelope(map[string]interface{}{
		toolresult.MetadataSkipRenderTruncationKey: true,
		"is_truncated": false,
	}))
	if !strings.Contains(failedWithErr, pointer) {
		t.Fatalf("failed call must keep the pointer as recovery hint, got %q", failedWithErr)
	}

	window := renderToolTextForModelHistory(body, "", newEnvelope(map[string]interface{}{
		toolresult.MetadataSkipRenderTruncationKey: true,
		"is_truncated":       false,
		"artifact_source_id": id,
	}))
	if !strings.Contains(window, pointer) {
		t.Fatalf("artifact_read window must keep the pointer, got %q", window)
	}
}

// TestRenderToolTextForModelHistory_FoldsCaptureTruncatedOutput pins the fix
// for the capture-flag leak: output_truncated only reports that the raw stream
// hit the retention limit, so the render layer must still fold and size the
// payload instead of forwarding the whole capture to history.
func TestRenderToolTextForModelHistory_FoldsCaptureTruncatedOutput(t *testing.T) {
	body := strings.Repeat("capture truncated shell output line\n", 700)
	if len(body) <= modelToolTextByteBudget {
		t.Fatalf("precondition: body must exceed the layer backstop, got %d bytes", len(body))
	}
	envelope := &Envelope{
		ToolCallID: "call-capture-truncated",
		Metadata: map[string]interface{}{
			toolresult.MetadataKey:  toolresult.KindText,
			"output_truncated":      true,
			"capture_limit_reached": true,
		},
	}
	got := renderToolTextForModelHistory(body, "", envelope)
	if !strings.Contains(got, "output truncated for history safety") {
		t.Fatalf("capture-truncated output must still be folded for history, got %d bytes", len(got))
	}
	if len(got) > modelToolTextByteBudget {
		t.Fatalf("folded payload must fit the %d-byte backstop, got %d bytes", modelToolTextByteBudget, len(got))
	}
}
