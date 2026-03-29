package ui

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/fatih/color"
)

// ToolCallStatus 工具调用状态
type ToolCallStatus int

const (
	ToolCallPending ToolCallStatus = iota
	ToolCallRunning
	ToolCallSuccess
	ToolCallError
)

// ToolCallDisplay 工具调用显示配置
type ToolCallDisplay struct {
	FunctionName string
	Arguments    map[string]interface{}
	Status       ToolCallStatus
	Error        error
	Result       string
}

// toolCallColors 工具调用颜色配置
var (
	toolCallNameColor    = color.New(color.FgCyan, color.Bold)
	toolCallArgColor     = color.New(color.FgHiBlack)
	toolCallSuccessColor = color.New(color.FgGreen)
	toolCallErrorColor   = color.New(color.FgRed)
	toolCallCheckColor   = color.New(color.FgYellow)
	toolCallDimColor     = color.New(color.FgHiBlack)
)

const (
	toolResultPreviewLines  = 4
	toolResultPreviewBytes  = 1024
	toolResultPreviewLineLen = 200
)

// FormatToolCall 格式化工具调用显示
// 格式: ✓ Shell go build ./...
func FormatToolCall(display *ToolCallDisplay) string {
	var sb strings.Builder

	// 状态图标
	switch display.Status {
	case ToolCallSuccess:
		sb.WriteString(toolCallSuccessColor.Sprint("✓ "))
	case ToolCallError:
		sb.WriteString(toolCallErrorColor.Sprint("✗ "))
	case ToolCallRunning:
		sb.WriteString(toolCallCheckColor.Sprint("○ "))
	default:
		sb.WriteString("○ ")
	}

	// 工具名称
	sb.WriteString(toolCallNameColor.Sprint(formatToolFunctionName(display.FunctionName)))

	// 参数摘要（通用处理）
	argSummary := formatToolArgSummary(display.Arguments)
	if argSummary != "" {
		sb.WriteString(" ")
		sb.WriteString(argSummary)
	}

	// 失败时附加具体错误原因
	if display.Status == ToolCallError {
		errText := ""
		if display.Error != nil {
			errText = display.Error.Error()
		} else if strings.TrimSpace(display.Result) != "" {
			errText = display.Result
		}
		errSummary := formatToolErrorSummary(errText)
		if errSummary != "" {
			sb.WriteString(" - ")
			sb.WriteString(toolCallErrorColor.Sprint(errSummary))
		}
	}

	// 成功时附加简要输出摘要
	if display.Status == ToolCallSuccess {
		lines := formatToolResultSummaryLines(display.Result)
		for _, line := range lines {
			sb.WriteString("\n    | ")
			sb.WriteString(line)
		}
	}

	return sb.String()
}

// FormatToolCallStart 格式化工具调用开始显示
func FormatToolCallStart(functionName string, args map[string]interface{}) string {
	display := &ToolCallDisplay{
		FunctionName: functionName,
		Arguments:    args,
		Status:       ToolCallRunning,
	}
	return FormatToolCall(display)
}

// FormatToolCallResult 格式化工具调用结果
func FormatToolCallResult(functionName string, args map[string]interface{}, success bool, result string) string {
	status := ToolCallSuccess
	if !success {
		status = ToolCallError
	}
	display := &ToolCallDisplay{
		FunctionName: functionName,
		Arguments:    args,
		Status:       status,
		Result:       result,
	}
	return FormatToolCall(display)
}

// formatToolFunctionName 格式化函数显示名称（通用处理）
func formatToolFunctionName(name string) string {
	// 常见缩写映射（仅保留最常用的）
	shortNames := map[string]string{
		"execute_shell_command": "Shell",
	}

	if short, ok := shortNames[name]; ok {
		return short
	}

	// 通用处理：移除常见前缀
	name = strings.TrimPrefix(name, "execute_")
	name = strings.TrimPrefix(name, "run_")
	name = strings.TrimPrefix(name, "get_")
	name = strings.TrimPrefix(name, "mcp_")

	// 转换下划线/连字符为驼峰命名
	return toolToCamelCase(name)
}

// toolToCamelCase 将 snake_case 或 kebab-case 转换为 CamelCase
func toolToCamelCase(s string) string {
	s = strings.ReplaceAll(s, "-", "_")
	parts := strings.Split(s, "_")
	result := ""
	for _, part := range parts {
		if len(part) > 0 {
			result += strings.ToUpper(string(part[0])) + strings.ToLower(part[1:])
		}
	}
	return result
}

// formatToolArgSummary 格式化参数摘要（通用处理）
func formatToolArgSummary(args map[string]interface{}) string {
	if args == nil || len(args) == 0 {
		return ""
	}

	// 按优先级查找主要参数
	priorityKeys := []string{"command", "path", "file", "file_path", "filepath", "url", "query", "pattern", "name", "title", "message", "text"}

	for _, key := range priorityKeys {
		if val, ok := args[key]; ok {
			return formatToolArgValue(val)
		}
	}

	// 没有找到优先参数，显示所有简单参数
	return formatToolAllArgs(args)
}

