package commands

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/functions"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolresult"
	runtimetypes "github.com/wwsheng009/ai-agent-runtime/internal/types"
)

type failingRichTestFunction struct {
	testFunction
	metadata map[string]interface{}
	err      error
}

func (f *failingRichTestFunction) Execute(ctx context.Context, args map[string]interface{}) (string, error) {
	if f.err == nil {
		f.err = errors.New("tool failed")
	}
	return "", f.err
}

func (f *failingRichTestFunction) ExecuteWithMeta(ctx context.Context, args map[string]interface{}) (string, map[string]interface{}, error) {
	output, err := f.Execute(ctx, args)
	return output, cloneFunctionSchema(f.metadata), err
}

func TestBuildRequestLogContent_IncludesFunctionExposureReport(t *testing.T) {
	requestBody := map[string]interface{}{
		"model": "test-model",
	}
	report := &aicliFunctionExposureReport{
		Prompt:             "search alpha data",
		Mode:               skillExposurePrefer,
		FinalFunctionNames: []string{"skill__alpha"},
		SkillFunctions:     []string{"skill__alpha"},
	}

	content := buildRequestLogContent("https://example.com/v1/chat", requestBody, report)

	if content["url"] != "https://example.com/v1/chat" {
		t.Fatalf("unexpected url: %v", content["url"])
	}
	if body, ok := content["body"].(map[string]interface{}); !ok || body["model"] != "test-model" {
		t.Fatalf("unexpected body: %#v", content["body"])
	}
	if count, ok := content["exposed_function_count"].(int); !ok || count != 1 {
		t.Fatalf("unexpected exposed_function_count: %#v", content["exposed_function_count"])
	}
	if functions, ok := content["exposed_functions"].([]string); !ok || len(functions) != 1 || functions[0] != "skill__alpha" {
		t.Fatalf("unexpected exposed_functions: %#v", content["exposed_functions"])
	}
	reportValue, ok := content["function_exposure"].(*aicliFunctionExposureReport)
	if !ok || reportValue == nil {
		t.Fatalf("unexpected function_exposure: %#v", content["function_exposure"])
	}
	if reportValue.Mode != skillExposurePrefer {
		t.Fatalf("unexpected report mode: %#v", reportValue.Mode)
	}
}

func TestBuildRequestLogContent_OmitsFunctionExposureWhenNil(t *testing.T) {
	content := buildRequestLogContent("https://example.com/v1/chat", map[string]interface{}{"model": "test-model"}, nil)

	if _, exists := content["function_exposure"]; exists {
		t.Fatalf("did not expect function_exposure in content: %#v", content)
	}
	if _, exists := content["exposed_function_count"]; exists {
		t.Fatalf("did not expect exposed_function_count in content: %#v", content)
	}
	if _, exists := content["exposed_functions"]; exists {
		t.Fatalf("did not expect exposed_functions in content: %#v", content)
	}
}

