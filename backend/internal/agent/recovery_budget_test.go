package agent

import (
	"context"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/llm"
	llmadapter "github.com/wwsheng009/ai-agent-runtime/internal/llm/adapter"
)

// TestSharedRecoveryBudgetStopsMalformedRecovery 锁定 P0-3 item 3：provider 层
// 与 agent 层共享同一份 per-run 退化恢复配额，配额被任一层耗尽后，agent 层
// 不再回注反馈（此前两层各自计数会把同一退化样本放大成十几次 HTTP 尝试）。
func TestSharedRecoveryBudgetStopsMalformedRecovery(t *testing.T) {
	loop := &ReActLoop{}
	loop.degenerateRecoveryBudget = llm.NewDegenerateRecoveryBudget(1)
	if !loop.recoveryBudget().Allow() {
		t.Fatal("fixture budget must allow one consumption")
	}

	err := &llmadapter.MalformedToolCallError{
		Kind:    "openai_stream_protocol_error",
		Code:    "invalid_tool_arguments",
		Message: "openai_stream_protocol_error: code=invalid_tool_arguments: tool call 0 (write_file) has incomplete or non-object JSON arguments",
		ToolCalls: []llmadapter.MalformedToolCall{{
			Index:     0,
			ID:        "call-bad",
			Name:      "write_file",
			Arguments: `{"path": "out.txt", "timeout": 60s}`,
		}},
	}
	if loop.tryRecoverMalformedToolCall(context.Background(), "trace-1", "session-1", 1, "goal", nil, nil, loopRunOptions{}, err) {
		t.Fatal("shared recovery budget exhaustion must stop the feedback re-prompt")
	}
	if got := loop.malformedToolCallRecoveries["write_file"]; got != 0 {
		t.Fatalf("per-tool counter must not advance after the shared budget is exhausted: %d", got)
	}
}

// TestRecoveryBudgetIsRunScopedAndStable 锁定配额的生命周期语义：同一 loop 内
// 反复取用得到同一实例（provider 与 agent 看到同一份计数），默认上限为共享常量。
func TestRecoveryBudgetIsRunScopedAndStable(t *testing.T) {
	loop := &ReActLoop{}
	first := loop.recoveryBudget()
	if first == nil {
		t.Fatal("recoveryBudget must never be nil for a non-nil loop")
	}
	if first != loop.recoveryBudget() {
		t.Fatal("recoveryBudget must return the same run-scoped instance")
	}
	if first.Max() != llm.DefaultDegenerateRecoveryBudget {
		t.Fatalf("default budget=%d want %d", first.Max(), llm.DefaultDegenerateRecoveryBudget)
	}
	var nilLoop *ReActLoop
	if nilLoop.recoveryBudget() != nil {
		t.Fatal("nil loop must report a nil budget")
	}
}
