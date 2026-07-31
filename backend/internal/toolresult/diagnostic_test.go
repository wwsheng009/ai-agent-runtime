package toolresult

import (
	"fmt"
	"strings"
	"testing"

	runtimeerrors "github.com/wwsheng009/ai-agent-runtime/internal/errors"
)

func TestDiagnoseFailureUsesStructuredMetadataAndAction(t *testing.T) {
	diagnostic := Diagnose("task_output", "call-1", "background job failed", map[string]interface{}{
		"tool_metadata": map[string]interface{}{
			"error_code": string(runtimeerrors.ErrJobNotFound),
		},
	})
	if diagnostic.OK {
		t.Fatal("expected failed tool invocation")
	}
	if diagnostic.ErrorCode != string(runtimeerrors.ErrJobNotFound) {
		t.Fatalf("expected job-not-found code, got %#v", diagnostic)
	}
	if diagnostic.Retryable {
		t.Fatal("job-not-found must not be blindly retried")
	}
	if !strings.HasPrefix(diagnostic.NextAction, "Use the") {
		t.Fatalf("expected precise id recovery action, got %q", diagnostic.NextAction)
	}
}

func TestDiagnoseAttachesStaleViewHints(t *testing.T) {
	snippet := "\tfunc Hello() {\n\t\treturn 1\n\t}\n"
	diagnostic := Diagnose("edit", "call-stale-hints", "old_string 未在文件中找到", map[string]interface{}{
		"error_code":                 string(runtimeerrors.ErrToolStaleContext),
		"failure_class":              "stale_context",
		"file_path":                  "snippet.go",
		"suggested_view_offset":      12,
		"suggested_view_limit":       40,
		"current_snippet":            snippet,
		"current_snippet_start_line": 13,
		"next_action":                "STALE_CONTEXT: copy current_snippet then retry",
	})
	if diagnostic.ErrorCode != string(runtimeerrors.ErrToolStaleContext) {
		t.Fatalf("error_code=%q want STALE_CONTEXT", diagnostic.ErrorCode)
	}
	if diagnostic.FilePath != "snippet.go" {
		t.Fatalf("file_path=%q", diagnostic.FilePath)
	}
	if diagnostic.SuggestedViewOffset == nil || *diagnostic.SuggestedViewOffset != 12 {
		t.Fatalf("suggested_view_offset=%v", diagnostic.SuggestedViewOffset)
	}
	if diagnostic.SuggestedViewLimit == nil || *diagnostic.SuggestedViewLimit != 40 {
		t.Fatalf("suggested_view_limit=%v", diagnostic.SuggestedViewLimit)
	}
	if diagnostic.CurrentSnippetStartLine == nil || *diagnostic.CurrentSnippetStartLine != 13 {
		t.Fatalf("current_snippet_start_line=%v", diagnostic.CurrentSnippetStartLine)
	}
	// Must preserve leading tab indent — TrimSpace would break copy-paste rebuild.
	if diagnostic.CurrentSnippet != snippet {
		t.Fatalf("current_snippet=%q want exact %q", diagnostic.CurrentSnippet, snippet)
	}
}

func TestDiagnoseCapsOversizedCurrentSnippet(t *testing.T) {
	// Oversized multi-line snippet must stay under contract budget and drop the
	// tail on whole-line boundaries (no mid-line cut when multiple lines fit).
	var lines []string
	// ~80 chars/line * 120 lines >> 4KiB budget.
	pad := strings.Repeat("x", 64)
	for i := 0; i < 120; i++ {
		lines = append(lines, fmt.Sprintf("\tline_%03d = %s", i, pad))
	}
	huge := strings.Join(lines, "\n")
	if len(huge) <= maxCurrentSnippetContractBytes {
		t.Fatalf("test fixture too small: len=%d budget=%d", len(huge), maxCurrentSnippetContractBytes)
	}
	diagnostic := Diagnose("edit", "call-big-snip", "old_string 未在文件中找到", map[string]interface{}{
		"error_code":      string(runtimeerrors.ErrToolStaleContext),
		"current_snippet": huge,
	})
	if diagnostic.CurrentSnippet == "" {
		t.Fatal("expected capped current_snippet")
	}
	if len(diagnostic.CurrentSnippet) > maxCurrentSnippetContractBytes {
		t.Fatalf("capped snippet len=%d over budget %d", len(diagnostic.CurrentSnippet), maxCurrentSnippetContractBytes)
	}
	if strings.Contains(diagnostic.CurrentSnippet, "line_119") {
		t.Fatalf("expected oversized tail dropped, got tail present in %q", diagnostic.CurrentSnippet[len(diagnostic.CurrentSnippet)-40:])
	}
	if !strings.Contains(diagnostic.CurrentSnippet, "line_000") {
		t.Fatalf("expected head lines retained, got %q", diagnostic.CurrentSnippet[:80])
	}
	// Whole-line cap: every retained line should still be complete.
	for _, line := range strings.Split(diagnostic.CurrentSnippet, "\n") {
		if line == "" {
			continue
		}
		if !strings.HasPrefix(line, "\tline_") || !strings.HasSuffix(line, pad) {
			t.Fatalf("expected complete padded line, got %q", line)
		}
	}
}

func TestDiagnosePrefersNestedAuthoredNextActionForStaleContext(t *testing.T) {
	// Historical / pre-promotion payloads may only carry recovery fields under
	// tool_metadata. Diagnose must not fall back to generic STALE_CONTEXT text
	// when the tool already authored a specific next_action.
	authored := "STALE_CONTEXT: re-view offset 12 first; do not replay the same old_string."
	diagnostic := Diagnose("edit", "call-stale", "old_string 未在文件中找到", map[string]interface{}{
		"tool_metadata": map[string]interface{}{
			"error_code":            string(runtimeerrors.ErrToolStaleContext),
			"retryable":             false,
			"failure_class":         "stale_context",
			"suggested_view_offset": 12,
			"next_action":           authored,
		},
	})
	if diagnostic.OK {
		t.Fatal("expected failed diagnostic")
	}
	if diagnostic.ErrorCode != string(runtimeerrors.ErrToolStaleContext) {
		t.Fatalf("error_code=%q want STALE_CONTEXT", diagnostic.ErrorCode)
	}
	if diagnostic.Retryable {
		t.Fatalf("STALE_CONTEXT must not be retryable: %#v", diagnostic)
	}
	if diagnostic.NextAction != authored {
		t.Fatalf("expected nested authored next_action, got %q", diagnostic.NextAction)
	}
	if diagnostic.SuggestedViewOffset == nil || *diagnostic.SuggestedViewOffset != 12 {
		t.Fatalf("expected nested suggested_view_offset=12, got %v", diagnostic.SuggestedViewOffset)
	}
}

