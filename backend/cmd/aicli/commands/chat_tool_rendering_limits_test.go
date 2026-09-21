package commands

import "testing"

// TestSharedChatToolResultPreviewLimitsStayUsable guards the relaxed tool-result
// display budget. The previous 3-line / 360-byte cap folded nearly every real
// grep/view/shell result, so the omission suffix became the most prominent line
// of the block and the transcript read as "tool output is always truncated".
func TestSharedChatToolResultPreviewLimitsStayUsable(t *testing.T) {
	maxLines, maxBytes := sharedChatToolResultPreviewLimits("shell")
	if maxLines < 8 || maxBytes < 2048 {
		t.Fatalf("default tool preview limits are too tight: lines=%d bytes=%d", maxLines, maxBytes)
	}

	todoLines, todoBytes := sharedChatToolResultPreviewLimits("todos")
	if todoLines < maxLines || todoBytes < maxBytes {
		t.Fatalf("todos must not be narrower than the default: lines=%d bytes=%d", todoLines, todoBytes)
	}
}