func TestBuildToolExecutionSummary_IncludesSuccessAndErrorCalls(t *testing.T) {
	summary := buildToolExecutionSummary([]aicliToolExecutionCallSummary{
		{
			ToolCallID:              "call-1",
			Function:                "skill__alpha",
			Success:                 true,
			ToolSource:              "mcp",
			OutputKind:              "text",
			ResultPreview:           "OK",
			ResultBytes:             2,
			OutputCaptureComplete:   boolPointer(true),
			CaptureLimitReached:     boolPointer(false),
			RetainedOutputBytes:     2,
			OutputCaptureLimitBytes: 4096,
			RawOutputArtifactPath:   `C:\temp\shell-output\toolkit\git_123.txt`,
		},
		{
			ToolCallID: "call-2",
			Function:   "builtin__diagnose",
			Success:    false,
			Error:      "boom",
			ToolSource: "meta",
			OutputKind: "json",
		},
	}, 1, 1)

	if summary == nil {
		t.Fatal("expected tool execution summary")
	}
	if summary.CallCount != 2 || summary.SuccessCount != 1 || summary.ErrorCount != 1 {
		t.Fatalf("unexpected counts: %+v", summary)
	}
	if len(summary.Functions) != 2 || summary.Functions[0] != "skill__alpha" || summary.Functions[1] != "builtin__diagnose" {
		t.Fatalf("unexpected functions: %+v", summary.Functions)
	}
	if len(summary.Calls) != 2 || !summary.Calls[0].Success || summary.Calls[1].Error != "boom" {
		t.Fatalf("unexpected call summaries: %+v", summary.Calls)
	}
	if summary.Calls[0].ToolSource != "mcp" || summary.Calls[0].OutputKind != "text" {
		t.Fatalf("expected first call metadata, got %+v", summary.Calls[0])
	}
	if summary.Calls[0].RawOutputArtifactPath != `C:\temp\shell-output\toolkit\git_123.txt` {
		t.Fatalf("expected first call artifact path, got %+v", summary.Calls[0])
	}
	if summary.Calls[0].OutputCaptureComplete == nil || !*summary.Calls[0].OutputCaptureComplete {
		t.Fatalf("expected first call output_capture_complete=true, got %+v", summary.Calls[0])
	}
	if summary.Calls[0].CaptureLimitReached == nil || *summary.Calls[0].CaptureLimitReached {
		t.Fatalf("expected first call capture_limit_reached=false, got %+v", summary.Calls[0])
	}
	if summary.Calls[1].ToolSource != "meta" || summary.Calls[1].OutputKind != "json" {
		t.Fatalf("expected second call metadata, got %+v", summary.Calls[1])
	}
}

func boolPointer(v bool) *bool {
	return &v
}

func TestChatLogger_LogToolExecutionSummary_AppendsMessage(t *testing.T) {
	logger := NewChatLogger("nvidia", "openai", "test-model", false, "https://example.com")
	scope := aicliLogScope{TurnID: "turn-0001", RequestID: "turn-0001-req-01"}
	logger.LogToolExecutionSummary(scope, &aicliToolExecutionSummary{
		CallCount:    1,
		SuccessCount: 1,
		Functions:    []string{"skill__alpha"},
	})

	if len(logger.sessionLog.Messages) != 1 {
		t.Fatalf("expected 1 log message, got %d", len(logger.sessionLog.Messages))
	}
	entry := logger.sessionLog.Messages[0]
	if entry.MessageType != "tool_execution_summary" {
		t.Fatalf("unexpected message type: %s", entry.MessageType)
	}
	if entry.TurnID != scope.TurnID || entry.RequestID != scope.RequestID {
		t.Fatalf("unexpected scope: turn=%s request=%s", entry.TurnID, entry.RequestID)
	}
	summary, ok := entry.Content.(*aicliToolExecutionSummary)
	if !ok || summary == nil {
		t.Fatalf("unexpected summary payload: %#v", entry.Content)
	}
	if summary.CallCount != 1 || len(summary.Functions) != 1 || summary.Functions[0] != "skill__alpha" {
		t.Fatalf("unexpected summary content: %+v", summary)
	}
}

func TestChatLogger_LogToolEvents_PropagateToolCallID(t *testing.T) {
	logger := NewChatLogger("nvidia", "openai", "test-model", false, "https://example.com")
	scope := aicliLogScope{TurnID: "turn-0001", RequestID: "turn-0001-req-01"}

	logger.LogToolCall(scope, "call-1", "skill__alpha", map[string]interface{}{"prompt": "run"})
	logger.LogToolResult(scope, "call-1", "skill__alpha", "OK", nil)

	if len(logger.sessionLog.Messages) != 2 {
		t.Fatalf("expected 2 log messages, got %d", len(logger.sessionLog.Messages))
	}

	callEntry := logger.sessionLog.Messages[0]
	resultEntry := logger.sessionLog.Messages[1]
	if callEntry.ToolCallID != "call-1" || resultEntry.ToolCallID != "call-1" {
		t.Fatalf("unexpected tool_call_id values: %+v / %+v", callEntry, resultEntry)
	}
	if callEntry.TurnID != scope.TurnID || resultEntry.RequestID != scope.RequestID {
		t.Fatalf("unexpected scope propagation: %+v / %+v", callEntry, resultEntry)
	}
}