func TestDiagnoseRefinesGenericToolExecutionToStaleContextFromMessage(t *testing.T) {
	// Live chat-logs from pre-promotion / older binaries often stamp
	// error_code=TOOL_EXECUTION + generic next_action while the body is clearly
	// an edit old_string miss. Diagnose must refine to STALE_CONTEXT so
	// tool.completed / offline efficiency stop counting bare TOOL_EXECUTION.
	diagnostic := Diagnose(
		"edit",
		"call-edit-miss",
		"old_string 未在文件中找到；edit 只执行精确匹配（包括空格、缩进和换行），不会自动模糊定位。",
		map[string]interface{}{
			"error_code":  string(runtimeerrors.ErrToolExecution),
			"retryable":   true,
			"next_action": DefaultToolExecutionNextAction,
		},
	)
	if diagnostic.OK {
		t.Fatal("expected failed diagnostic")
	}
	if diagnostic.ErrorCode != string(runtimeerrors.ErrToolStaleContext) {
		t.Fatalf("error_code=%q want STALE_CONTEXT; diagnostic=%#v", diagnostic.ErrorCode, diagnostic)
	}
	if diagnostic.Retryable {
		t.Fatalf("refined STALE_CONTEXT must not be retryable: %#v", diagnostic)
	}
	if !strings.Contains(diagnostic.NextAction, "stale") && !strings.Contains(strings.ToLower(diagnostic.NextAction), "re-read") &&
		!strings.Contains(diagnostic.NextAction, "view") {
		t.Fatalf("expected stale recovery next_action, got %q", diagnostic.NextAction)
	}
}

func TestDiagnoseRefinesGenericToolExecutionToStaleContextFromFailureClass(t *testing.T) {
	diagnostic := Diagnose("apply_patch", "call-hunk", "patch apply failed", map[string]interface{}{
		"error_code":    string(runtimeerrors.ErrToolExecution),
		"failure_class": "stale_context",
		"next_action":   DefaultToolExecutionNextAction,
	})
	if diagnostic.ErrorCode != string(runtimeerrors.ErrToolStaleContext) {
		t.Fatalf("error_code=%q want STALE_CONTEXT from failure_class; %#v", diagnostic.ErrorCode, diagnostic)
	}
	if diagnostic.Retryable {
		t.Fatalf("STALE_CONTEXT must not be retryable")
	}
}

func TestDiagnoseDoesNotDemoteSpecificStructuredCode(t *testing.T) {
	// Specific tool-authored codes win even if the message looks like something else.
	// Non-edit tools (bash) must keep TOOL_TIMEOUT even if body mentions old_string.
	diagnostic := Diagnose("bash", "call-timeout", "old_string 未在文件中找到", map[string]interface{}{
		"error_code": string(runtimeerrors.ErrToolTimeout),
		"retryable":  true,
	})
	if diagnostic.ErrorCode != string(runtimeerrors.ErrToolTimeout) {
		t.Fatalf("specific TOOL_TIMEOUT must not be demoted, got %q", diagnostic.ErrorCode)
	}
	if !diagnostic.Retryable {
		t.Fatalf("authored retryable=true for timeout must stick")
	}
}

func TestDiagnoseRefinesMislabeledTimeoutOnEditStaleBody(t *testing.T) {
	// Live residual: edit old_string miss body embeds map keys like TOOL_TIMEOUT /
	// "timeout", and some export paths stamped error_code=TOOL_TIMEOUT + timeout next_action.
	// Models then follow timeout recovery instead of STALE current_snippet rebuild.
	body := "old_string 未在文件中找到；edit 只执行精确匹配。 最接近片段: \"TOOL_TIMEOUT\": \"timeout\",\n    \"TOOL_PATH_NOT_FOUND\": \"path_missing\""
	diagnostic := Diagnose("edit", "call-edit-timeout-mislabeled", body, map[string]interface{}{
		"error_code":  string(runtimeerrors.ErrToolTimeout),
		"retryable":   true,
		"next_action": "Code search timed out. Prefer toolkit grep with a narrower path.",
	})
	if diagnostic.ErrorCode != string(runtimeerrors.ErrToolStaleContext) {
		t.Fatalf("error_code=%q want STALE_CONTEXT; diagnostic=%#v", diagnostic.ErrorCode, diagnostic)
	}
	if diagnostic.Retryable {
		t.Fatalf("refined STALE_CONTEXT must not be retryable: %#v", diagnostic)
	}
	if strings.Contains(strings.ToLower(diagnostic.NextAction), "timed out") ||
		strings.Contains(strings.ToLower(diagnostic.NextAction), "timeout") {
		t.Fatalf("timeout next_action must not stick after STALE refine, got %q", diagnostic.NextAction)
	}
	if !strings.Contains(diagnostic.NextAction, "STALE_CONTEXT") &&
		!strings.Contains(strings.ToLower(diagnostic.NextAction), "stale") &&
		!strings.Contains(strings.ToLower(diagnostic.NextAction), "old_string") &&
		!strings.Contains(strings.ToLower(diagnostic.NextAction), "view") {
		t.Fatalf("expected STALE recovery next_action, got %q", diagnostic.NextAction)
	}
}

func TestDiagnoseRefinesMislabeledPathOnEditStaleBody(t *testing.T) {
	body := "old_string 未在文件中找到；已尝试 CRLF/LF。 最接近片段: \"path_missing\" / no such file"
	diagnostic := Diagnose("edit", "call-edit-path-mislabeled", body, map[string]interface{}{
		"error_code": string(runtimeerrors.ErrToolPathNotFound),
		"retryable":  false,
	})
	if diagnostic.ErrorCode != string(runtimeerrors.ErrToolStaleContext) {
		t.Fatalf("error_code=%q want STALE_CONTEXT; %#v", diagnostic.ErrorCode, diagnostic)
	}
}

func TestClassifyToolErrorCodePrefersStaleOverTimeoutInSnippet(t *testing.T) {
	// Without structured code, message-only classification must still prefer STALE
	// when the body is an old_string miss even if closest snippet mentions timeout.
	msg := "old_string 未在文件中找到；最接近片段: ERROR_CODE_TO_CATEGORY timeout TOOL_TIMEOUT"
	code := classifyToolErrorCode(msg)
	if code != string(runtimeerrors.ErrToolStaleContext) {
		t.Fatalf("classify=%q want STALE_CONTEXT", code)
	}
}

func TestDiagnoseSuccessDoesNotPromoteUnderlyingJobError(t *testing.T) {
	diagnostic := Diagnose("task_output", "call-2", "", map[string]interface{}{
		"error_code": string(runtimeerrors.ErrToolTimeout),
	})
	if !diagnostic.OK || diagnostic.ErrorCode != "" || diagnostic.Retryable {
		t.Fatalf("expected successful query contract, got %#v", diagnostic)
	}
}

