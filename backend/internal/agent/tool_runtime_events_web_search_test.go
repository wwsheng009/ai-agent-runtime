package agent

import (
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/output"
	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

// P3-1：web_search 的结构化结果必须进入 tool.completed 载荷，前端来源卡片
// 才能在前端做防御式解析（sources / has_answer / answer_queries / truncated）。
func TestToolCompletedEventPayloadPromotesWebSearchMetadata(t *testing.T) {
	sources := []map[string]interface{}{
		{"url": "https://example.com/1", "title": "示例", "snippet": "片段"},
	}
	payload := toolCompletedEventPayload(toolExecutionResult{
		Call: types.ToolCall{ID: "call-web", Name: "web_search"},
		Envelope: &output.Envelope{Metadata: map[string]interface{}{
			"sources":        sources,
			"has_answer":     true,
			"answer_queries": []string{"q1", "q2"},
			"truncated":      true,
		}},
	}, 1, "trace-web", nil)

	got, ok := payload["sources"].([]map[string]interface{})
	if !ok || len(got) != 1 || got[0]["url"] != "https://example.com/1" {
		t.Fatalf("sources not promoted: %#v", payload["sources"])
	}
	if payload["has_answer"] != true {
		t.Fatalf("has_answer not promoted: %#v", payload["has_answer"])
	}
	if queries, ok := payload["answer_queries"].([]string); !ok || len(queries) != 2 || queries[0] != "q1" {
		t.Fatalf("answer_queries not promoted: %#v", payload["answer_queries"])
	}
	if payload["truncated"] != true {
		t.Fatalf("truncated not promoted with sources: %#v", payload["truncated"])
	}
}

// 类型不符或来源缺失时不提升：畸形数据留给前端解析层拒绝，而不是在这里补形状。
func TestToolCompletedEventPayloadSkipsMalformedWebSearchMetadata(t *testing.T) {
	payload := toolCompletedEventPayload(toolExecutionResult{
		Call: types.ToolCall{ID: "call-web-bad", Name: "web_search"},
		Envelope: &output.Envelope{Metadata: map[string]interface{}{
			"sources":   "not-an-array",
			"truncated": true,
		}},
	}, 1, "trace-web-bad", nil)

	if _, present := payload["sources"]; present {
		t.Fatalf("malformed sources must not be promoted: %#v", payload["sources"])
	}
	// truncated 只在 sources 命中时提升：其他工具的 truncated 不得借 web_search 通道混入。
	if _, present := payload["truncated"]; present {
		t.Fatalf("truncated must not be promoted without sources: %#v", payload["truncated"])
	}
}
