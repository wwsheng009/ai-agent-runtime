package llm

import (
	"bytes"
	"testing"
)

// 构造一个典型的 SSE 响应体：若干 chunk + 末尾 usage 事件。
func benchSSEBody(chunks int) []byte {
	var b bytes.Buffer
	for i := 0; i < chunks; i++ {
		b.WriteString("data: {\"choices\":[{\"delta\":{\"content\":\"tok\"}}]}\n\n")
	}
	b.WriteString("data: {\"usage\":{\"prompt_tokens\":120,\"completion_tokens\":40,\"total_tokens\":160}}\n\n")
	b.WriteString("data: [DONE]\n\n")
	return b.Bytes()
}

func BenchmarkExtractUsageFromResponseBody(b *testing.B) {
	body := benchSSEBody(50)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = ExtractTokenUsageFromResponseBody(body)
	}
}

func BenchmarkExtractUsageFromResponseBody_Large(b *testing.B) {
	body := benchSSEBody(2000)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = ExtractTokenUsageFromResponseBody(body)
	}
}