func TestNextLogScope_IncrementsPerTurnAndPerRequest(t *testing.T) {
	session := &ChatSession{
		TurnContextTokenCount:   999,
		ContextTokenCount:       888,
		ContextWindowTokenCount: 777,
	}

	scope1 := nextLogScope(session, "first turn")
	if session.TurnContextTokenCount != 0 {
		t.Fatalf("expected new turn to reset turn token usage, got %+v", session)
	}
	if session.ContextTokenCount != 888 || session.ContextWindowTokenCount != 777 {
		t.Fatalf("expected new turn to preserve session context usage, got %+v", session)
	}
	session.TurnContextTokenCount = 42
	session.ContextTokenCount = 24
	session.ContextWindowTokenCount = 2048
	scope2 := nextLogScope(session, "")
	if session.TurnContextTokenCount != 42 || session.ContextTokenCount != 24 || session.ContextWindowTokenCount != 2048 {
		t.Fatalf("expected same turn to preserve turn token usage, got %+v", session)
	}
	scope3 := nextLogScope(session, "second turn")
	if session.TurnContextTokenCount != 0 {
		t.Fatalf("expected second turn to reset turn token usage, got %+v", session)
	}
	if session.ContextTokenCount != 24 || session.ContextWindowTokenCount != 2048 {
		t.Fatalf("expected second turn to preserve session context usage, got %+v", session)
	}

	if scope1.TurnID != "turn-0001" || scope1.RequestID != "turn-0001-req-01" {
		t.Fatalf("unexpected first scope: %+v", scope1)
	}
	if scope2.TurnID != "turn-0001" || scope2.RequestID != "turn-0001-req-02" {
		t.Fatalf("unexpected second scope: %+v", scope2)
	}
	if scope3.TurnID != "turn-0002" || scope3.RequestID != "turn-0002-req-01" {
		t.Fatalf("unexpected third scope: %+v", scope3)
	}
}

func TestNextLogScope_ConsumesPrecountedUserTurn(t *testing.T) {
	session := &ChatSession{
		Messages: []runtimetypes.Message{
			*runtimetypes.NewSystemMessage("system"),
			*runtimetypes.NewUserMessage("previous"),
			*runtimetypes.NewAssistantMessage("answer"),
		},
	}

	beginChatUserTurn(session, "first turn")
	if session.MsgCount != 1 || session.TurnRequestCount != 0 {
		t.Fatalf("expected user turn to be pre-counted, got msgs=%d requests=%d", session.MsgCount, session.TurnRequestCount)
	}
	if session.StatusMessageCount != 3 {
		t.Fatalf("expected status message count to include visible history plus pending prompt, got %d", session.StatusMessageCount)
	}

	scope := nextLogScope(session, "first turn")

	if scope.TurnID != "turn-0001" || scope.RequestID != "turn-0001-req-01" {
		t.Fatalf("unexpected scope for pre-counted turn: %+v", scope)
	}
	if session.MsgCount != 1 || session.TurnRequestCount != 1 {
		t.Fatalf("expected request scope not to double-count pre-counted turn, got msgs=%d requests=%d", session.MsgCount, session.TurnRequestCount)
	}
	if session.turnPrimed {
		t.Fatalf("expected first request scope to consume the pre-counted turn marker")
	}
}

func TestApplyChatTurnContextTokens_DoesNotLowerLiveSessionContextSnapshot(t *testing.T) {
	session := &ChatSession{ContextTokenCount: 900}

	applyChatTurnContextTokens(session, 100, 1000, false)
	applyChatTurnContextTokens(session, 250, 1000, false)

	if session.ContextTokenCount != 900 {
		t.Fatalf("expected smaller live request contexts not to lower session snapshot, got %d", session.ContextTokenCount)
	}
	if session.TurnContextTokenCount != 350 {
		t.Fatalf("expected turn aggregate context tokens to be 350, got %d", session.TurnContextTokenCount)
	}
	if session.ContextWindowTokenCount != 1000 {
		t.Fatalf("expected context window token count to be 1000, got %d", session.ContextWindowTokenCount)
	}
}

