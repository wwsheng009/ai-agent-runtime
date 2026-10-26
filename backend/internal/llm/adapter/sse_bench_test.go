package adapter

import (
	"strings"
	"testing"
)

func benchSSEStream(chunks int) string {
	var b strings.Builder
	for i := 0; i < chunks; i++ {
		b.WriteString("data: {\"choices\":[{\"delta\":{\"content\":\"tok\"}}]}\n\n")
	}
	b.WriteString("data: [DONE]\n\n")
	return b.String()
}

func BenchmarkScanSSEFrames(b *testing.B) {
	stream := benchSSEStream(50)
	b.ReportAllocs()
	b.SetBytes(int64(len(stream)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		r := strings.NewReader(stream)
		_ = ScanSSEFrames(r, func(SSEFrame) (bool, error) { return true, nil })
	}
}

func BenchmarkScanSSEFrames_Large(b *testing.B) {
	stream := benchSSEStream(2000)
	b.ReportAllocs()
	b.SetBytes(int64(len(stream)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		r := strings.NewReader(stream)
		_ = ScanSSEFrames(r, func(SSEFrame) (bool, error) { return true, nil })
	}
}
