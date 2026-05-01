package commands

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/functions"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolresult"
)

type aicliToolExecutionCallSummary struct {
	ToolCallID                 string `json:"tool_call_id,omitempty"`
	Function                   string `json:"function"`
	Success                    bool   `json:"success"`
	Error                      string `json:"error,omitempty"`
	ToolSource                 string `json:"tool_source,omitempty"`
	OutputKind                 string `json:"output_kind,omitempty"`
	ShellType                  string `json:"shell_type,omitempty"`
	ShellPath                  string `json:"shell_path,omitempty"`
	ShellDisplay               string `json:"shell_display,omitempty"`
	ResultPreview              string `json:"result_preview,omitempty"`
	ResultBytes                int    `json:"result_bytes,omitempty"`
	OutputCaptureComplete      *bool  `json:"output_capture_complete,omitempty"`
	CaptureLimitReached        *bool  `json:"capture_limit_reached,omitempty"`
	OutputCaptureLimitDisabled *bool  `json:"output_capture_limit_disabled,omitempty"`
	OutputCaptureLimitBytes    int    `json:"output_capture_limit_bytes,omitempty"`
	RetainedOutputBytes        int    `json:"retained_output_bytes,omitempty"`
	OmittedOutputBytes         int    `json:"omitted_output_bytes,omitempty"`
	RawOutputArtifactPath      string `json:"raw_output_artifact_path,omitempty"`
}

type aicliToolExecutionSummary struct {
	CallCount    int                             `json:"call_count"`
	SuccessCount int                             `json:"success_count"`
	ErrorCount   int                             `json:"error_count"`
	Functions    []string                        `json:"functions,omitempty"`
	Calls        []aicliToolExecutionCallSummary `json:"calls,omitempty"`
}

func isSessionDebugEnabled(session *ChatSession) bool {
	return session != nil && (session.HTTPDebug || session.SkillsDebug)
}

func writeSessionDebugInfo(session *ChatSession, debugInfo string, printToConsole bool) {
	if strings.TrimSpace(debugInfo) == "" {
		return
	}
	if session != nil && session.Logger != nil && session.Logger.logDir != "" {
		if err := session.Logger.WriteDebugInfo(session.Logger.logDir, debugInfo); err != nil {
			fmt.Fprintf(os.Stderr, "[调试日志写入失败] %v\n", err)
		}
	}
	if printToConsole && isSessionDebugEnabled(session) {
		beginDirectInteractiveOutput(session)
		fmt.Printf("\n%s\n", debugInfo)
	}
}

func formatToolExecutionStartDebug(tc functions.ToolCall) string {
	return fmt.Sprintf("[tool-debug] execute id=%s function=%s args=%s",
		tc.ID, tc.Function, marshalCompactJSON(tc.Args))
}

func formatToolExecutionResultDebug(tc functions.ToolCall, result string, err error, metadata map[string]interface{}) string {
	source, kind := compactToolExecutionMetadata(metadata)
	captureSummary := aicliToolExecutionCallSummary{}
	applyToolExecutionOutputCaptureMetadata(&captureSummary, metadata)
	applyToolExecutionShellMetadata(&captureSummary, metadata)
	if err != nil {
		line := fmt.Sprintf("[tool-debug] result id=%s function=%s success=false error=%q",
			tc.ID, tc.Function, err.Error())
		if source != "" {
			line += fmt.Sprintf(" source=%s", source)
		}
		if kind != "" {
			line += fmt.Sprintf(" kind=%s", kind)
		}
		if shell := toolExecutionShellDebugValue(captureSummary); shell != "" {
			line += fmt.Sprintf(" shell=%q", shell)
		}
		line += formatToolExecutionCaptureDebugSuffix(captureSummary)
		return line
	}
	line := fmt.Sprintf("[tool-debug] result id=%s function=%s success=true bytes=%d preview=%q",
		tc.ID, tc.Function, len(result), truncateOutputPreview(result, maxToolResultPreviewLines, maxToolResultPreviewBytes))
	if source != "" {
		line += fmt.Sprintf(" source=%s", source)
	}
	if kind != "" {
		line += fmt.Sprintf(" kind=%s", kind)
	}
	if shell := toolExecutionShellDebugValue(captureSummary); shell != "" {
		line += fmt.Sprintf(" shell=%q", shell)
	}
	line += formatToolExecutionCaptureDebugSuffix(captureSummary)
	return line
}