func TestDiagnoseTimeoutIsRetryableWithSideEffectWarning(t *testing.T) {
	diagnostic := Diagnose("bash", "call-3", "[TOOL_TIMEOUT] command timed out", nil)
	if diagnostic.ErrorCode != string(runtimeerrors.ErrToolTimeout) || !diagnostic.Retryable {
		t.Fatalf("expected retryable timeout, got %#v", diagnostic)
	}
	if diagnostic.NextAction == "" {
		t.Fatal("expected timeout next action")
	}
}

func TestApplyDiagnosticMetadata(t *testing.T) {
	metadata := map[string]interface{}{}
	diagnostic := Diagnose("view", "call-4", "permission denied", nil)
	ApplyDiagnosticMetadata(metadata, diagnostic)
	if metadata[MetadataOKKey] != false {
		t.Fatalf("expected ok=false, got %#v", metadata)
	}
	if metadata[MetadataErrorCodeKey] != string(runtimeerrors.ErrAgentPermission) {
		t.Fatalf("expected permission code, got %#v", metadata)
	}
	if metadata[MetadataRetryableKey] != false || metadata[MetadataNextActionKey] == "" {
		t.Fatalf("expected non-retryable action metadata, got %#v", metadata)
	}
}

func TestApplyDiagnosticMetadataPromotesStaleRecoveryFields(t *testing.T) {
	snippet := "\tfunc Hello() {\n\t\treturn 1\n\t}"
	diagnostic := Diagnose("edit", "call-stale-promote", "old_string 未在文件中找到", map[string]interface{}{
		"error_code":                 string(runtimeerrors.ErrToolStaleContext),
		"failure_class":              "stale_context",
		"file_path":                  "snippet.go",
		"suggested_view_offset":      12,
		"suggested_view_limit":       40,
		"current_snippet":            snippet,
		"current_snippet_start_line": 13,
		"next_action":                "STALE_CONTEXT: copy current_snippet then retry",
	})
	// Simulate a stripped export that only kept disposition codes.
	metadata := map[string]interface{}{
		"error_code": string(runtimeerrors.ErrToolStaleContext),
	}
	ApplyDiagnosticMetadata(metadata, diagnostic)
	if got, _ := metadata["current_snippet"].(string); got != snippet {
		t.Fatalf("current_snippet=%q want exact %q", got, snippet)
	}
	if got, _ := metadata["file_path"].(string); got != "snippet.go" {
		t.Fatalf("file_path=%q", got)
	}
	if offset, ok := metadata["suggested_view_offset"].(int); !ok || offset != 12 {
		t.Fatalf("suggested_view_offset=%#v", metadata["suggested_view_offset"])
	}
	if start, ok := metadata["current_snippet_start_line"].(int); !ok || start != 13 {
		t.Fatalf("current_snippet_start_line=%#v", metadata["current_snippet_start_line"])
	}
}

func TestDiagnoseParsesClosestSnippetFromErrorBody(t *testing.T) {
	// Live residual: pre-promotion chat-logs only embed closest lines in error text.
	body := "old_string 未在文件中找到；edit 只执行精确匹配。\n" +
		"建议从第 30 行附近用 view 重读（suggested_view_offset=29）。\n" +
		"最接近的当前内容（第 30 行附近，可直接据此重建 old_string）:\n" +
		"    30|\tcase toolresult.OutcomeFailed:\n" +
		"    31|\t\tif code == string(errors.ErrToolStaleContext) {\n" +
		"next_action: 优先用上方“最接近的当前内容”\n" +
		"old_string 预览: \"stale\""
	diagnostic := Diagnose("edit", "call-parse-snip", body, map[string]interface{}{
		"error_code": string(runtimeerrors.ErrToolExecution),
	})
	if diagnostic.ErrorCode != string(runtimeerrors.ErrToolStaleContext) {
		t.Fatalf("error_code=%q want STALE_CONTEXT", diagnostic.ErrorCode)
	}
	if !strings.Contains(diagnostic.CurrentSnippet, "case toolresult.OutcomeFailed") {
		t.Fatalf("expected parsed current_snippet, got %q", diagnostic.CurrentSnippet)
	}
	if !strings.Contains(diagnostic.CurrentSnippet, "\tcase") {
		t.Fatalf("expected leading tab preserved in snippet, got %q", diagnostic.CurrentSnippet)
	}
	if diagnostic.SuggestedViewOffset == nil || *diagnostic.SuggestedViewOffset != 29 {
		t.Fatalf("suggested_view_offset=%v want 29", diagnostic.SuggestedViewOffset)
	}
	if diagnostic.CurrentSnippetStartLine == nil || *diagnostic.CurrentSnippetStartLine != 30 {
		t.Fatalf("current_snippet_start_line=%v want 30", diagnostic.CurrentSnippetStartLine)
	}

	// Also promote onto metadata for chat-log export.
	meta := map[string]interface{}{}
	ApplyDiagnosticMetadata(meta, diagnostic)
	if snip, _ := meta["current_snippet"].(string); !strings.Contains(snip, "OutcomeFailed") {
		t.Fatalf("promoted current_snippet missing: %#v", meta["current_snippet"])
	}
}

func TestParseClosestSnippetFromQuotedFragment(t *testing.T) {
	body := `old_string 未在文件中找到。 最接近片段: "\tcase toolresult.OutcomeFailed:\n\t\tif code == \"STALE_CONTEXT\" {" next_action: view`
	snip, start := parseClosestSnippetFromErrorMessage(body)
	if !strings.Contains(snip, "case toolresult.OutcomeFailed") {
		t.Fatalf("snippet=%q", snip)
	}
	if !strings.HasPrefix(snip, "\tcase") {
		t.Fatalf("expected tab indent, got %q", snip)
	}
	if start != 0 {
		t.Fatalf("quoted form has no line number, start=%d", start)
	}
}

func TestParseClosestSnippetFromTruncatedQuotedFragment(t *testing.T) {
	// Historical binaries mid-line-truncated %q closest (no closing quote).
	// Offline rehydrate / Diagnose must still recover a usable partial snippet.
	body := "old_string 未在文件中找到；edit 只执行精确匹配（包括空格、缩进和换行），不会自动模糊定位。 " +
		`最接近片段: "\tEventRewindStarted           = \"rewind_started\"\n\tEventRewindFinished          = \"rewind_fin`
	snip, start := parseClosestSnippetFromErrorMessage(body)
	if !strings.Contains(snip, "EventRewindStarted") {
		t.Fatalf("expected partial snippet recovery, got %q", snip)
	}
	if !strings.Contains(snip, "EventRewindFinished") {
		t.Fatalf("expected second line of truncated quote, got %q", snip)
	}
	if start != 0 {
		t.Fatalf("quoted form has no line number, start=%d", start)
	}
}

