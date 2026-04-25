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
	ToolCallID    string `json:"tool_call_id,omitempty"`
	Function      string `json:"function"`
	Success       bool   `json:"success"`
	Error         string `json:"error,omitempty"`
	ToolSource    string `json:"tool_source,omitempty"`
	OutputKind    string `json:"output_kind,omitempty"`
	ResultPreview string `json:"result_preview,omitempty"`
	ResultBytes   int    `json:"result_bytes,omitempty"`
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
	if !isSessionDebugEnabled(session) || strings.TrimSpace(debugInfo) == "" {
		return
	}
	if session != nil && session.Logger != nil && session.Logger.logDir != "" {
		if err := session.Logger.WriteDebugInfo(session.Logger.logDir, debugInfo); err != nil {
			fmt.Fprintf(os.Stderr, "[调试日志写入失败] %v\n", err)
		}
	}
	if printToConsole {
		fmt.Printf("\n%s\n", debugInfo)
	}
}

func formatToolExecutionStartDebug(tc functions.ToolCall) string {
	return fmt.Sprintf("[tool-debug] execute id=%s function=%s args=%s",
		tc.ID, tc.Function, marshalCompactJSON(tc.Args))
}

func formatToolExecutionResultDebug(tc functions.ToolCall, result string, err error, metadata map[string]interface{}) string {
	source, kind := compactToolExecutionMetadata(metadata)
	if err != nil {
		line := fmt.Sprintf("[tool-debug] result id=%s function=%s success=false error=%q",
			tc.ID, tc.Function, err.Error())
		if source != "" {
			line += fmt.Sprintf(" source=%s", source)
		}
		if kind != "" {
			line += fmt.Sprintf(" kind=%s", kind)
		}
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
		lines = append(lines, strings.Join(parts, " "))
	}
	return strings.Join(lines, "\n")
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

func firstNonEmptyString(value interface{}) string {
	text, _ := value.(string)
	return strings.TrimSpace(text)
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
