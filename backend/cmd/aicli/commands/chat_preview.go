package commands

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

const (
	maxToolResultPreviewLines = 6
	maxToolResultPreviewBytes = 1024
)

// truncateOutput 截断输出内容，只保留前几行
func truncateOutput(text string, maxLines int) string {
	if text == "" {
		return ""
	}
	lines := strings.Split(text, "\n")
	if len(lines) <= maxLines {
		return text
	}
	truncated := strings.Join(lines[:maxLines], "\n")
	return fmt.Sprintf("%s\n... (已省略剩余 %d 行)", truncated, len(lines)-maxLines)
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

func truncateOutputPreview(text string, maxLines, maxBytes int) string {
	preview := truncateOutput(text, maxLines)
	if preview == "" || maxBytes <= 0 || len(preview) <= maxBytes {
		return preview
	}

	const suffix = "\n... (已省略剩余内容)"
	if len(suffix) >= maxBytes {
		return truncateUTF8Bytes(preview, maxBytes)
	}

	prefix := truncateUTF8Bytes(preview, maxBytes-len(suffix))
	if prefix == "" {
		return truncateUTF8Bytes(preview, maxBytes)
	}
	return prefix + suffix
}
