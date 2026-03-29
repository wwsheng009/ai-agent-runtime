package output

import (
	"context"
	"fmt"
	"strings"
)

// PlaywrightSnapshotReducer 压缩 Playwright 调试和快照输出。
type PlaywrightSnapshotReducer struct{}

// Name 返回 reducer 名称。
func (r *PlaywrightSnapshotReducer) Name() string {
	return "playwright_snapshot"
}

// Reduce 从大段浏览器输出中提取错误信号。
func (r *PlaywrightSnapshotReducer) Reduce(_ context.Context, input ReducedInput) (*Envelope, bool, error) {
	if !looksLikePlaywright(input.Raw.ToolName, input.Text) {
		return nil, false, nil
	}

	consoleErrors := make([]string, 0, 3)
	failedRequests := make([]string, 0, 3)
	pageHints := make([]string, 0, 2)

	for _, line := range strings.Split(strings.ReplaceAll(input.Text, "\r\n", "\n"), "\n") {
		trimmed := strings.TrimSpace(line)
		lower := strings.ToLower(trimmed)
		if trimmed == "" {
			continue
		}

		switch {
		case strings.Contains(lower, "console error"):
			consoleErrors = appendUniqueLimited(consoleErrors, summarizeLine(trimmed, 160), 3)
		case strings.Contains(lower, "failed request"), strings.Contains(lower, "request failed"):
			failedRequests = appendUniqueLimited(failedRequests, summarizeLine(trimmed, 160), 3)
		case strings.HasPrefix(lower, "url:"), strings.HasPrefix(lower, "title:"), strings.Contains(lower, "snapshot"):
			pageHints = appendUniqueLimited(pageHints, summarizeLine(trimmed, 140), 2)
		}
	}

	summary := fmt.Sprintf(
		"Parsed Playwright output: %d console errors, %d failed requests.",
		len(consoleErrors),
		len(failedRequests),
	)
	if len(pageHints) > 0 {
		summary += "\nPage hints: " + strings.Join(pageHints, " | ")
	}
	if len(consoleErrors) > 0 {
		summary += "\nConsole errors: " + strings.Join(consoleErrors, " | ")
	}
	if len(failedRequests) > 0 {
		summary += "\nFailed requests: " + strings.Join(failedRequests, " | ")
	}
	if len(consoleErrors) == 0 && len(failedRequests) == 0 && len(pageHints) == 0 {
		summary += "\n" + summarizeLine(input.Text, 220)
	}

	return &Envelope{
		ToolName:   input.Raw.ToolName,
		ToolCallID: input.Raw.ToolCallID,
		Summary:    summary,
		Error:      strings.TrimSpace(input.Raw.Error),
		Metadata: map[string]interface{}{
			"console_errors":  len(consoleErrors),
			"failed_requests": len(failedRequests),
		},
	}, true, nil
}

func looksLikePlaywright(toolName, text string) bool {
	lowerTool := strings.ToLower(toolName)
	lowerText := strings.ToLower(text)
	if strings.Contains(lowerTool, "playwright") {
		return true
	}
	for _, marker := range []string{
		"playwright",
		"console error",
		"failed request",
		"request failed",
		"snapshot",
	} {
		if strings.Contains(lowerText, marker) {
			return true
		}
	}
	return false
}

func appendUniqueLimited(items []string, item string, limit int) []string {
	item = strings.TrimSpace(item)
	if item == "" {
		return items
	}
	for _, existing := range items {
		if existing == item {
			return items
		}
	}
	if len(items) >= limit {
		return items
	}
	return append(items, item)
}