func formatToolExecutionSummaryDebug(summary *aicliToolExecutionSummary) string {
	if summary == nil {
		return "[tool-debug] no tool execution summary"
	}
	lines := []string{
		fmt.Sprintf("[tool-debug] summary calls=%d success=%d error=%d",
			summary.CallCount, summary.SuccessCount, summary.ErrorCount),
	}
	if len(summary.Functions) > 0 {
		lines = append(lines, fmt.Sprintf("[tool-debug] functions=%s", strings.Join(summary.Functions, ", ")))
	}
	for _, call := range summary.Calls {
		parts := []string{
			fmt.Sprintf("[tool-debug] call id=%s function=%s success=%t", call.ToolCallID, call.Function, call.Success),
		}
		if strings.TrimSpace(call.ToolSource) != "" {
			parts = append(parts, fmt.Sprintf("source=%s", strings.TrimSpace(call.ToolSource)))
		}
		if strings.TrimSpace(call.OutputKind) != "" {
			parts = append(parts, fmt.Sprintf("kind=%s", strings.TrimSpace(call.OutputKind)))
		}
		if shell := toolExecutionShellDebugValue(call); shell != "" {
			parts = append(parts, fmt.Sprintf("shell=%q", shell))
		}
		if call.OutputCaptureComplete != nil {
			parts = append(parts, fmt.Sprintf("capture_complete=%t", *call.OutputCaptureComplete))
		}
		if call.CaptureLimitReached != nil {
			parts = append(parts, fmt.Sprintf("capture_limit_reached=%t", *call.CaptureLimitReached))
		}
		if call.OutputCaptureLimitDisabled != nil {
			parts = append(parts, fmt.Sprintf("capture_limit_disabled=%t", *call.OutputCaptureLimitDisabled))
		}
		if call.OutputCaptureLimitBytes > 0 {
			parts = append(parts, fmt.Sprintf("capture_limit_bytes=%d", call.OutputCaptureLimitBytes))
		}
		if call.RetainedOutputBytes > 0 {
			parts = append(parts, fmt.Sprintf("retained_bytes=%d", call.RetainedOutputBytes))
		}
		if call.OmittedOutputBytes > 0 {
			parts = append(parts, fmt.Sprintf("omitted_bytes=%d", call.OmittedOutputBytes))
		}
		if strings.TrimSpace(call.RawOutputArtifactPath) != "" {
			parts = append(parts, fmt.Sprintf("artifact=%s", strings.TrimSpace(call.RawOutputArtifactPath)))
		}
		lines = append(lines, strings.Join(parts, " "))
	}
	return strings.Join(lines, "\n")
}

func formatToolExecutionCaptureDebugSuffix(summary aicliToolExecutionCallSummary) string {
	parts := make([]string, 0, 6)
	if summary.OutputCaptureComplete != nil {
		parts = append(parts, fmt.Sprintf("capture_complete=%t", *summary.OutputCaptureComplete))
	}
	if summary.CaptureLimitReached != nil {
		parts = append(parts, fmt.Sprintf("capture_limit_reached=%t", *summary.CaptureLimitReached))
	}
	if summary.OutputCaptureLimitDisabled != nil {
		parts = append(parts, fmt.Sprintf("capture_limit_disabled=%t", *summary.OutputCaptureLimitDisabled))
	}
	if summary.OutputCaptureLimitBytes > 0 {
		parts = append(parts, fmt.Sprintf("capture_limit_bytes=%d", summary.OutputCaptureLimitBytes))
	}
	if summary.RetainedOutputBytes > 0 {
		parts = append(parts, fmt.Sprintf("retained_bytes=%d", summary.RetainedOutputBytes))
	}
	if summary.OmittedOutputBytes > 0 {
		parts = append(parts, fmt.Sprintf("omitted_bytes=%d", summary.OmittedOutputBytes))
	}
	if strings.TrimSpace(summary.RawOutputArtifactPath) != "" {
		parts = append(parts, fmt.Sprintf("artifact=%s", strings.TrimSpace(summary.RawOutputArtifactPath)))
	}
	if len(parts) == 0 {
		return ""
	}
	return " " + strings.Join(parts, " ")
}

