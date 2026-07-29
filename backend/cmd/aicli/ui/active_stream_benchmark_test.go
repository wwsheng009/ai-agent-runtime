package ui

import (
	"strings"
	"testing"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/motion"
)

func BenchmarkActiveMarkdownRepaint100KiB(b *testing.B) {
	line := "- item with 中文 text and enough content to exercise markdown wrapping\n"
	source := strings.Repeat(line, 100*1024/len(line)+1)
	controller := NewActiveStreamController(80, 8)
	controller.Policy = motion.NewPolicy(motion.Config{Forced: motion.ForceMode(motion.ModeOff)})
	controller.BeginAssistant("assistant")
	controller.PushAssistantDelta(source, true)
	_, _ = controller.PaintLines(time.Unix(1, 0), true)

	b.ReportAllocs()
	b.SetBytes(int64(len(source)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = controller.PaintLines(time.Unix(int64(i+2), 0), true)
	}
}

func BenchmarkActiveMarkdownInitialCode100KiB(b *testing.B) {
	line := "func renderValue(value string) { println(value) }\n"
	code := strings.Repeat(line, 100*1024/len(line)+1)
	source := "```go\n" + code + "```\n"
	b.ReportAllocs()
	b.SetBytes(int64(len(source)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		controller := NewActiveStreamController(80, 8)
		controller.Policy = motion.NewPolicy(motion.Config{Forced: motion.ForceMode(motion.ModeOff)})
		controller.BeginAssistant("assistant")
		controller.PushAssistantDelta(source, true)
		_, _ = controller.PaintLines(time.Unix(1, 0), true)
	}
}
