package agent

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
	"github.com/wwsheng009/ai-agent-runtime/internal/llm"
	"github.com/wwsheng009/ai-agent-runtime/internal/subagentbatch"
)

// TestDurableParkAnnouncesTurnSuspendedOnce 守护方案 §6.8 / 审计 G3 的挂起可观测性：
// durable 宿主**首次**为某个 turn 写挂起记录时必须发一次 turn.suspended（携带
// obligation 计数、resume 队列长度与 batch id），同一 turn 的重复挂起（崩溃重试 /
// 同 turn 第二个批次）不得重复播报——这是**边沿**事件，不是每批次的流水。
//
// 降级路径的反面契约（零 turn.suspended）由 suspension_gate_test.go 的
// assertDegradedSuspensionDispatch 覆盖：本用例只钉 durable 路径的正向信号。
func TestDurableParkAnnouncesTurnSuspendedOnce(t *testing.T) {
	ctx := context.Background()
	store, err := subagentbatch.NewSQLiteBatchStore(&subagentbatch.StoreConfig{
		Path: filepath.Join(t.TempDir(), "batches.db"),
	})
	require.NoError(t, err)
	require.True(t, store.IsDurable())
	t.Cleanup(func() { _ = store.Close() })

	apiAgent := newSuspensionGateAgent(t, nil)
	apiAgent.SetSubagentBatchCoordinator(&SubagentBatchCoordinator{store: store})

	bus := runtimeevents.NewBus()
	var mu sync.Mutex
	var suspended []runtimeevents.Event
	bus.Subscribe(runtimeevents.EventTurnSuspended, func(event runtimeevents.Event) {
		mu.Lock()
		suspended = append(suspended, event)
		mu.Unlock()
	})
	apiAgent.SetEventBus(bus)

	now := time.Now().UTC()
	batch := &subagentbatch.SubagentBatch{
		BatchID:         "batch_park_edge",
		RootScopeID:     "sess-park-edge",
		ParentSessionID: "sess-park-edge",
		ExecutionMode:   subagentbatch.ExecutionModeBackground,
		Status:          subagentbatch.BatchRunning,
		HeartbeatAt:     now,
		CreatedAt:       now,
		UpdatedAt:       now,
		Version:         1,
	}
	created, err := store.CreateBatch(ctx, batch, []subagentbatch.SubagentTaskRecord{
		{TaskID: "t1", BatchID: batch.BatchID, OrderIndex: 1, UpdatedAt: now},
	})
	require.NoError(t, err)
	require.True(t, created)

	loop := NewReActLoop(apiAgent, llm.NewLLMRuntime(nil), &LoopReActConfig{})
	loop.turnID = "turn-park-edge"

	loop.parkBackgroundTurn(ctx, batch, "sess-park-edge")
	// 幂等重挂（崩溃重试 / 同 turn 第二个批次）：记录已存在 ⇒ 不重复播报。
	loop.parkBackgroundTurn(ctx, batch, "sess-park-edge")

	mu.Lock()
	defer mu.Unlock()
	require.Len(t, suspended, 1, "turn.suspended 是边沿事件：同一 turn 只播报一次")
	event := suspended[0]
	require.Equal(t, runtimeevents.EventTurnSuspended, event.Type)
	require.Equal(t, "sess-park-edge", event.SessionID)
	require.Equal(t, "turn-park-edge", event.Payload["turn_id"], "载荷必须锚定挂起的 turn（I3）")
	require.Equal(t, "batch_park_edge", event.Payload["batch_id"])
	require.Equal(t, 2, event.Payload["obligation_count"], "batch 行 + 任务行都要计入 obligation")
	require.Equal(t, 1, event.Payload["resume_queue_count"])
	require.NotEmpty(t, event.Payload["parked_at"])
}

// TestSettleParkedTurnAnnouncesResumeClosure 守护 G3 的**闭合半边**（2026-09-26
// 真机 E2E 复现的缺口）：后台批次在父 turn 结束前就全部终态 ⇒ 结清路径清掉挂起
// 记录，但这辈子不会再有 resume episode。若这里不发闭合事件，消费者只见过
// turn.suspended、永远等不到 turn.resumed，而前端契约是"只有 turn.resumed 清除
// 挂起态" ⇒ "托管中"横幅永久卡住。因此结清成功必须补发一次 trigger=settled 的
// turn.resumed；未结清 / 重复结清都不得播报。
func TestSettleParkedTurnAnnouncesResumeClosure(t *testing.T) {
	ctx := context.Background()
	store, err := subagentbatch.NewSQLiteBatchStore(&subagentbatch.StoreConfig{
		Path: filepath.Join(t.TempDir(), "batches.db"),
	})
	require.NoError(t, err)
	require.True(t, store.IsDurable())
	t.Cleanup(func() { _ = store.Close() })

	ag := NewAgentWithLLM(&Config{Name: "settle-announce-agent", Model: "gpt-4"}, nil, llm.NewLLMRuntime(nil))
	ag.SetSubagentBatchCoordinator(NewSubagentBatchCoordinator(SubagentBatchCoordinatorConfig{Store: store}))

	bus := runtimeevents.NewBus()
	var mu sync.Mutex
	var resumed []runtimeevents.Event
	bus.Subscribe(runtimeevents.EventTurnResumed, func(event runtimeevents.Event) {
		mu.Lock()
		resumed = append(resumed, event)
		mu.Unlock()
	})
	ag.SetEventBus(bus)

	loop := &ReActLoop{agent: ag}
	loop.turnID = "turn-settle-announce"
	parkAgentSettleTurn(t, store, "turn-settle-announce", "batch-settled")
	seedAgentSettleBatch(t, store, "batch-settled", subagentbatch.BatchRunning)

	// 义务未终态：结清是 no-op，不得播报"已闭合"。
	loop.settleParkedTurnOnRunEnd(ctx, "sess-1", "turn-settle-announce")
	mu.Lock()
	require.Empty(t, resumed, "未结清的挂起不得播报闭合事件")
	mu.Unlock()

	_, err = store.UpdateBatch(ctx, "batch-settled", -1, func(batch *subagentbatch.SubagentBatch) {
		batch.Status = subagentbatch.BatchCompleted
	})
	require.NoError(t, err)
	loop.settleParkedTurnOnRunEnd(ctx, "sess-1", "turn-settle-announce")

	mu.Lock()
	defer mu.Unlock()
	require.Len(t, resumed, 1, "结清是边沿：同一 turn 只播报一次")
	event := resumed[0]
	require.Equal(t, runtimeevents.EventTurnResumed, event.Type)
	require.Equal(t, "sess-1", event.SessionID)
	require.Equal(t, "turn-settle-announce", event.Payload["turn_id"], "闭合事件必须锚定被结清的 turn（I3）")
	require.Equal(t, runtimeevents.TurnResumedTriggerSettled, event.Payload["trigger"])
	require.Equal(t, true, event.Payload["settled"])
	require.Equal(t, 2, event.Payload["obligation_count"])

	// 记录已清：再次调用不得重复播报，也不得把别家 turn 的结清算到这里。
	loop.settleParkedTurnOnRunEnd(ctx, "sess-1", "turn-settle-announce")
	require.Len(t, resumed, 1)
}