func toolExecutionLogPayload(content string, metadata map[string]interface{}) interface{} {
	if len(metadata) == 0 {
		return content
	}
	return map[string]interface{}{
		"content":  content,
		"metadata": cloneFunctionSchema(metadata),
	}
}

func compactToolExecutionMetadata(metadata map[string]interface{}) (string, string) {
	if len(metadata) == 0 {
		return "", ""
	}
	return firstNonEmptyString(metadata[toolresult.SourceKey]), firstNonEmptyString(metadata[toolresult.MetadataKey])
}

func applyToolExecutionOutputCaptureMetadata(summary *aicliToolExecutionCallSummary, metadata map[string]interface{}) {
	if summary == nil || len(metadata) == 0 {
		return
	}
	summary.OutputCaptureComplete = firstBoolPointer(metadata["output_capture_complete"])
	summary.CaptureLimitReached = firstBoolPointer(metadata["capture_limit_reached"])
	summary.OutputCaptureLimitDisabled = firstBoolPointer(metadata["output_capture_limit_disabled"])
	summary.OutputCaptureLimitBytes = firstPositiveInt(metadata["output_capture_limit_bytes"])
	summary.RetainedOutputBytes = firstPositiveInt(metadata["retained_output_bytes"])
	summary.OmittedOutputBytes = firstPositiveInt(metadata["omitted_output_bytes"])
	summary.RawOutputArtifactPath = firstNonEmptyString(metadata["raw_output_artifact_path"])
}

func applyToolExecutionShellMetadata(summary *aicliToolExecutionCallSummary, metadata map[string]interface{}) {
	if summary == nil || len(metadata) == 0 {
		return
	}
	summary.ShellType = firstNonEmptyString(metadata["shell_type"])
	summary.ShellPath = firstNonEmptyString(metadata["shell_path"])
	summary.ShellDisplay = firstNonEmptyString(metadata["shell_display"])
}

func toolExecutionShellDebugValue(summary aicliToolExecutionCallSummary) string {
	if trimmed := strings.TrimSpace(summary.ShellDisplay); trimmed != "" {
		return trimmed
	}
	shellType := strings.TrimSpace(summary.ShellType)
	shellPath := strings.TrimSpace(summary.ShellPath)
	switch {
	case shellType != "" && shellPath != "":
		return fmt.Sprintf("%s (%s)", shellType, shellPath)
	case shellType != "":
		return shellType
	case shellPath != "":
		return shellPath
	default:
		return ""
	}
}

func firstNonEmptyString(value interface{}) string {
	text, _ := value.(string)
	return strings.TrimSpace(text)
}

func firstBoolPointer(value interface{}) *bool {
	flag, ok := value.(bool)
	if !ok {
		return nil
	}
	return &flag
}

func firstPositiveInt(value interface{}) int {
	switch typed := value.(type) {
	case int:
		if typed > 0 {
			return typed
		}
	case int32:
		if typed > 0 {
			return int(typed)
		}
	case int64:
		if typed > 0 {
			return int(typed)
		}
	case float32:
		if typed > 0 && float32(int(typed)) == typed {
			return int(typed)
		}
	case float64:
		if typed > 0 && float64(int(typed)) == typed {
			return int(typed)
		}
	}
	return 0
}

func marshalCompactJSON(value interface{}) string {
	if value == nil {
		return "null"
	}
	data, err := json.Marshal(value)
	if err != nil {
		return fmt.Sprintf("%v", value)
	}
	return string(data)
}

func buildToolExecutionSummary(calls []aicliToolExecutionCallSummary, successCount, errorCount int) *aicliToolExecutionSummary {
	summary := &aicliToolExecutionSummary{
		CallCount:    len(calls),
		SuccessCount: successCount,
		ErrorCount:   errorCount,
		Calls:        append([]aicliToolExecutionCallSummary(nil), calls...),
	}
	functions := make([]string, 0, len(calls))
	seen := make(map[string]struct{}, len(calls))
	for _, call := range calls {
		if call.Function == "" {
			continue
		}
		if _, exists := seen[call.Function]; exists {
			continue
		}
		seen[call.Function] = struct{}{}
		functions = append(functions, call.Function)
	}
	if len(functions) > 0 {
		summary.Functions = functions
	}
	return summary
}
