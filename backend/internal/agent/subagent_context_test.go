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

// TestBuildForkHistoryWithWindowKeepsMostRecentCompletedMessages 锁定 §8.3：
// batch 侧 fork 窗口（task.ForkMessages，等价 direct spawn fork_turns=N）以
// 「消息条数」为单位，且窗口作用于配平后的已完成轮次。
func TestBuildForkHistoryWithWindowKeepsMostRecentCompletedMessages(t *testing.T) {
	// 配平后为 [u1 a1 u2 a2]（system 与进行中 user 均已剔除）。
	windowed, err := BuildForkHistoryWithWindow(forkFixture(), nil, 0, 2)
	if err != nil {
		t.Fatalf("windowed fork must build: %v", err)
	}
	if len(windowed) != 2 ||
		windowed[0].Content != "now check the api layer" ||
		windowed[1].Content != "the api layer is thin" {
		t.Fatalf("window must keep the most recent 2 completed messages, got %+v", windowed)
	}
	full, err := BuildForkHistory(forkFixture(), nil, 0)
	if err != nil {
		t.Fatalf("full fork must build: %v", err)
	}
	// 窗口 ≥ 快照长度时等价全量；lastMessages<=0 与 BuildForkHistory 同义。
	for _, window := range []int{0, 99} {
		got, err := BuildForkHistoryWithWindow(forkFixture(), nil, 0, window)
		if err != nil || len(got) != len(full) {
			t.Fatalf("window=%d must equal the full snapshot (%d messages), got %d (%v)", window, len(full), len(got), err)
		}
	}
}

// TestBuildForkHistoryWithWindowAppliesBeforeTokenCeiling 锁定调用序：窗口先于
// 上限校验——缩小窗口必须能让「全量超限」的父历史合法通过，否则 last:N 对大
// 历史会话永远不可用。
func TestBuildForkHistoryWithWindowAppliesBeforeTokenCeiling(t *testing.T) {
	perMessage := func(messages []types.Message) int { return len(messages) * 50 }
	if _, err := BuildForkHistory(forkFixture(), perMessage, 100); err == nil {
		t.Fatal("full snapshot (4 messages / 200 tokens) must exceed the 100-token ceiling")
	}
	windowed, err := BuildForkHistoryWithWindow(forkFixture(), perMessage, 100, 2)
	if err != nil {
		t.Fatalf("windowed snapshot (2 messages / 100 tokens) must fit the ceiling: %v", err)
	}
	if len(windowed) != 2 {
		t.Fatalf("windowed snapshot = %d messages, want 2", len(windowed))
	}
}

// TestBuildForkHistoryForTaskAppliesForkMessages 锁定 task→快照接线：窗口从任务
// 字段透传到 batch 侧唯一的快照构建入口。
func TestBuildForkHistoryForTaskAppliesForkMessages(t *testing.T) {
	task := SubagentTask{ID: "forker", Goal: "g", Context: SubagentContextFork, ForkMessages: 2}
	history, err := buildForkHistoryForTask(SubagentRunOptions{ParentHistory: forkFixture()}, task, nil, 0)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if len(history) != 2 || history[len(history)-1].Content != "the api layer is thin" {
		t.Fatalf("fork_messages=2 must keep the most recent 2 messages, got %+v", history)
	}
}
