package ui

import (
	"fmt"
	"strings"
)

const sessionInfoLabelWidth = 10

// SessionInfo 描述 chat 启动时展示的会话信息
type SessionInfo struct {
	ProviderName     string
	Protocol         string
	ModelName        string
	EndpointURL      string
	Host             string
	KeyCount         int
	Timeout          string
	IsStream         bool
	IsReasoningModel bool
}

// PrintSessionInfo 打印会话信息
func PrintSessionInfo(info SessionInfo) bool {
	theme := GetTheme(ThemeAuto)

	PrintEmptyLine()
	PrintThickSeparator()

	// Provider 信息
	printSessionInfoRow(theme.SystemIcon+" ", "Provider:", theme.SuccessColor.Sprint("( "+info.ProviderName+" )"), theme.ColorizeLabel)
	if info.Protocol != "" {
		printSessionInfoRow(sessionInfoChildPrefix(theme), "Protocol:", theme.Dimmed(info.Protocol), theme.ColorizeLabel)
	}
	if info.EndpointURL != "" {
		printSessionInfoRow(sessionInfoChildPrefix(theme), "Endpoint:", theme.Dimmed(info.EndpointURL), theme.ColorizeLabel)
	}
	if info.Host != "" {
		printSessionInfoRow(sessionInfoChildPrefix(theme), "Host:", theme.Dimmed(info.Host), theme.ColorizeLabel)
	}
	if info.KeyCount > 0 {
		printSessionInfoRow(sessionInfoChildPrefix(theme), "Auth Keys:", theme.Dimmed(fmt.Sprintf("%d", info.KeyCount)), theme.ColorizeLabel)
	}
	if info.Timeout != "" {
		printSessionInfoRow(sessionInfoChildPrefix(theme), "Timeout:", theme.Dimmed(info.Timeout), theme.ColorizeLabel)
	}

	// Model 信息
	printSessionInfoRow(theme.SystemIcon+" ", "Model:", theme.SuccessColor.Sprint(info.ModelName), theme.ColorizeLabel)

	// Stream 模式
	streamStatus := "off"
	if info.IsStream {
		streamStatus = theme.SuccessColor.Sprint("on")
	} else {
		streamStatus = theme.Dimmed(streamStatus)
	}
	printSessionInfoRow(theme.SystemIcon+" ", "Stream:", streamStatus, theme.ColorizeLabel)

	// 推理模型标识
	if info.IsReasoningModel {
		printSessionInfoRow(theme.SystemIcon+" ", "Type:", theme.WarningColor.Sprint("推理模型 (禁用 temperature)"), theme.ColorizeLabel)
	}

	PrintThickSeparator()
	PrintEmptyLine()

	return true
}

func sessionInfoChildPrefix(theme *Theme) string {
	return strings.Repeat(" ", DisplayWidth(theme.SystemIcon+" "))
}

func printSessionInfoRow(prefix, label, value string, colorizeLabel func(string) string) {
	if colorizeLabel == nil {
		colorizeLabel = func(text string) string { return text }
	}
	labelText := fmt.Sprintf("%-*s", sessionInfoLabelWidth, label)
	fmt.Printf("%s%s %s\n", prefix, colorizeLabel(labelText), value)
}

// PrintStatus 打印状态信息
func PrintStatus(label, value string) {
	theme := GetTheme(ThemeAuto)
	fmt.Printf("%-15s %s\n", theme.ColorizeLabel(label+":"), theme.ColorizeSecondary(value))
}

// PrintStatusColored 打印带颜色的状态信息
func PrintStatusColored(label, value string, colorFunc func(string) string) {
	theme := GetTheme(ThemeAuto)
	fmt.Printf("%-15s %s\n", theme.ColorizeLabel(label+":"), colorFunc(value))
}

// PrintTable 打印表格
func PrintTable(headers []string, rows [][]string) {
	if len(headers) == 0 || len(rows) == 0 {
		return
	}

	theme := GetTheme(ThemeAuto)

	// 计算每列宽度
	colCount := len(headers)
	colWidths := make([]int, colCount)

	// 先从表头计算宽度
	for i, header := range headers {
		colWidths[i] = len(header)
	}

	// 再从每行数据更新宽度
	for _, row := range rows {
		for i, cell := range row {
			if len(cell) > colWidths[i] {
				colWidths[i] = len(cell)
			}
		}
	}

	// 打印表头
	var headerParts []string
	for i, header := range headers {
		headerParts = append(headerParts, theme.ColorizeLabel(fmt.Sprintf("%-*s", colWidths[i], header)))
	}
	fmt.Println(strings.Join(headerParts, "  "))

	// 打印分隔线
	var separatorParts []string
	for _, width := range colWidths {
		separatorParts = append(separatorParts, theme.SeparatorColor.Sprint(strings.Repeat("-", width)))
	}
	fmt.Println(strings.Join(separatorParts, "  "))

	// 打印数据行
	for _, row := range rows {
		var cellParts []string
		for i, cell := range row {
			cellParts = append(cellParts, fmt.Sprintf("%-*s", colWidths[i], cell))
		}
		fmt.Println(strings.Join(cellParts, "  "))
	}
}

// PrintKeyValue 打印键值对
func PrintKeyValue(key, value string) {
	theme := GetTheme(ThemeAuto)
	fmt.Printf("%s: %s\n", theme.ColorizeLabel(key), theme.ColorizeSecondary(value))
}

// PrintKeyValues 打印多个键值对
func PrintKeyValues(pairs map[string]string) {
	theme := GetTheme(ThemeAuto)

	maxKeyLen := 0
	for key := range pairs {
		if len(key) > maxKeyLen {
			maxKeyLen = len(key)
		}
	}

	for key, value := range pairs {
		fmt.Printf("%-*s: %s\n", maxKeyLen, theme.ColorizeLabel(key), theme.ColorizeSecondary(value))
	}
}

// PrintUsageInfo 打印使用信息
func PrintUsageInfo(promptTokens, completionTokens, totalTokens int64, duration int64) {
	theme := GetTheme(ThemeAuto)

	fmt.Println()
	PrintSeparator()

	if totalTokens > 0 {
		fmt.Printf("%s %s %s\n",
			theme.InfoIcon,
			theme.ColorizeLabel("Total Tokens:"),
			theme.SuccessColor.Sprintf("%d", totalTokens))
	}

	if promptTokens > 0 {
		fmt.Printf("%s %s %d\n",
			theme.InfoIcon,
			theme.ColorizeLabel("  Prompt:"),
			promptTokens)
	}

	if completionTokens > 0 {
		fmt.Printf("%s %s %d\n",
			theme.InfoIcon,
			theme.ColorizeLabel("Completion:"),
			completionTokens)
	}

	if duration > 0 {
		seconds := float64(duration) / 1000.0
		fmt.Printf("%s %s %.2f%s\n",
			theme.InfoIcon,
			theme.ColorizeLabel("Duration:"),
			seconds,
			theme.Dimmed("s"))
	}

	PrintSeparator()
	fmt.Println()
}

// PrintConfig 打印配置信息
func PrintConfig(config map[string]interface{}) {
	PrintSection("当前配置")

	for key, value := range config {
		var valueStr string
		switch v := value.(type) {
		case string:
			valueStr = v
		case bool:
			valueStr = fmt.Sprintf("%v", v)
		case int, int64:
			valueStr = fmt.Sprintf("%d", v)
		case float64:
			valueStr = fmt.Sprintf("%.2f", v)
		default:
			valueStr = fmt.Sprintf("%v", v)
		}
		PrintKeyValue(key, valueStr)
	}

	PrintEmptyLine()
}
