package output

import (
	"context"
	"fmt"
	"strings"
)

// TextReducer 是 P1 的默认 reducer，负责做确定性裁剪。
type TextReducer struct {
	MaxChars int
	MaxLines int
}

// NewTextReducer 创建一个基础文本 reducer。
func NewTextReducer(maxChars, maxLines int) *TextReducer {
	if maxChars <= 0 {
		maxChars = 1200
	}
	if maxLines <= 0 {
		maxLines = 16
	}

	return &TextReducer{
		MaxChars: maxChars,
		MaxLines: maxLines,
	}
}

// Name 返回 reducer 名称。
func (r *TextReducer) Name() string {
	return "text_truncation"
}

// Reduce 对原始文本做确定性裁剪。
func (r *TextReducer) Reduce(_ context.Context, input ReducedInput) (*Envelope, bool, error) {
	content := strings.TrimSpace(strings.ReplaceAll(input.Text, "\r\n", "\n"))
	if content == "" && input.Raw.Error == "" {
		return &Envelope{
			ToolName:   input.Raw.ToolName,
			ToolCallID: input.Raw.ToolCallID,
			Summary:    "Tool returned no output.",
		}, true, nil
	}

	lines := strings.Split(content, "\n")
	trimmedByLines := false
	if len(lines) > r.MaxLines {
		lines = lines[:r.MaxLines]
		trimmedByLines = true
	}

	summary := strings.TrimSpace(strings.Join(lines, "\n"))
	trimmedByChars := false
	if len(summary) > r.MaxChars {
		summary = summary[:r.MaxChars]
		trimmedByChars = true
	}
	if trimmedByChars {
		summary = strings.TrimSpace(summary) + "..."
	}

	if input.Raw.Error != "" {
		summary = strings.TrimSpace(summary)
		if summary == "" {
			summary = fmt.Sprintf("Tool %s failed before producing output.", input.Raw.ToolName)
		}
	}

	metadata := map[string]interface{}{
		"trimmed_lines": trimmedByLines,
		"trimmed_chars": trimmedByChars,
	}

	if input.ByteCount > 0 {
		metadata["raw_bytes"] = input.ByteCount
	}

	return &Envelope{
		ToolName:   input.Raw.ToolName,
		ToolCallID: input.Raw.ToolCallID,
		Summary:    summary,
		Error:      strings.TrimSpace(input.Raw.Error),
		Metadata:   metadata,
	}, true, nil
}