func TestParseClosestSnippetRejectsProseMentionOfClosestMarker(t *testing.T) {
	// Live residual: apply_patch bodies often only have next_action prose that
	// quotes “最接近的当前内容”, then dump 期望内容 (model-intended old lines).
	// That must not rehydrate as current_snippet — it is not file current text.
	body := "更新文件 foo.go 失败: 无法定位 hunk: @@；未找到期望旧内容。" +
		"next_action: 先用 view/grep 重读目标文件附近最新内容，按返回的“最接近的当前内容”重建补丁" +
		"（一次只改一个文件/区域）；不要原样重试同一 stale @@/旧行。\n" +
		"期望内容:\n" +
		"func (c *Controller) selectCandidates(\n" +
		"\tctx context.Context,\n"
	snip, start := parseClosestSnippetFromErrorMessage(body)
	if snip != "" || start != 0 {
		t.Fatalf("expected empty snippet for prose-only mention, got snip=%q start=%d", snip, start)
	}
}

func TestDiagnoseClassifiesCommonRecoveryModes(t *testing.T) {
	testCases := []struct {
		name      string
		message   string
		code      runtimeerrors.ErrorCode
		retryable bool
	}{
		{name: "json arguments", message: "json: cannot unmarshal number into Go value", code: runtimeerrors.ErrToolInvalidArgs},
		{name: "windows path", message: "The system cannot find the path specified", code: runtimeerrors.ErrToolPathNotFound},
		{name: "network", message: "dial tcp: connection refused", code: runtimeerrors.ErrNetworkUnavailable, retryable: true},
		{name: "rate limit", message: "HTTP 429 rate limit exceeded", code: runtimeerrors.ErrAPIRateLimit, retryable: true},
		{name: "quota auth", message: "HTTP 403 insufficient user quota", code: runtimeerrors.ErrAPIUnauthorized},
		{name: "server", message: "HTTP 503 service unavailable", code: runtimeerrors.ErrAPIServerError, retryable: true},
		{name: "shell missing command", message: "head : The term 'head' is not recognized as a name of a cmdlet", code: runtimeerrors.ErrToolShellCompat},
		{name: "shell exit 127", message: "exit status 127", code: runtimeerrors.ErrToolShellCompat},
		{name: "stale patch hunk", message: "无法定位 hunk: @@；未找到期望旧内容", code: runtimeerrors.ErrToolStaleContext},
		{name: "stale edit old_string", message: "old_string 未在文件中找到；edit 只执行精确匹配", code: runtimeerrors.ErrToolStaleContext},
		{name: "spawn depth", message: "[SPAWN_DEPTH_LIMIT] agent spawn depth limit reached: max_depth=1 requested_depth=2", code: runtimeerrors.ErrAgentSpawnDepthLimit},
		{name: "agent already exists", message: "session already exists: child-1", code: runtimeerrors.ErrAgentAlreadyExists},
		{name: "agent busy", message: "session is busy (running)", code: runtimeerrors.ErrAgentBusy},
		{name: "agent alias missing", message: "unknown agent session reference: session_ref_missing", code: runtimeerrors.ErrAgentSessionNotFound},
		{name: "sqlite interrupted", message: "sqlite3: interrupted", code: runtimeerrors.ErrStreamInterrupted, retryable: true},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			diagnostic := Diagnose("test_tool", "call-test", tc.message, nil)
			if diagnostic.ErrorCode != string(tc.code) || diagnostic.Retryable != tc.retryable {
				t.Fatalf("unexpected diagnostic for %q: %#v", tc.message, diagnostic)
			}
			if diagnostic.NextAction == "" {
				t.Fatalf("expected next action for %q", tc.message)
			}
		})
	}
}

func TestDiagnoseAgentLifecycleConflictNextActions(t *testing.T) {
	testCases := []struct {
		name           string
		message        string
		code           runtimeerrors.ErrorCode
		wantNextSubstr string
	}{
		{name: "already exists", message: "session already exists: child-1", code: runtimeerrors.ErrAgentAlreadyExists, wantNextSubstr: "Do not retry the same spawn_agent unchanged"},
		{name: "busy", message: "session is busy (running)", code: runtimeerrors.ErrAgentBusy, wantNextSubstr: "Do not retry the same send_input unchanged"},
		{name: "session not found", message: "[AGENT_SESSION_NOT_FOUND] agent session reference not found: session_ref_missing", code: runtimeerrors.ErrAgentSessionNotFound, wantNextSubstr: "Use list_agents"},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			diagnostic := Diagnose("test_tool", "call-lifecycle", tc.message, nil)
			if diagnostic.ErrorCode != string(tc.code) {
				t.Fatalf("error_code=%q want %q; diagnostic=%#v", diagnostic.ErrorCode, tc.code, diagnostic)
			}
			if diagnostic.Retryable {
				t.Fatalf("lifecycle conflict must not be retryable: %#v", diagnostic)
			}
			if !strings.Contains(diagnostic.NextAction, tc.wantNextSubstr) {
				t.Fatalf("next_action %q missing %q", diagnostic.NextAction, tc.wantNextSubstr)
			}
		})
	}
}

func TestDiagnoseChineseToolkitInvalidArgsNextAction(t *testing.T) {
	testCases := []struct {
		name           string
		tool           string
		message        string
		wantNextSubstr string
	}{
		{
			name:           "pattern missing",
			tool:           "grep",
			message:        "pattern 参数缺失或无效",
			wantNextSubstr: "Provide every required argument",
		},
		{
			name:           "file_path missing",
			tool:           "edit",
			message:        "file_path 参数缺失或无效",
			wantNextSubstr: "Provide every required argument",
		},
		{
			name:           "content missing empty",
			tool:           "write",
			message:        "content 参数缺失或为空",
			wantNextSubstr: "Provide every required argument",
		},
		{
			name:           "command not string",
			tool:           "shell",
			message:        "command 参数缺失或不是字符串",
			wantNextSubstr: "Provide every required argument",
		},
		{
			name:           "type invalid",
			tool:           "write",
			message:        "参数类型错误: content",
			wantNextSubstr: "argument value types",
		},
		{
			name:           "apply_patch illegal hunk",
			tool:           "apply_patch",
			message:        "第 41 行不是合法的 hunk 内容: *** Begin Patch\",",
			wantNextSubstr: "argument",
		},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			diagnostic := Diagnose(tc.tool, "call-zh-args", tc.message, nil)
			if diagnostic.ErrorCode != string(runtimeerrors.ErrToolInvalidArgs) {
				t.Fatalf("error_code=%q want TOOL_INVALID_ARGS; diagnostic=%#v", diagnostic.ErrorCode, diagnostic)
			}
			if !strings.Contains(diagnostic.NextAction, tc.wantNextSubstr) {
				t.Fatalf("next_action %q missing %q", diagnostic.NextAction, tc.wantNextSubstr)
			}
			if strings.Contains(diagnostic.NextAction, "Inspect the error details") {
				t.Fatalf("must not fall back to generic execution next_action: %q", diagnostic.NextAction)
			}
			if diagnostic.Retryable {
				t.Fatalf("invalid args must not be blindly retryable: %#v", diagnostic)
			}
		})
	}
}