func TestApplyChatContextTokensFromUsage_UsesTotalTokensAsActiveContextSnapshot(t *testing.T) {
	session := &ChatSession{}

	got := applyChatContextTokensFromUsage(session, &runtimetypes.TokenUsage{
		PromptTokens:     100,
		CompletionTokens: 25,
		TotalTokens:      130,
		CachedTokens:     80,
		ReasoningTokens:  20,
	}, 1000, false)

	if got != 130 || session.ContextTokenCount != 130 {
		t.Fatalf("expected total tokens to become active context snapshot, got return=%d context=%d", got, session.ContextTokenCount)
	}
	if session.ContextWindowTokenCount != 1000 {
		t.Fatalf("expected context window token count to be 1000, got %d", session.ContextWindowTokenCount)
	}
}

func TestApplyChatContextTokensFromUsage_FallsBackToInputPlusOutput(t *testing.T) {
	session := &ChatSession{}

	got := applyChatContextTokensFromUsage(session, &runtimetypes.TokenUsage{
		PromptTokens:     100,
		CompletionTokens: 25,
	}, 0, false)

	if got != 125 || session.ContextTokenCount != 125 {
		t.Fatalf("expected prompt+completion fallback to become active context snapshot, got return=%d context=%d", got, session.ContextTokenCount)
	}
}

func TestApplyChatContextTokensFromUsage_DoesNotLowerExistingActiveContextSnapshot(t *testing.T) {
	session := &ChatSession{ContextTokenCount: 1320}

	got := applyChatContextTokensFromUsage(session, &runtimetypes.TokenUsage{
		PromptTokens:     12,
		CompletionTokens: 28,
		TotalTokens:      40,
	}, 256000, false)

	if got != 1320 || session.ContextTokenCount != 1320 {
		t.Fatalf("expected smaller request usage not to lower active context snapshot, got return=%d context=%d", got, session.ContextTokenCount)
	}
	if session.ContextWindowTokenCount != 256000 {
		t.Fatalf("expected window tokens to still update, got %d", session.ContextWindowTokenCount)
	}
}

func TestChatLogger_LogRequestAndResponse_PropagateScopeAndDuration(t *testing.T) {
	logger := NewChatLogger("nvidia", "openai", "test-model", false, "https://example.com")
	scope := aicliLogScope{TurnID: "turn-0001", RequestID: "turn-0001-req-01"}

	logger.LogRequest(scope, map[string]interface{}{"body": "request"})
	logger.LogResponse(scope, map[string]interface{}{"content": "response"}, nil, false, nil, 123)

	if len(logger.sessionLog.Messages) != 2 {
		t.Fatalf("expected 2 log messages, got %d", len(logger.sessionLog.Messages))
	}
	requestEntry := logger.sessionLog.Messages[0]
	responseEntry := logger.sessionLog.Messages[1]

	if requestEntry.MessageType != "request" || responseEntry.MessageType != "response" {
		t.Fatalf("unexpected message types: %s / %s", requestEntry.MessageType, responseEntry.MessageType)
	}
	if requestEntry.TurnID != scope.TurnID || requestEntry.RequestID != scope.RequestID {
		t.Fatalf("unexpected request scope: %+v", requestEntry)
	}
	if responseEntry.TurnID != scope.TurnID || responseEntry.RequestID != scope.RequestID {
		t.Fatalf("unexpected response scope: %+v", responseEntry)
	}
	if requestEntry.Duration != 123 {
		t.Fatalf("expected request duration to be updated to 123, got %d", requestEntry.Duration)
	}
	if responseEntry.Duration != 123 {
		t.Fatalf("expected response duration to be 123, got %d", responseEntry.Duration)
	}
}

