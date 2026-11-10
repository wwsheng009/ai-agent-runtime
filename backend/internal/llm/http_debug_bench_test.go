package llm

import (
	"context"
	"testing"
)

func benchDebugRequestBody() map[string]interface{} {
	messages := make([]interface{}, 0, 40)
	for i := 0; i < 40; i++ {
		messages = append(messages, map[string]interface{}{
			"role":    "user",
			"content": "这是一条用于基准测试的消息内容，长度适中以模拟真实会话历史。",
		})
	}
	tools := make([]interface{}, 0, 12)
	for i := 0; i < 12; i++ {
		tools = append(tools, map[string]interface{}{
			"type": "function",
			"function": map[string]interface{}{
				"name":        "tool_" + string(rune('a'+i%26)),
				"description": "benchmark tool description",
				"parameters":  map[string]interface{}{"type": "object"},
			},
		})
	}
	return map[string]interface{}{
		"model":        "bench-model",
		"messages":     messages,
		"tools":        tools,
		"instructions": "benchmark system instructions",
	}
}

// BenchmarkBuildHTTPDebugRequestMetadata_NoReporter 覆盖无监听者时的开销。
// 优化前这里会对整个请求体做至多 6 次 JSON 编码，即使事件注定被丢弃。
func BenchmarkBuildHTTPDebugRequestMetadata_NoReporter(b *testing.B) {
	body := benchDebugRequestBody()
	ctx := context.Background() // 无 reporter
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = buildHTTPDebugRequestMetadataActive(ctx, nil, "openai", "bench", body)
	}
}

// BenchmarkBuildHTTPDebugRequestMetadata_WithReporter 是有监听者的对照组：
// 该路径必须保留完整指纹计算。
func BenchmarkBuildHTTPDebugRequestMetadata_WithReporter(b *testing.B) {
	body := benchDebugRequestBody()
	ctx := WithHTTPDebugReporter(context.Background(), func(HTTPDebugEvent) {})
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = buildHTTPDebugRequestMetadataActive(ctx, nil, "openai", "bench", body)
	}
}

// BenchmarkBuildHTTPDebugRequestMetadata_WithReporterBody 覆盖生产路径：
// 请求体已经序列化过一次，诊断指纹直接哈希字段原始字节，不再重新编码
// messages/tools/instructions。
func BenchmarkBuildHTTPDebugRequestMetadata_WithReporterBody(b *testing.B) {
	body := benchDebugRequestBody()
	bodyBytes, err := MarshalProviderRequestBody(body)
	if err != nil {
		b.Fatal(err)
	}
	ctx := WithHTTPDebugReporter(context.Background(), func(HTTPDebugEvent) {})
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = buildHTTPDebugRequestMetadataActiveWithBody(ctx, nil, "openai", "bench", body, bodyBytes)
	}
}

func BenchmarkBuildHTTPDebugRequestMetadata_WithReporterLargeBody(b *testing.B) {
	body := largeProviderRequestBody()
	ctx := WithHTTPDebugReporter(context.Background(), func(HTTPDebugEvent) {})
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = buildHTTPDebugRequestMetadataActive(ctx, nil, "openai", "bench", body)
	}
}

func BenchmarkBuildHTTPDebugRequestMetadata_WithReusedLargeBody(b *testing.B) {
	body := largeProviderRequestBody()
	bodyBytes, err := MarshalProviderRequestBody(body)
	if err != nil {
		b.Fatal(err)
	}
	ctx := WithHTTPDebugReporter(context.Background(), func(HTTPDebugEvent) {})
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = buildHTTPDebugRequestMetadataActiveWithBody(ctx, nil, "openai", "bench", body, bodyBytes)
	}
}