// formatToolArgValue 格式化单个参数值
func formatToolArgValue(val interface{}) string {
	switch v := val.(type) {
	case string:
		return toolTruncateSmart(v, 60)
	case int, int64, float64, bool:
		return fmt.Sprintf("%v", v)
	default:
		return "..."
	}
}

func formatToolErrorSummary(errText string) string {
	trimmed := strings.TrimSpace(errText)
	if trimmed == "" {
		return ""
	}
	if idx := strings.IndexAny(trimmed, "\r\n"); idx >= 0 {
		trimmed = trimmed[:idx]
	}
	trimmed = strings.ReplaceAll(trimmed, "\t", " ")
	trimmed = strings.Join(strings.Fields(trimmed), " ")
	return toolTruncateSmart(trimmed, 160)
}

func formatToolResultSummaryLines(result string) []string {
	trimmed := strings.TrimSpace(result)
	if trimmed == "" {
		return nil
	}

	normalized := strings.ReplaceAll(trimmed, "\r\n", "\n")
	normalized = strings.ReplaceAll(normalized, "\r", "\n")

	truncated := truncateUTF8Bytes(normalized, toolResultPreviewBytes)
	byteTruncated := len(truncated) < len(normalized)

	lines := strings.Split(truncated, "\n")
	out := make([]string, 0, toolResultPreviewLines)
	moreLines := false

	for i, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		out = append(out, toolTruncateSmart(line, toolResultPreviewLineLen))
		if len(out) >= toolResultPreviewLines {
			if hasNonEmptyLine(lines[i+1:]) {
				moreLines = true
			}
			break
		}
	}

	if byteTruncated || moreLines {
		if len(out) == 0 {
			out = append(out, "... (truncated)")
		} else if len(out) < toolResultPreviewLines {
			out = append(out, "... (truncated)")
		} else {
			out[len(out)-1] = "... (truncated)"
		}
	}

	return out
}

func hasNonEmptyLine(lines []string) bool {
	for _, line := range lines {
		if strings.TrimSpace(line) != "" {
			return true
		}
	}
	return false
}

func truncateUTF8Bytes(text string, maxBytes int) string {
	if text == "" || maxBytes <= 0 {
		return ""
	}
	if len(text) <= maxBytes {
		return text
	}
	for maxBytes > 0 && !utf8.ValidString(text[:maxBytes]) {
		maxBytes--
	}
	return text[:maxBytes]
}

// toolTruncateSmart 智能截断字符串
func toolTruncateSmart(s string, maxLen int) string {
	s = strings.TrimSpace(s)
	if len(s) <= maxLen {
		return s
	}

	// 尝试在单词边界截断
	if idx := strings.LastIndex(s[:maxLen], " "); idx > maxLen/2 {
		return s[:idx] + "..."
	}

	// 尝试在路径分隔符处截断
	if idx := strings.LastIndex(s[:maxLen], "/"); idx > maxLen/2 {
		return "..." + s[idx:]
	}
	if idx := strings.LastIndex(s[:maxLen], "\\"); idx > maxLen/2 {
		return "..." + s[idx:]
	}

	return s[:maxLen] + "..."
}

// formatToolAllArgs 格式化所有参数
func formatToolAllArgs(args map[string]interface{}) string {
	var parts []string
	count := 0
	maxArgs := 3

	for key, val := range args {
		if count >= maxArgs {
			parts = append(parts, "...")
			break
		}
		switch v := val.(type) {
		case string:
			parts = append(parts, key+"="+toolTruncateSmart(v, 20))
		case int, int64, float64, bool:
			parts = append(parts, fmt.Sprintf("%s=%v", key, v))
		default:
			// 跳过复杂类型
			continue
		}
		count++
	}

	return strings.Join(parts, " ")
}

// PrintToolCallStart 打印工具调用开始
func PrintToolCallStart(functionName string, args map[string]interface{}) {
	fmt.Print("  ")
	fmt.Println(FormatToolCallStart(functionName, args))
}

// PrintToolCallResult 打印工具调用结果
func PrintToolCallResult(functionName string, args map[string]interface{}, success bool, result string) {
	fmt.Print("  ")
	fmt.Println(FormatToolCallResult(functionName, args, success, result))
}

// PrintToolCallsStart 打印多个工具调用的开始标题
func PrintToolCallsStart(count int) {
	if count == 1 {
		fmt.Println()
		fmt.Printf("  %s 执行工具调用...\n", GetIcon(IconTool))
	} else {
		fmt.Println()
		fmt.Printf("  %s 执行 %d 个工具调用...\n", GetIcon(IconTool), count)
	}
}

// PrintToolCallsEnd 打印工具调用完成标题
func PrintToolCallsEnd(successCount, errorCount int) {
	fmt.Println()
	if errorCount > 0 {
		fmt.Printf("  %s 完成: %d 成功, %d 失败\n", GetIcon(IconTool), successCount, errorCount)
	} else {
		fmt.Printf("  %s 完成 %d 个工具调用\n", GetIcon(IconTool), successCount)
	}
}
