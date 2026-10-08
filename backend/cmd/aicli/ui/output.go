package ui

import (
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/render"
)

// TruncateVisible truncates by terminal cell width.
func TruncateVisible(text string, maxWidth int, marker string) string {
	return render.TruncateText(text, maxWidth, marker)
}