func TestDiagnoseShellFailureNextActions(t *testing.T) {
	testCases := []struct {
		name           string
		message        string
		wantCode       runtimeerrors.ErrorCode
		wantNextSubstr string
		wantNotSubstr  string
	}{
		{
			name:           "bare exit status 1",
			message:        "exit status 1",
			wantCode:       runtimeerrors.ErrToolExecution,
			wantNextSubstr: "Do not replay the identical command blindly",
		},
		{
			name:           "windows head not recognized",
			message:        "命令执行失败: exit status 1\n提示: Windows PowerShell/pwsh 默认没有 `head`；请改用 `Select-Object -First 200`\n\n当前环境信息:\nShell: pwsh",
			wantCode:       runtimeerrors.ErrToolShellCompat,
			wantNextSubstr: "Select-Object -First N",
		},
		{
			name:           "command not found",
			message:        "bash: rg: command not found",
			wantCode:       runtimeerrors.ErrToolShellCompat,
			wantNextSubstr: "Do not retry the same command unchanged",
		},
		{
			name:           "batch all failed",
			message:        "bash command batch completed with 1 failure(s)。失败摘要: #1 go test ./... => exit status 1",
			wantCode:       runtimeerrors.ErrToolExecution,
			wantNextSubstr: "fix only the failed commands",
		},
		{
			name:           "no-match exit guidance",
			message:        "exit status 1。命令: rg -n Foo。提示: rg 退出码 1 通常表示未匹配到结果（不是命令崩溃）",
			wantCode:       runtimeerrors.ErrToolExecution,
			wantNextSubstr: "no-match / empty evidence",
		},
		{
			name:           "syntax parsererror",
			message:        "ParserError: Unexpected token '|' in expression or statement",
			wantCode:       runtimeerrors.ErrToolExecution,
			wantNextSubstr: "Shell syntax or quoting failed",
		},
		{
			name:           "enriched exit with output summary",
			message:        "exit status 1。命令: go test ./...。输出摘要: --- FAIL: TestFoo",
			wantCode:       runtimeerrors.ErrToolExecution,
			wantNextSubstr: "Do not retry unchanged",
			wantNotSubstr:  "identical command blindly",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			diagnostic := Diagnose("bash", "call-shell", tc.message, nil)
			if diagnostic.OK {
				t.Fatalf("expected failure diagnostic, got %#v", diagnostic)
			}
			if diagnostic.ErrorCode != string(tc.wantCode) {
				t.Fatalf("error_code=%q want %q; diagnostic=%#v", diagnostic.ErrorCode, tc.wantCode, diagnostic)
			}
			if !strings.Contains(diagnostic.NextAction, tc.wantNextSubstr) {
				t.Fatalf("next_action %q missing %q", diagnostic.NextAction, tc.wantNextSubstr)
			}
			if tc.wantNotSubstr != "" && strings.Contains(diagnostic.NextAction, tc.wantNotSubstr) {
				t.Fatalf("next_action %q should not contain %q", diagnostic.NextAction, tc.wantNotSubstr)
			}
			if diagnostic.Retryable {
				t.Fatalf("shell recovery failures must not be blindly retryable: %#v", diagnostic)
			}
		})
	}
}

func TestDiagnoseShellBatchPartialStillWinsOverGenericShellAction(t *testing.T) {
	// Mixed batch success/failure should keep partial guidance even when the
	// error text looks like a shell batch failure.
	diagnostic := Diagnose("bash", "call-batch-shell", "bash command batch completed with 1 failure(s)", map[string]interface{}{
		"batch":           true,
		"failed_count":    1,
		"requested_count": 3,
		"succeeded_count": 2,
	})
	if diagnostic.Outcome != OutcomePartial {
		t.Fatalf("expected partial outcome, got %#v", diagnostic)
	}
	if !strings.Contains(diagnostic.NextAction, "Reuse successful") {
		t.Fatalf("expected partial next_action to win, got %q", diagnostic.NextAction)
	}
}

func TestApplyDiagnosticMetadataShellCompat(t *testing.T) {
	metadata := map[string]interface{}{}
	diagnostic := Diagnose("bash", "call-head", "The term 'head' is not recognized as a name of a cmdlet", nil)
	ApplyDiagnosticMetadata(metadata, diagnostic)
	if metadata[MetadataErrorCodeKey] != string(runtimeerrors.ErrToolShellCompat) {
		t.Fatalf("expected TOOL_SHELL_COMPAT metadata, got %#v", metadata)
	}
	next, _ := metadata[MetadataNextActionKey].(string)
	if !strings.Contains(next, "Select-Object") && !strings.Contains(next, "shell-native") {
		t.Fatalf("expected shell-compat next_action metadata, got %q", next)
	}
	if metadata[MetadataRetryableKey] != false {
		t.Fatalf("expected non-retryable shell-compat, got %#v", metadata)
	}
}

func TestDiagnoseHonorsExplicitRetryDisposition(t *testing.T) {
	diagnostic := Diagnose("remote_call", "call-explicit", "connection refused", map[string]interface{}{
		"error_code":  string(runtimeerrors.ErrNetworkUnavailable),
		"retryable":   false,
		"next_action": "Switch to the local fallback.",
	})
	if diagnostic.Retryable || diagnostic.NextAction != "Switch to the local fallback." {
		t.Fatalf("expected explicit runtime disposition, got %#v", diagnostic)
	}
}

func TestDiagnoseEmptySuccessOutcome(t *testing.T) {
	diagnostic := Diagnose("grep", "call-empty", "", map[string]interface{}{
		MetadataEmptyResultKey: true,
	})
	if !diagnostic.OK || !diagnostic.EmptyResult || diagnostic.Outcome != OutcomeEmpty {
		t.Fatalf("expected empty success outcome, got %#v", diagnostic)
	}
}

