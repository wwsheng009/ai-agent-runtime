package encoding

import (
	"strings"
	"testing"

	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
)

// reasoningDeltaForRequest 构造属于指定 request 的 reasoning 增量事件。
//
// 与同包既有 reasoningDeltaEvent（固定 turn-1/stream-1）的区别：这里显式携带
// stream_id，用于验证「不同 request 的增量缓冲互不干扰」。二者复用同一
// payload 形状（mode=append + reasoning.format=stream_delta）。
func reasoningDeltaForRequest(requestID, text string, seq uint64) runtimeevents.Event {
	return runtimeevents.Event{
		Type: "assistant.reasoning",
		Payload: map[string]interface{}{
			"turn_id":   "turn-1",
			"stream_id": requestID,
			"sequence":  seq,
			"mode":      "append",
			"reasoning": map[string]interface{}{
				"format":  "stream_delta",
				"summary": text,
			},
		},
	}
}

// TestReasoningStreamAppendsByteFaithfully 锁定增量追加的字节语义：
// 追加结果必须与逐段直接拼接完全一致，不得推断空白或合并分段。
//
// 这条不变量是 appendReasoningText 用 builder 改写后必须保持的：
// provider 可能在单词、标点、code span 或 UTF-8 rune 中间切开。
func TestReasoningStreamAppendsByteFaithfully(t *testing.T) {
	e := NewEventEncoder()
	e.Encode(llmStarted())
	// 故意包含：词中切分、标点独立、多字节 rune 切分、空行
	parts := []string{"推理", "ing", " is", " split", "-", "mid", "\n\n", "句号", "。", " end"}

	var want strings.Builder
	for i, part := range parts {
		e.Encode(reasoningDeltaEvent(part, uint64(i+1)))
		want.WriteString(part)
	}

	it := e.reasoningBy[requestKeyForTest(t, e)]
	if it == nil {
		t.Fatalf("reasoning item not created")
	}
	if it.Head != want.String() {
		t.Fatalf("Head mismatch:\n got %q\nwant %q", it.Head, want.String())
	}
}

// TestReasoningBuilderRecoversFromExternalHeadRewrite 锁定缓冲自愈：
// 若 Item.Head 被其它路径改写（快照/纠正/重放），下一次增量必须
// 以新的 Head 为基线，而不是继续用旧缓冲。
func TestReasoningBuilderRecoversFromExternalHeadRewrite(t *testing.T) {
	e := NewEventEncoder()
	e.Encode(llmStarted())
	e.Encode(reasoningDeltaEvent("AAAA", 1))
	e.Encode(reasoningDeltaEvent("BBBB", 2))

	key := requestKeyForTest(t, e)
	it := e.reasoningBy[key]
	if it == nil {
		t.Fatalf("item missing")
	}
	if it.Head != "AAAABBBB" {
		t.Fatalf("Head = %q, want AAAABBBB", it.Head)
	}

	// 模拟外部改写：缓冲仍持有旧内容，必须被检测并重建。
	it.Head = "RESET"

	e.Encode(reasoningDeltaEvent("CCCC", 3))
	if it.Head != "RESETCCCC" {
		t.Fatalf("Head after rewrite = %q, want RESETCCCC", it.Head)
	}
}

// TestReasoningBuilderIsDroppedOnSnapshot 锁定快照替换会丢弃缓冲：
// 权威快照（非 delta）整体替换正文后，后续增量必须接在快照文本之后。
func TestReasoningBuilderIsDroppedOnSnapshot(t *testing.T) {
	e := NewEventEncoder()
	e.Encode(llmStarted())
	e.Encode(reasoningDeltaEvent("draft", 1))

	key := requestKeyForTest(t, e)
	it := e.reasoningBy[key]
	if it == nil {
		t.Fatalf("item missing")
	}

	// 权威快照：mode=replace，正文整体替换（不能带 format=stream_delta，
	// 否则 ReasoningOperationForEvent 会判定为增量追加）。
	e.Encode(runtimeevents.Event{
		Type: "assistant.reasoning",
		Payload: map[string]interface{}{
			"turn_id":   "turn-1",
			"stream_id": "stream-1",
			"mode":      "replace",
			"summary":   "AUTHORITATIVE",
		},
	})
	if it.Head != "AUTHORITATIVE" {
		t.Fatalf("Head after snapshot = %q, want AUTHORITATIVE", it.Head)
	}
	if _, exists := e.reasoningBuilders[key]; exists {
		t.Fatalf("builder must be dropped after authoritative snapshot")
	}

	// 快照会重置 seq 排序状态（delete(e.reasoningOrder, key)），因此快照之后的
	// 增量必须从 seq=1 重新开始，否则会被既有乱序判定丢弃（与本优化无关）。
	e.Encode(reasoningDeltaEvent("+tail", 1))
	if it.Head != "AUTHORITATIVE+tail" {
		t.Fatalf("Head after post-snapshot delta = %q, want AUTHORITATIVE+tail", it.Head)
	}
}

// TestReasoningAppendIsLinear 是性能回归哨兵：大量小增量追加后，
// builder 的容量不应呈现平方级膨胀。
//
// 旧实现（t.Head + text）在 N 段时约产生 O(N²/2) 字节复制；
// 这里断言最终内容正确且容量保持在可控线性预算内，避免脆弱的时间断言。
func TestReasoningAppendIsLinear(t *testing.T) {
	const chunks = 4000
	e := NewEventEncoder()
	e.Encode(llmStarted())
	for i := 0; i < chunks; i++ {
		e.Encode(reasoningDeltaEvent("x", uint64(i+1)))
	}
	key := requestKeyForTest(t, e)
	it := e.reasoningBy[key]
	if it == nil {
		t.Fatalf("item missing")
	}
	if len(it.Head) != chunks {
		t.Fatalf("len(Head) = %d, want %d", len(it.Head), chunks)
	}
	builder := e.reasoningBuilders[key]
	if builder == nil {
		t.Fatalf("builder missing")
	}
	if builder.Cap() > chunks*4 {
		t.Fatalf("builder cap %d exceeds linear budget (%d)", builder.Cap(), chunks*4)
	}
}

// TestReasoningBuildersDoNotLeakAcrossRequests 确保不同 request 的缓冲互不干扰。
func TestReasoningBuildersDoNotLeakAcrossRequests(t *testing.T) {
	e := NewEventEncoder()
	e.Encode(llmStarted())
	for i := 0; i < 3; i++ {
		e.Encode(reasoningDeltaForRequest("stream-a", "A", uint64(i+1)))
		e.Encode(reasoningDeltaForRequest("stream-b", "B", uint64(i+1)))
	}
	for key, it := range e.reasoningBy {
		if it == nil {
			continue
		}
		want := ""
		switch {
		case strings.Contains(key, "stream-a"):
			want = "AAA"
		case strings.Contains(key, "stream-b"):
			want = "BBB"
		default:
			continue
		}
		if it.Head != want {
			t.Fatalf("key %q Head = %q, want %q", key, it.Head, want)
		}
	}
}

// requestKeyForTest 返回本次测试里唯一 reasoning item 的 key。
func requestKeyForTest(t *testing.T, e *EventEncoder) string {
	t.Helper()
	for key := range e.reasoningBy {
		return key
	}
	t.Fatalf("no reasoning key")
	return ""
}
