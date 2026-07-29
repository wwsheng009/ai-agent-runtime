package markdown

import (
	"strings"
	"testing"
)

func BenchmarkRenderMarkdown100KiB(b *testing.B) {
	line := "- item with 中文 text and **bold** plus `inline code` for wrapping\n"
	source := strings.Repeat(line, 100*1024/len(line)+1)
	opts := testOpts(80)
	b.ReportAllocs()
	b.SetBytes(int64(len(source)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = Render(source, opts)
	}
}

func BenchmarkRenderCode100KiB(b *testing.B) {
	line := "func renderValue(value string) { println(value) }\n"
	code := strings.Repeat(line, 100*1024/len(line)+1)
	source := "```go\n" + code + "```\n"
	opts := testOpts(100)
	b.ReportAllocs()
	b.SetBytes(int64(len(source)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = Render(source, opts)
	}
}