func TestMarkEmptySuccessAndEvidence(t *testing.T) {
	meta := MarkEmptySuccess(map[string]interface{}{
		"match_count": 0,
		"pattern":     "NoSuch",
	})
	if meta[MetadataEmptyResultKey] != true || meta[MetadataOutcomeKey] != OutcomeEmpty {
		t.Fatalf("expected empty success markers, got %#v", meta)
	}
	if !HasEmptySuccessEvidence(meta) {
		t.Fatalf("expected HasEmptySuccessEvidence true for explicit empty")
	}

	// Zero match_count alone is evidence even before MarkEmptySuccess.
	if !HasEmptySuccessEvidence(map[string]interface{}{"match_count": 0}) {
		t.Fatalf("expected match_count=0 evidence")
	}
	if !HasEmptySuccessEvidence(map[string]interface{}{
		"count": 0,
		"files": []string{},
	}) {
		t.Fatalf("expected count=0 with empty files evidence")
	}
	if HasEmptySuccessEvidence(map[string]interface{}{"count": 0}) {
		t.Fatalf("count=0 alone must not be empty evidence")
	}
	if HasEmptySuccessEvidence(map[string]interface{}{"match_count": 3}) {
		t.Fatalf("positive match_count must not be empty evidence")
	}

	// Partial / mutation must not be stamped empty.
	partial := MarkEmptySuccess(map[string]interface{}{
		"partial_failure": true,
		"failed_count":    1,
		"match_count":     0,
	})
	if partial[MetadataEmptyResultKey] == true || partial[MetadataOutcomeKey] == OutcomeEmpty {
		t.Fatalf("partial must not become empty: %#v", partial)
	}
	mut := MarkEmptySuccess(map[string]interface{}{
		"tool_metadata": map[string]interface{}{
			"mutated_paths": []string{"x.go"},
		},
	})
	if mut[MetadataEmptyResultKey] == true {
		t.Fatalf("mutation must not become empty: %#v", mut)
	}
	if HasEmptySuccessEvidence(map[string]interface{}{
		MetadataEmptyResultKey: true,
		"tool_metadata": map[string]interface{}{
			"mutated_paths": []string{"x.go"},
		},
	}) {
		t.Fatalf("mutation proof must exclude empty evidence even with stale flag")
	}
}

func TestDiagnosePartialBatchOutcome(t *testing.T) {
	diagnostic := Diagnose("bash", "call-batch", "batch completed with 1 failure(s)", map[string]interface{}{
		"batch":           true,
		"failed_count":    1,
		"requested_count": 3,
	})
	if diagnostic.OK || diagnostic.Outcome != OutcomePartial {
		t.Fatalf("expected partial batch outcome, got %#v", diagnostic)
	}
	if !strings.Contains(diagnostic.NextAction, "Reuse successful") {
		t.Fatalf("expected partial next_action, got %q", diagnostic.NextAction)
	}
	if !strings.Contains(diagnostic.NextAction, "Do not re-run the entire batch") {
		t.Fatalf("expected no full-batch retry guidance, got %q", diagnostic.NextAction)
	}
}

func TestDiagnosePartialBatchFromRequestCountAliasOnSuccess(t *testing.T) {
	// Tools such as multi-file readers publish request_count + partial_failure
	// while still returning Success with mixed item results.
	diagnostic := Diagnose("view", "call-view-batch", "", map[string]interface{}{
		"batch":           true,
		"request_count":   3,
		"succeeded_count": 2,
		"failed_count":    1,
		"partial_failure": true,
	})
	if !diagnostic.OK {
		t.Fatalf("success-path partial should keep ok=true, got %#v", diagnostic)
	}
	if diagnostic.Outcome != OutcomePartial {
		t.Fatalf("expected outcome=partial, got %#v", diagnostic)
	}
	if diagnostic.RequestedCount != 3 || diagnostic.FailedCount != 1 || diagnostic.SucceededCount != 2 {
		t.Fatalf("expected batch counts, got %#v", diagnostic)
	}
	if !diagnostic.PartialFailure {
		t.Fatalf("expected partial_failure, got %#v", diagnostic)
	}
	if !strings.Contains(diagnostic.NextAction, "1/3") {
		t.Fatalf("expected count-aware next_action, got %q", diagnostic.NextAction)
	}
}

func TestDiagnosePartialBatchFromNestedToolMetadata(t *testing.T) {
	// Agent loop nests raw toolkit metadata under tool_metadata before gateway.
	// Counts must still promote outcome=partial (live smoke regression).
	diagnostic := Diagnose("view", "call-nested-partial", "", map[string]interface{}{
		"tool_metadata": map[string]interface{}{
			"batch":           true,
			"request_count":   2,
			"succeeded_count": 1,
			"failed_count":    1,
			"partial_failure": true,
			MetadataFailedItemsKey: []map[string]interface{}{
				FailedItemMap(IntPtr(1), "missing.go", "missing.go", "path does not exist"),
			},
		},
	})
	if !diagnostic.OK {
		t.Fatalf("success-path nested partial should keep ok=true, got %#v", diagnostic)
	}
	if diagnostic.Outcome != OutcomePartial {
		t.Fatalf("expected outcome=partial from nested counts, got %#v", diagnostic)
	}
	if diagnostic.RequestedCount != 2 || diagnostic.FailedCount != 1 || diagnostic.SucceededCount != 1 {
		t.Fatalf("expected nested batch counts, got %#v", diagnostic)
	}
	if !diagnostic.PartialFailure {
		t.Fatalf("expected partial_failure from nested metadata, got %#v", diagnostic)
	}
	if len(diagnostic.FailedItems) == 0 || diagnostic.FailedItems[0].Path != "missing.go" {
		t.Fatalf("expected nested failed_items, got %#v", diagnostic.FailedItems)
	}
	if !strings.Contains(diagnostic.NextAction, "missing.go") && !strings.Contains(diagnostic.NextAction, "1/2") {
		t.Fatalf("expected partial next_action, got %q", diagnostic.NextAction)
	}

	stats := ExtractBatchStats(map[string]interface{}{
		"tool_metadata": map[string]interface{}{
			"batch":           true,
			"request_count":   2,
			"succeeded_count": 1,
			"failed_count":    1,
			"partial_failure": true,
		},
	})
	if stats.Requested != 2 || stats.Failed != 1 || stats.Succeeded != 1 || !stats.Partial {
		t.Fatalf("ExtractBatchStats should read nested tool_metadata counts, got %#v", stats)
	}
}

func TestExtractBatchStatsAliases(t *testing.T) {
	stats := ExtractBatchStats(map[string]interface{}{
		"batch":           true,
		"request_count":   4,
		"failed_count":    1,
		"succeeded_count": 3,
	})
	if stats.Requested != 4 || stats.Failed != 1 || stats.Succeeded != 3 || !stats.Partial {
		t.Fatalf("unexpected stats: %#v", stats)
	}

	inferred := ExtractBatchStats(map[string]interface{}{
		"batch":           true,
		"requested_count": 2,
		"failed_count":    1,
	})
	if inferred.Succeeded != 1 {
		t.Fatalf("expected inferred succeeded=1, got %#v", inferred)
	}
}

func TestApplyDiagnosticMetadataPartialSuccessCounts(t *testing.T) {
	metadata := map[string]interface{}{}
	diagnostic := Diagnose("view", "call-meta-partial", "", map[string]interface{}{
		"batch":           true,
		"request_count":   3,
		"succeeded_count": 2,
		"failed_count":    1,
		"partial_failure": true,
	})
	ApplyDiagnosticMetadata(metadata, diagnostic)
	if metadata[MetadataOutcomeKey] != OutcomePartial {
		t.Fatalf("expected outcome=partial metadata, got %#v", metadata)
	}
	if metadata[MetadataRequestedCountKey] != 3 || metadata[MetadataFailedCountKey] != 1 || metadata[MetadataSucceededCountKey] != 2 {
		t.Fatalf("expected promoted counts, got %#v", metadata)
	}
	if metadata[MetadataPartialFailureKey] != true {
		t.Fatalf("expected partial_failure metadata, got %#v", metadata)
	}
	if next, _ := metadata[MetadataNextActionKey].(string); !strings.Contains(next, "Reuse successful") {
		t.Fatalf("expected partial next_action, got %#v", metadata)
	}
}

