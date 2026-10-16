package agent

// subagent_context_test.go — fork 上下文快照测试（P2-1'）。
//
// 覆盖（对齐 v2 方案 §4 P2-1' 测试计划）：
//   - 快照边界：已完成轮次全含、进行中轮次不含（尾部 user 截断）、
//     父级 system 消息剥离、首轮 fork 等价 fresh（nil）；
//   - 冻结性：深拷贝，父级后续改写不影响快照；
//   - 超限拒绝：整体报错含实测值，绝不静默截断；
//   - 宿主未接线：fork 请求得到可操作错误而非静默 fresh。

import (
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

func forkFixture() []types.Message {
	return []types.Message{
		{Role: "system", Content: "parent system prompt"},
		{Role: "user", Content: "analyze the auth module"},
		{Role: "assistant", Content: "the auth module has three handlers"},
		{Role: "user", Content: "now check the api layer"},
		{Role: "assistant", Content: "the api layer is thin"},
		// 尾部未回复的 user（父级进行中的轮次）——必须被截掉。
		{Role: "user", Content: "in-flight question"},
	}
}

func TestBuildForkHistoryExcludesInFlightTurn(t *testing.T) {
	snapshot, err := BuildForkHistory(forkFixture(), nil, 0)
	if err != nil {
		t.Fatalf("fork snapshot must build: %v", err)
	}
	if len(snapshot) == 0 {
		t.Fatal("fixture has completed turns; snapshot must not be empty")
	}
	joined := ""
	for _, message := range snapshot {
		joined += message.Role + ":" + message.Content + "\n"
	}
	// 已完成轮次全含。
	if !strings.Contains(joined, "auth module has three handlers") ||
		!strings.Contains(joined, "api layer is thin") {
		t.Fatalf("completed turns must be included, got:\n%s", joined)
	}
	// 进行中轮次不含。
	if strings.Contains(joined, "in-flight question") {
		t.Fatal("in-flight user turn must be excluded from the balanced prefix")
	}
	// 父级 system 剥离。
	if strings.Contains(joined, "parent system prompt") {
		t.Fatal("parent system messages must be stripped from the fork snapshot")
	}
	// 角色只含 user/assistant。
	for _, message := range snapshot {
		role := strings.ToLower(message.Role)
		if role != "user" && role != "assistant" {
			t.Fatalf("snapshot must carry dialogue roles only, got %q", message.Role)
		}
	}
}

func TestBuildForkHistoryEmptyParentEquivalentToFresh(t *testing.T) {
	// 空父历史 → nil（fork 等价 fresh，DSH 首轮 fork 语义）。
	snapshot, err := BuildForkHistory(nil, nil, 0)
	if err != nil || snapshot != nil {
		t.Fatalf("empty parent must yield nil snapshot, got %v, %v", snapshot, err)
	}
	// 只有 system → 配平后为空，同样等价 fresh。
	snapshot, err = BuildForkHistory([]types.Message{{Role: "system", Content: "x"}}, nil, 0)
	if err != nil || snapshot != nil {
		t.Fatalf("system-only parent must yield nil snapshot, got %v, %v", snapshot, err)
	}
}

func TestBuildForkHistoryFreezesSnapshot(t *testing.T) {
	messages := forkFixture()
	snapshot, err := BuildForkHistory(messages, nil, 0)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	// 父级历史被外部改写（模拟父会话继续追加/修改）。
	messages[2].Content = "mutated after fork"
	if strings.Contains(snapshot[1].Content, "mutated") {
		t.Fatal("snapshot must be frozen at build time (deep copy)")
	}
}

func TestBuildForkHistoryRefusesOversize(t *testing.T) {
	// countTokens 返回固定大值 → 超限整体拒绝，报错含实测值与 next_action。
	_, err := BuildForkHistory(forkFixture(), func([]types.Message) int { return 200 }, 100)
	if err == nil {
		t.Fatal("oversize snapshot must be refused")
	}
	if !strings.Contains(err.Error(), "200 > 100") || !strings.Contains(err.Error(), "use_fresh_context") {
		t.Fatalf("oversize error must report measured size and next_action, got %v", err)
	}
}

func TestBuildForkHistoryForTaskUnwiredHostIsActionable(t *testing.T) {
	task := SubagentTask{ID: "forker", Goal: "g", Context: SubagentContextFork}
	_, err := buildForkHistoryForTask(SubagentRunOptions{}, task, nil, 0)
	if err == nil {
		t.Fatal("fork without host-provided history must fail, not silently fall back to fresh")
	}
	if !strings.Contains(err.Error(), "forker") || !strings.Contains(err.Error(), "next_action=") {
		t.Fatalf("error must name the task and carry next_action, got %v", err)
	}
	// fresh 任务不受影响。
	fresh := SubagentTask{ID: "fresh", Goal: "g"}
	history, err := buildForkHistoryForTask(SubagentRunOptions{}, fresh, nil, 0)
	if err != nil || history != nil {
		t.Fatalf("fresh task must skip fork build, got %v, %v", history, err)
	}
}

func TestBuildForkHistoryForTaskUsesParentHistory(t *testing.T) {
	task := SubagentTask{ID: "forker", Goal: "g", Context: SubagentContextFork}
	history, err := buildForkHistoryForTask(
		SubagentRunOptions{ParentHistory: forkFixture()},
		task,
		nil,
		0,
	)
	if err != nil {
		t.Fatalf("fork with host history must build: %v", err)
	}
	if len(history) == 0 {
		t.Fatal("snapshot must not be empty for a completed-turn fixture")
	}
	for _, message := range history {
		if strings.EqualFold(message.Role, "system") {
			t.Fatal("fork snapshot must not carry parent system messages")
		}
	}
}

func TestBuildForkHistoryForTaskHonorsConfiguredCeiling(t *testing.T) {
	// 部署配置的 token 上限（agents.forkMaxSnapshotTokens）透传到快照构建：
	// 超限整体拒绝且报错含实测值（不静默截断）。
	task := SubagentTask{ID: "forker", Goal: "g", Context: SubagentContextFork}
	_, err := buildForkHistoryForTask(
		SubagentRunOptions{ParentHistory: forkFixture()},
		task,
		nil,
		1, // 任意消息都超过 1 token 的估算
	)
	if err == nil {
		t.Fatal("configured ceiling must refuse an oversize snapshot")
	}
	if !strings.Contains(err.Error(), "use_fresh_context") {
		t.Fatalf("oversize error must carry next_action, got %v", err)
	}
}