func TestTruncateOutputPreview_TruncatesSingleLineByBytes(t *testing.T) {
	text := strings.Repeat("界", 600)

	preview := truncateOutputPreview(text, maxToolResultPreviewLines, 64)

	if preview == text {
		t.Fatal("expected preview to be truncated")
	}
	if len(preview) > 64 {
		t.Fatalf("expected preview to be at most 64 bytes, got %d", len(preview))
	}
	if !utf8.ValidString(preview) {
		t.Fatalf("expected preview to remain valid utf8: %q", preview)
	}
	if !strings.Contains(preview, "已省略剩余内容") {
		t.Fatalf("expected preview truncation marker, got %q", preview)
	}
}

func TestSendHTTPRequest_FailFastCapturesRetryableResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte("error code: 502"))
	}))
	defer server.Close()

	req, err := http.NewRequest(http.MethodPost, server.URL, strings.NewReader(`{"hello":"world"}`))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}

	resp, body, report, err := sendHTTPRequest(server.Client(), req, RetryConfig{DisableRetries: true}, nil)
	if err == nil {
		t.Fatal("expected error")
	}
	if resp == nil || resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("expected 502 response, got %#v", resp)
	}
	if string(body) != "error code: 502" {
		t.Fatalf("unexpected body: %q", string(body))
	}
	if report == nil || len(report.Attempts) != 1 {
		t.Fatalf("expected one attempt in report, got %#v", report)
	}
	if report.FinalStatusCode != http.StatusBadGateway {
		t.Fatalf("unexpected final status: %+v", report)
	}
	if !strings.Contains(report.LastResponsePreview, "error code: 502") {
		t.Fatalf("unexpected preview: %+v", report)
	}
}

func TestSanitizeHeadersForDebug_RedactsAuthorization(t *testing.T) {
	header := http.Header{}
	header.Set("Authorization", "Bearer sk-1234567890abcdef")
	header.Set("Content-Type", "application/json")

	sanitized := sanitizeHeadersForDebug(header)
	if sanitized["Authorization"][0] == header.Get("Authorization") {
		t.Fatalf("expected authorization to be redacted, got %q", sanitized["Authorization"][0])
	}
	if !strings.Contains(sanitized["Authorization"][0], "***") {
		t.Fatalf("expected redaction marker, got %q", sanitized["Authorization"][0])
	}
	if sanitized["Content-Type"][0] != "application/json" {
		t.Fatalf("unexpected content-type: %q", sanitized["Content-Type"][0])
	}
}

func TestExtractUsageFromResponseBody(t *testing.T) {
	usage := extractUsageFromResponseBody([]byte(`{"usage":{"total_tokens":42,"input_tokens":21}}`))
	if usage == nil {
		t.Fatal("expected usage payload")
	}
	if total, ok := usage["total_tokens"].(int); !ok || total != 42 {
		t.Fatalf("unexpected total_tokens: %#v", usage)
	}
	if input, ok := usage["input_tokens"].(int); !ok || input != 21 {
		t.Fatalf("unexpected input_tokens: %#v", usage)
	}

	if usage := extractUsageFromResponseBody([]byte(`not-json`)); usage != nil {
		t.Fatalf("expected nil for invalid json, got %#v", usage)
	}
}

func TestFormatToolExecutionResultDebug_UsesPreviewAndError(t *testing.T) {
	successReport := formatToolExecutionResultDebug(
		functions.ToolCall{ID: "call-1", Function: "bash"},
		"line1\nline2",
		nil,
		map[string]interface{}{
			toolresult.SourceKey:   toolresult.SourceToolkit,
			toolresult.MetadataKey: toolresult.KindText,
			"shell_type":           "pwsh",
			"shell_path":           `C:\Program Files\PowerShell\7\pwsh.exe`,
			"shell_display":        `pwsh (C:\Program Files\PowerShell\7\pwsh.exe)`,
		},
	)
	if !strings.Contains(successReport, "success=true") || !strings.Contains(successReport, `preview="line1`) {
		t.Fatalf("unexpected success report: %s", successReport)
	}
	if !strings.Contains(successReport, "source=toolkit") || !strings.Contains(successReport, "kind=text") {
		t.Fatalf("expected metadata in success report: %s", successReport)
	}
	if !strings.Contains(successReport, `shell="pwsh (C:\\Program Files\\PowerShell\\7\\pwsh.exe)"`) {
		t.Fatalf("expected shell metadata in success report: %s", successReport)
	}

	errorReport := formatToolExecutionResultDebug(
		functions.ToolCall{ID: "call-2", Function: "view"},
		"",
		http.ErrHandlerTimeout,
		map[string]interface{}{
			toolresult.SourceKey:   toolresult.SourceMeta,
			toolresult.MetadataKey: toolresult.KindStructured,
		},
	)
	if !strings.Contains(errorReport, "success=false") || !strings.Contains(errorReport, http.ErrHandlerTimeout.Error()) {
		t.Fatalf("unexpected error report: %s", errorReport)
	}
	if !strings.Contains(errorReport, "source=meta") || !strings.Contains(errorReport, "kind=structured") {
		t.Fatalf("expected metadata in error report: %s", errorReport)
	}
}