func TestExtractFailedItemsFromBatchItems(t *testing.T) {
	items := ExtractFailedItems(map[string]interface{}{
		"batch": true,
		"items": []interface{}{
			map[string]interface{}{"index": 0, "command": "echo ok", "success": true},
			map[string]interface{}{"index": 1, "command": "bad-cmd", "success": false, "error": "exit status 1"},
			map[string]interface{}{"index": 2, "command": "also-bad", "success": false, "error": "not found"},
		},
	})
	if len(items) != 2 {
		t.Fatalf("expected 2 failed items, got %#v", items)
	}
	if items[0].Index == nil || *items[0].Index != 1 || items[0].Ref != "bad-cmd" {
		t.Fatalf("unexpected first failed item: %#v", items[0])
	}
	if items[1].Error != "not found" {
		t.Fatalf("unexpected second failed item: %#v", items[1])
	}
}

func TestExtractFailedItemsFromPathErrorStrings(t *testing.T) {
	items := ExtractFailedItems(map[string]interface{}{
		"errors": []string{"missing.txt: file not found", "other.go: permission denied"},
	})
	if len(items) != 2 {
		t.Fatalf("expected 2 path failures, got %#v", items)
	}
	if items[0].Path != "missing.txt" || !strings.Contains(items[0].Error, "file not found") {
		t.Fatalf("unexpected first path failure: %#v", items[0])
	}
	if items[1].Path != "other.go" {
		t.Fatalf("unexpected second path failure: %#v", items[1])
	}
}

func TestDiagnosePartialBatchIncludesFailedItemsInNextAction(t *testing.T) {
	diagnostic := Diagnose("bash", "call-batch-items", "batch completed with 1 failure(s)", map[string]interface{}{
		"batch":           true,
		"failed_count":    1,
		"requested_count": 2,
		"succeeded_count": 1,
		"items": []interface{}{
			map[string]interface{}{"index": 0, "command": "echo ok", "success": true},
			map[string]interface{}{"index": 1, "command": "Get-Content missing", "success": false, "error": "path not found"},
		},
	})
	if diagnostic.Outcome != OutcomePartial {
		t.Fatalf("expected partial, got %#v", diagnostic)
	}
	if len(diagnostic.FailedItems) != 1 {
		t.Fatalf("expected one failed item, got %#v", diagnostic.FailedItems)
	}
	if !strings.Contains(diagnostic.NextAction, "Failed items:") {
		t.Fatalf("expected item-aware next_action, got %q", diagnostic.NextAction)
	}
	if !strings.Contains(diagnostic.NextAction, "Get-Content missing") {
		t.Fatalf("expected failed command in next_action, got %q", diagnostic.NextAction)
	}
}

func TestApplyDiagnosticMetadataPromotesFailedItems(t *testing.T) {
	metadata := map[string]interface{}{}
	diagnostic := Diagnose("view", "call-failed-items", "", map[string]interface{}{
		"batch":           true,
		"request_count":   2,
		"succeeded_count": 1,
		"failed_count":    1,
		"partial_failure": true,
		MetadataFailedItemsKey: []interface{}{
			map[string]interface{}{"index": 1, "path": "missing.txt", "error": "not found"},
		},
	})
	ApplyDiagnosticMetadata(metadata, diagnostic)
	raw, ok := metadata[MetadataFailedItemsKey].([]map[string]interface{})
	if !ok || len(raw) != 1 {
		t.Fatalf("expected promoted failed_items maps, got %#v", metadata[MetadataFailedItemsKey])
	}
	if raw[0]["path"] != "missing.txt" {
		t.Fatalf("expected path in failed_items, got %#v", raw[0])
	}
	if next, _ := metadata[MetadataNextActionKey].(string); !strings.Contains(next, "missing.txt") {
		t.Fatalf("expected path-aware next_action, got %q", next)
	}
}

func TestFailedItemMapAndIntPtr(t *testing.T) {
	row := FailedItemMap(IntPtr(0), "a.go", "", "not found")
	if row == nil {
		t.Fatal("expected non-nil failed item map")
	}
	if row["index"] != 0 {
		t.Fatalf("expected index 0 preserved, got %#v", row["index"])
	}
	if row["path"] != "a.go" {
		t.Fatalf("expected path, got %#v", row["path"])
	}
	if row["ref"] != "a.go" {
		t.Fatalf("expected path as default ref, got %#v", row["ref"])
	}
	if row["error"] != "not found" {
		t.Fatalf("expected error, got %#v", row["error"])
	}
	if FailedItemMap(nil, "", "", "") != nil {
		t.Fatalf("expected empty inputs to yield nil map")
	}
	// Explicit ref should win over path default.
	withRef := FailedItemMap(IntPtr(2), "b.go", "cmd-ref", "boom")
	if withRef["ref"] != "cmd-ref" {
		t.Fatalf("expected explicit ref, got %#v", withRef)
	}
}

func TestApplyDiagnosticMetadataEmptySuccessNextAction(t *testing.T) {
	metadata := map[string]interface{}{}
	diagnostic := Diagnose("grep", "call-empty-meta", "", map[string]interface{}{
		MetadataEmptyResultKey: true,
	})
	ApplyDiagnosticMetadata(metadata, diagnostic)
	if metadata[MetadataOKKey] != true {
		t.Fatalf("expected ok=true, got %#v", metadata)
	}
	if metadata[MetadataOutcomeKey] != OutcomeEmpty {
		t.Fatalf("expected outcome=empty, got %#v", metadata)
	}
	if metadata[MetadataEmptyResultKey] != true {
		t.Fatalf("expected empty_result=true, got %#v", metadata)
	}
	next, _ := metadata[MetadataNextActionKey].(string)
	if !strings.Contains(next, "Empty successful result is valid evidence") {
		t.Fatalf("expected empty-success next_action, got %q", next)
	}
	if diagnostic.NextAction == "" {
		t.Fatalf("expected diagnostic.NextAction for empty success, got %#v", diagnostic)
	}
}

