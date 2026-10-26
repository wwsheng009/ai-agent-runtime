package adapter

import (
	"strings"
	"testing"
)

func benchAnthropicStream(chunks int) string {
	var b strings.Builder
	for i := 0; i < chunks; i++ {
		b.WriteString("event: content_block_delta\n")
		b.WriteString(`data: {"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"tok"}}` + "\n\n")
	}
	b.WriteString("event: message_delta\n")
	b.WriteString(`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"}}` + "\n\n")
	b.WriteString("event: message_stop\n")
	b.WriteString(`data: {"type":"message_stop"}` + "\n\n")
	return b.String()
}

func benchGeminiStream(chunks int) string {
	var b strings.Builder
	for i := 0; i < chunks; i++ {
		b.WriteString(`data: {"candidates":[{"content":{"parts":[{"text":"tok"}]}}]}` + "\n\n")
	}
	b.WriteString(`data: {"candidates":[{"finishReason":"STOP"}]}` + "\n\n")
	return b.String()
}

func BenchmarkAnthropicHandleResponseStream(b *testing.B) {
	stream := benchAnthropicStream(50)
	a := &AnthropicAdapter{}
	b.ReportAllocs()
	b.SetBytes(int64(len(stream)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, err := a.HandleResponse(true, strings.NewReader(stream), StreamCallbacks{})
		if err != nil {
			b.Fatalf("HandleResponse: %v", err)
		}
	}
}

func BenchmarkGeminiHandleResponseStream(b *testing.B) {
	stream := benchGeminiStream(50)
	a := &GeminiAdapter{}
	b.ReportAllocs()
	b.SetBytes(int64(len(stream)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, err := a.HandleResponse(true, strings.NewReader(stream), StreamCallbacks{})
		if err != nil {
			b.Fatalf("HandleResponse: %v", err)
		}
	}
}