func TestFormatToolExecutionSummaryDebug_IncludesCounts(t *testing.T) {
	report := formatToolExecutionSummaryDebug(&aicliToolExecutionSummary{
		CallCount:    2,
		SuccessCount: 1,
		ErrorCount:   1,
		Functions:    []string{"bash", "view"},
		Calls: []aicliToolExecutionCallSummary{
			{ToolCallID: "call-1", Function: "bash", Success: true, ToolSource: "toolkit", OutputKind: "text", ShellDisplay: `pwsh (C:\Program Files\PowerShell\7\pwsh.exe)`},
			{ToolCallID: "call-2", Function: "view", Success: false, ToolSource: "meta", OutputKind: "json"},
		},
	})

	if !strings.Contains(report, "summary calls=2 success=1 error=1") {
		t.Fatalf("unexpected report: %s", report)
	}
	if !strings.Contains(report, "functions=bash, view") {
		t.Fatalf("unexpected report: %s", report)
	}
	if !strings.Contains(report, `call id=call-1 function=bash success=true source=toolkit kind=text shell="pwsh (C:\\Program Files\\PowerShell\\7\\pwsh.exe)"`) {
		t.Fatalf("expected first call metadata in report: %s", report)
	}
	if !strings.Contains(report, "call id=call-2 function=view success=false source=meta kind=json") {
		t.Fatalf("expected second call metadata in report: %s", report)
	}
}

func TestChatLogger_LogResponse_FallsBackToRawContentForPlainText(t *testing.T) {
	logger := NewChatLogger("codex_ee", "codex", "gpt-5.2-codex", false, "https://example.com")
	scope := aicliLogScope{TurnID: "turn-0001", RequestID: "turn-0001-req-01"}

	logger.LogResponse(scope, nil, []byte("error code: 502"), false, http.ErrHandlerTimeout, 10)

	if len(logger.sessionLog.Messages) != 1 {
		t.Fatalf("expected one log entry, got %d", len(logger.sessionLog.Messages))
	}
	entry := logger.sessionLog.Messages[0]
	if entry.RawContent != "error code: 502" {
		t.Fatalf("expected raw content fallback, got %#v", entry.RawContent)
	}
	if len(entry.RawContentJSON) != 0 {
		t.Fatalf("did not expect raw json for plain text, got %s", string(entry.RawContentJSON))
	}
}

func TestChatLogger_SanitizeRawJSONMessages_DowngradesInvalidRawJSON(t *testing.T) {
	logger := NewChatLogger("codex_ee", "codex", "gpt-5.2-codex", false, "https://example.com")
	logger.sessionLog.Messages = append(logger.sessionLog.Messages, ChatLogDetail{
		MessageType:    "response",
		RawContentJSON: []byte("error code: 502"),
	})

	logger.sanitizeRawJSONMessages()

	entry := logger.sessionLog.Messages[0]
	if entry.RawContent != "error code: 502" {
		t.Fatalf("expected raw content downgrade, got %#v", entry.RawContent)
	}
	if len(entry.RawContentJSON) != 0 {
		t.Fatalf("expected raw json to be cleared, got %s", string(entry.RawContentJSON))
	}
}