func TestDiagnosePathCandidatesAndAttemptedArgs(t *testing.T) {
	diagnostic := Diagnose("view", "call-path", "path not found: missing.go", map[string]interface{}{
		MetadataErrorCodeKey:      "TOOL_PATH_NOT_FOUND",
		MetadataRetryableKey:      false,
		MetadataPathCandidatesKey: []string{"missing_file.go", "Missing.go"},
		MetadataAttemptedArgsKey: map[string]interface{}{
			"file_path": "missing.go",
		},
	})
	if diagnostic.OK || diagnostic.Outcome != OutcomeFailed {
		t.Fatalf("expected failed path outcome, got %#v", diagnostic)
	}
	if len(diagnostic.PathCandidates) != 2 || diagnostic.PathCandidates[0] != "missing_file.go" {
		t.Fatalf("expected path candidates on diagnostic, got %#v", diagnostic.PathCandidates)
	}
	if diagnostic.AttemptedArgs["file_path"] != "missing.go" {
		t.Fatalf("expected attempted args, got %#v", diagnostic.AttemptedArgs)
	}

	metadata := map[string]interface{}{}
	ApplyDiagnosticMetadata(metadata, diagnostic)
	if raw, ok := metadata[MetadataPathCandidatesKey].([]string); !ok || len(raw) != 2 {
		t.Fatalf("expected path_candidates metadata, got %#v", metadata)
	}
	if raw, ok := metadata[MetadataAttemptedArgsKey].(map[string]interface{}); !ok || raw["file_path"] != "missing.go" {
		t.Fatalf("expected attempted_args metadata, got %#v", metadata)
	}
}

func TestDiagnoseEmptySuccessKeepsAttemptedArgs(t *testing.T) {
	diagnostic := Diagnose("grep", "call-empty-args", "", map[string]interface{}{
		MetadataEmptyResultKey: true,
		MetadataAttemptedArgsKey: map[string]interface{}{
			"pattern": "NoSuchSymbol",
			"path":    "backend",
		},
	})
	if !diagnostic.OK || diagnostic.Outcome != OutcomeEmpty {
		t.Fatalf("expected empty success, got %#v", diagnostic)
	}
	if diagnostic.AttemptedArgs["pattern"] != "NoSuchSymbol" {
		t.Fatalf("expected attempted args on empty success, got %#v", diagnostic.AttemptedArgs)
	}
	if !strings.Contains(diagnostic.NextAction, "Empty successful result is valid evidence") {
		t.Fatalf("expected empty next_action on diagnostic, got %q", diagnostic.NextAction)
	}
	if !strings.Contains(diagnostic.NextAction, "pattern") || !strings.Contains(diagnostic.NextAction, "path") {
		t.Fatalf("expected attempted arg keys in empty next_action, got %q", diagnostic.NextAction)
	}
}

func TestEnsureAttemptedArgsPromotesNestedAndFallback(t *testing.T) {
	// Nested tool_invocation summary should promote to top-level without fallback.
	metadata := map[string]interface{}{
		"tool_invocation": map[string]interface{}{
			MetadataAttemptedArgsKey: map[string]interface{}{
				"pattern": "from-invocation",
				"path":    "backend",
			},
		},
	}
	got := EnsureAttemptedArgs(metadata, map[string]interface{}{"pattern": "fallback"})
	if got["pattern"] != "from-invocation" {
		t.Fatalf("expected nested summary to win, got %#v", got)
	}
	if top, ok := metadata[MetadataAttemptedArgsKey].(map[string]interface{}); !ok || top["pattern"] != "from-invocation" {
		t.Fatalf("expected top-level promotion, got %#v", metadata)
	}

	// Missing summary should compact fallback args.
	emptyMeta := map[string]interface{}{}
	fallback := EnsureAttemptedArgs(emptyMeta, map[string]interface{}{
		"file_path": "missing.go",
		"api_token": "secret",
	})
	if fallback["file_path"] != "missing.go" {
		t.Fatalf("expected fallback compact args, got %#v", fallback)
	}
	if fallback["api_token"] != "[redacted]" {
		t.Fatalf("expected secret redaction in fallback, got %#v", fallback)
	}
	if emptyMeta[MetadataAttemptedArgsKey] == nil {
		t.Fatalf("expected metadata injection, got %#v", emptyMeta)
	}
}

func TestApplyDiagnosticMetadataPromotesNestedAttemptedArgsOnEmpty(t *testing.T) {
	metadata := map[string]interface{}{
		"tool_invocation": map[string]interface{}{
			MetadataAttemptedArgsKey: map[string]interface{}{
				"pattern": "nested-only",
			},
		},
	}
	diagnostic := Diagnose("grep", "call-nested", "", map[string]interface{}{
		MetadataEmptyResultKey: true,
		"tool_invocation":      metadata["tool_invocation"],
	})
	ApplyDiagnosticMetadata(metadata, diagnostic)
	if raw, ok := metadata[MetadataAttemptedArgsKey].(map[string]interface{}); !ok || raw["pattern"] != "nested-only" {
		t.Fatalf("expected nested attempted_args promoted on empty, got %#v", metadata)
	}
}

func TestCompactAttemptedArgsRedactsAndBounds(t *testing.T) {
	long := strings.Repeat("x", 200)
	compact := CompactAttemptedArgs(map[string]interface{}{
		"pattern":   long,
		"api_token": "super-secret",
		"_internal": "skip-me",
		"paths":     []string{"a", "b", "c", "d", "e", "f", "g"},
	})
	if _, ok := compact["_internal"]; ok {
		t.Fatalf("underscore keys must be omitted: %#v", compact)
	}
	if compact["api_token"] != "[redacted]" {
		t.Fatalf("expected secret redaction, got %#v", compact["api_token"])
	}
	pattern, _ := compact["pattern"].(string)
	if len([]rune(pattern)) > attemptedArgsMaxStringRunes {
		t.Fatalf("expected truncated pattern, got len=%d", len([]rune(pattern)))
	}
	paths, ok := compact["paths"].([]interface{})
	if !ok {
		t.Fatalf("expected paths slice, got %#v", compact["paths"])
	}
	if len(paths) != attemptedArgsMaxSliceItems+1 {
		t.Fatalf("expected bounded paths with overflow marker, got %#v", paths)
	}
	last, _ := paths[len(paths)-1].(string)
	if !strings.HasPrefix(last, "…(+") {
		t.Fatalf("expected overflow marker, got %q", last)
	}
}

func TestShellFailureNextAction_GitIgnoredPath(t *testing.T) {
	msg := "The following paths are ignored by one of your .gitignore files:\nbuild/out.bin\nhint: Use -f if you really want to add them.\nexit status 1"
	next := shellFailureNextAction(msg)
	if !strings.Contains(next, "git check-ignore") {
		t.Fatalf("expected check-ignore recovery, got %q", next)
	}
	if !strings.Contains(next, "git add -f") {
		t.Fatalf("expected force-add guidance, got %q", next)
	}
	if !isGitIgnoredPathFailure(strings.ToLower(msg)) {
		t.Fatal("expected isGitIgnoredPathFailure to match")
	}
	if isGitIgnoredPathFailure("exit status 1") {
		t.Fatal("bare exit must not look like gitignore failure")
	}
}
