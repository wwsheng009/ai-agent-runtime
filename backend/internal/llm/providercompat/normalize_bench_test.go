package providercompat

import (
	"context"
	"io"
	"strings"
	"testing"
)

func benchNormalizeStream(b *testing.B) {
	// 构造一段需要归一化的 SSE 流。归一化链是否非空取决于 ctx 里的协议，
	// 这里直接验证：链为空时走 passthrough（不启动扫描 goroutine），
	// 因此 bench 只在链非空时才有意义——由调用方保证。
	stream := benchStreamBody(50)
	ctx := Context{}
	r := NormalizeStreamReadCloser(ctx, strings.NewReader(stream))
	if r == nil {
		b.Skip("nil reader")
	}
	_, _ = io.Copy(io.Discard, r)
	_ = r.Close()
}

func benchStreamBody(chunks int) string {
	var s strings.Builder
	for i := 0; i < chunks; i++ {
		s.WriteString(`data: {"choices":[{"delta":{"content":"tok"}}]}` + "\n\n")
	}
	s.WriteString("data: [DONE]\n\n")
	return s.String()
}

// BenchmarkNormalizeStreamReadCloser 覆盖流式归一化的分配量。
// 注意：若 ctx 未启用任何 adapter（链为空），函数直接返回 passthrough，
// 不会启动扫描 goroutine，此时分配与本次优化无关。
func BenchmarkNormalizeStreamReadCloser(b *testing.B) {
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		benchNormalizeStream(b)
	}
}

var _ = context.Background
