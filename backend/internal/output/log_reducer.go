package output

import (
	"context"
	"fmt"
	"regexp"
	"strings"
)

var (
	logTimestampPattern = regexp.MustCompile(`^\[?\d{4}-\d{2}-\d{2}[t\s]\d{2}:\d{2}:\d{2}`)
	logLevelPattern     = regexp.MustCompile(`(?i)\b(trace|debug|info|warn(?:ing)?|error|fatal|panic)\b`)
)

// LogReducer 压缩通用日志输出。
type LogReducer struct{}

// Name 返回 reducer 名称。
func (r *LogReducer) Name() string {
	return "log_summary"
}

// Reduce 从多行日志中提取级别统计与关键告警。
func (r *LogReducer) Reduce(_ context.Context, input ReducedInput) (*Envelope, bool, error) {
	lines := normalizedNonEmptyLines(input.Text)
	if !looksLikeGenericLog(input.Raw.ToolName, lines) {
		return nil, false, nil
	}

	var errorCount, warningCount, infoCount int
	signals := make([]string, 0, 4)

	for _, line := range lines {
		lower := strings.ToLower(line)
		switch {
		case strings.Contains(lower, "panic"),
			strings.Contains(lower, "fatal"),
			strings.Contains(lower, "error"),
			strings.Contains(lower, "exception"),
			strings.Contains(lower, "failed"),
			strings.Contains(lower, "denied"),
			strings.Contains(lower, "timeout"):
			errorCount++
			signals = appendUniqueLimited(signals, summarizeLine(line, 160), 4)
		case strings.Contains(lower, "warn"):
			warningCount++
			signals = appendUniqueLimited(signals, summarizeLine(line, 160), 4)
		case strings.Contains(lower, "info"):
			infoCount++
		}
	}

	summary := fmt.Sprintf(
		"Parsed log output: %d lines, %d error-like entries, %d warnings.",
		len(lines), errorCount, warningCount,
	)
	if infoCount > 0 {
		summary += fmt.Sprintf("\nInfo entries: %d.", infoCount)
	}
	if len(signals) > 0 {
		summary += "\nSignals: " + strings.Join(signals, " | ")
	} else if len(lines) > 0 {
		summary += "\nRecent lines: " + strings.Join(limitStrings(lines, 2), " | ")
	}

	return &Envelope{
		ToolName:   input.Raw.ToolName,
		ToolCallID: input.Raw.ToolCallID,
		Summary:    summary,
		Error:      strings.TrimSpace(input.Raw.Error),
		Metadata: map[string]interface{}{
			"line_count":     len(lines),
			"error_lines":    errorCount,
			"warning_lines":  warningCount,
			"info_lines":     infoCount,
			"signal_samples": signals,
		},
	}, true, nil
}

func looksLikeGenericLog(toolName string, lines []string) bool {
	if len(lines) < 2 {
		return false
	}
	if strings.Contains(strings.ToLower(toolName), "log") {
		return true
	}

	logLike := 0
	for _, line := range lines {
		switch {
		case logTimestampPattern.MatchString(line):
			logLike++
		case logLevelPattern.MatchString(line):
			logLike++
		case strings.HasPrefix(line, "[") && strings.Contains(line, "]"):
			logLike++
		}
	}

	return logLike >= 2 && logLike*2 >= len(lines)
}
