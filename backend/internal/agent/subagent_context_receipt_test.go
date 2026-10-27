package agent

import (
	"testing"

	"github.com/stretchr/testify/require"

	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

// TestForkSnapshotHashStableAndSensitive 锁定 §6.4/§6.5：fork 快照指纹对同一
// 冻结快照稳定、对内容变化敏感；无快照返回空串（未 fork 与空快照分开表达）。
func TestForkSnapshotHashStableAndSensitive(t *testing.T) {
	snapshot := forkFixture()
	first := forkSnapshotHash(snapshot)
	if first == "" || len(first) != 16 {
		t.Fatalf("hash must be non-empty 16-hex, got %q", first)
	}
	// 深拷贝等价内容 → 同一指纹（快照一旦冻结，父会话后续改写不影响）。
	cloned := make([]types.Message, 0, len(snapshot))
	for _, message := range snapshot {
		cloned = append(cloned, *message.Clone())
	}
	if again := forkSnapshotHash(cloned); again != first {
		t.Fatalf("same content must hash identically: %q vs %q", first, again)
	}
	mutated := make([]types.Message, len(cloned))
	copy(mutated, cloned)
	mutated[len(mutated)-1].Content += " mutated"
	if changed := forkSnapshotHash(mutated); changed == first {
		t.Fatal("mutated content must change the fingerprint")
	}
	if empty := forkSnapshotHash(nil); empty != "" {
		t.Fatalf("empty snapshot must hash to empty, got %q", empty)
	}
}

// TestSubagentContextReceipt 锁定 §6.5：requested/effective 分离 + 注入事实。
func TestSubagentContextReceipt(t *testing.T) {
	freshTask := SubagentTask{ID: "t-fresh"}
	receipt := subagentContextReceipt(freshTask, 0)
	if receipt["requested_context"] != "fresh" || receipt["effective_context"] != "fresh" {
		t.Fatalf("fresh task receipt: %+v", receipt)
	}
	if receipt["parent_history_injected"] != false {
		t.Fatalf("fresh must not claim injected history: %+v", receipt)
	}

	forkTask := SubagentTask{ID: "t-fork", Context: SubagentContextFork}
	receipt = subagentContextReceipt(forkTask, 12)
	if receipt["requested_context"] != "fork" || receipt["effective_context"] != "fork" {
		t.Fatalf("fork task receipt: %+v", receipt)
	}
	if receipt["parent_history_injected"] != true || receipt["fork_snapshot_messages"] != 12 {
		t.Fatalf("fork snapshot facts missing: %+v", receipt)
	}

	// 请求 fork 但快照为空（首轮等价 fresh）：requested 保留、effective 降为 fresh。
	receipt = subagentContextReceipt(forkTask, 0)
	if receipt["requested_context"] != "fork" || receipt["effective_context"] != "fresh" {
		t.Fatalf("empty fork snapshot must degrade to fresh effective: %+v", receipt)
	}
}

// TestEmitSubagentRouteResolvedCarriesForkReceipt 锁定 §6.5 emit 接线：fork 任务
// 的路由审计载荷携带 effective=fork、注入事实与快照指纹（同一快照跨 attempt
// 指纹一致）。
func TestEmitSubagentRouteResolvedCarriesForkReceipt(t *testing.T) {
	rig := newAttemptEventRig(t)
	scheduler := NewSubagentScheduler(rig.agent, SubagentSchedulerConfig{})
	task := SubagentTask{ID: "t-fork", Role: "explore", Context: SubagentContextFork}
	spec := ChildAgentSpec{ForkHistory: forkFixture()}
	scheduler.emitSubagentRouteResolved(SubagentRunOptions{ParentSessionID: "p-1"}, task, spec, "child-1", "explore", 1, 2)

	events := rig.snapshot()
	var receipt map[string]interface{}
	for _, event := range events {
		if event.eventType == runtimeevents.EventSubagentRouteResolved {
			receipt = event.payload
		}
	}
	require.NotNil(t, receipt, "route.resolved 必须发出")
	require.Equal(t, "fork", receipt["requested_context"])
	require.Equal(t, "fork", receipt["effective_context"])
	require.Equal(t, true, receipt["parent_history_injected"])
	require.Equal(t, len(forkFixture()), receipt["fork_snapshot_messages"])
	require.Equal(t, forkSnapshotHash(forkFixture()), receipt["fork_snapshot_hash"])
}
