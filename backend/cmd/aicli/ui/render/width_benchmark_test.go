package render

import (
	"strings"
	"testing"
)

func BenchmarkTruncateText100KiB(b *testing.B) {
	text := strings.Repeat("structured-render-value ", 100*1024/len("structured-render-value ")+1)
	b.ReportAllocs()
	b.SetBytes(int64(len(text)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = TruncateText(text, 80, "…")
	}
}
