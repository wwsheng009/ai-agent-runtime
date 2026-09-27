package adapter

import (
	"strings"
	"testing"
)

// TestOpenAIStreamRejectsForeignChoiceIndex 锁定 P1-6 的保守断言：
// 请求从不设置 n，显式非 0 的 choice.index 属于身份不确定的候选槽，
// 必须 fail-closed 拒绝聚合，而不是把另一个槽的内容猜并进同一 StreamState。
func TestOpenAIStreamRejectsForeignChoiceIndex(t *testing.T) {
	_, err := (&OpenAIAdapter{}).HandleResponse(true, strings.NewReader(strings.Join([]string{
		`data: {"choices":[{"index":1,"delta":{"content":"wrong slot"}}]}`,
		"",
		"data: [DONE]",
		"",
	}, "\n")), StreamCallbacks{})
	if err == nil {
		t.Fatal("expected protocol error for a non-zero choice index")
	}
	if !strings.Contains(err.Error(), "unexpected_choice_index") {
		t.Fatalf("expected unexpected_choice_index, got %v", err)
	}
}

// TestOpenAIStreamRejectsSecondChoiceWithForeignIndex: 同一 chunk 里混入
// choices[1]（其 index 非 0）同样必须拒绝，不能因为 choices[0] 合法就忽略。
func TestOpenAIStreamRejectsSecondChoiceWithForeignIndex(t *testing.T) {
	_, err := (&OpenAIAdapter{}).HandleResponse(true, strings.NewReader(strings.Join([]string{
		`data: {"choices":[{"index":0,"delta":{"content":"ok"}},{"index":1,"delta":{"content":"other"}}]}`,
		"",
		"data: [DONE]",
		"",
	}, "\n")), StreamCallbacks{})
	if err == nil {
		t.Fatal("expected protocol error for a foreign second choice")
	}
	if !strings.Contains(err.Error(), "unexpected_choice_index") {
		t.Fatalf("expected unexpected_choice_index, got %v", err)
	}
}

// TestOpenAIStreamKeepsLegacyChoiceWithoutIndex: 缺省 index 的旧协议形态必须保持
// 兼容（大量中转会省略 index），不能被新的身份断言误伤。
func TestOpenAIStreamKeepsLegacyChoiceWithoutIndex(t *testing.T) {
	message, err := (&OpenAIAdapter{}).HandleResponse(true, strings.NewReader(strings.Join([]string{
		`data: {"choices":[{"delta":{"content":"legacy ok"}}]}`,
		"",
		"data: [DONE]",
		"",
	}, "\n")), StreamCallbacks{})
	if err != nil {
		t.Fatalf("legacy chunk without index must stay compatible: %v", err)
	}
	if content, _ := message["content"].(string); !strings.Contains(content, "legacy ok") {
		t.Fatalf("expected aggregated content, got %#v", message)
	}
}
