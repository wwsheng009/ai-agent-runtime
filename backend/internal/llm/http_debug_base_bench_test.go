package llm

import (
	"testing"
)

func benchDebugRequestBodyBase() map[string]interface{} {
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

// 基线：无 reporter 时的开销（旧代码路径）。
func BenchmarkBuildHTTPDebugRequestMetadata_Baseline(b *testing.B) {
	body := benchDebugRequestBodyBase()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = buildHTTPDebugRequestMetadata(nil, "openai", "bench", body)
	}
}
