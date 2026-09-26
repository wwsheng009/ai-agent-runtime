package commands

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
	"github.com/wwsheng009/ai-agent-runtime/internal/subagentbatch"
	"github.com/wwsheng009/ai-agent-runtime/internal/supervision"
)

// TestLocalHostWiresResumeDelivery 守护 G2 的宿主接线（缺口就是"能力在包内、
// 宿主没接线"）：控制面就绪的 CLI 宿主必须给 wake consumer 装上 DeliverResume，
// 否则 resume 上下文永远到不了父 turn 的 prompt，resume 只能退回 legacy
// AutoWakePrompt（只在 digest 里"去看摘要"，不带 rollup / I1 判据）。
func TestLocalHostWiresResumeDelivery(t *testing.T) {
	host := newLocalSupervisionTestHost(t)
	host.ActorRegistry = newLocalActorRegistry(host)
	host.wireLocalSupervisionWakeConsumer()
	require.NotNil(t, host.supervisionWake, "控制面就绪时必须装配 wake consumer")
	require.NotNil(t, host.supervisionWake.DeliverResume,
		"G2：宿主必须接 DeliverResume，否则 ResumePrompt 永远投不出去")
	require.NotNil(t, host.supervisionWake.Deliver,
		"legacy Deliver 必须保留（未接线 resume 的宿主/降级路径仍走它）")
	require.NotNil(t, host.supervisionWake.Announce,
		"G3：宿主必须接 Announce，否则挂起 → 恢复在 UI/审计面没有闭环信号")
}

// TestLocalTurnResumedEventPayload 钉住 G3 的 CLI 播报面：supervision 的
// host-neutral 播报必须被翻成宿主总线上的 turn.resumed（A+D 契约），
// 且键名与 supervision.EventPayload 的契约一致（前端/审计按同一份键消费）。
func TestLocalTurnResumedEventPayload(t *testing.T) {
	bus := runtimeevents.NewBus()
	var resumed []runtimeevents.Event
	bus.Subscribe(runtimeevents.EventTurnResumed, func(event runtimeevents.Event) {
		resumed = append(resumed, event)
	})
	host := &localChatRuntimeHost{EventBus: bus}

	host.publishLocalTurnResumed(supervision.ResumeAnnouncement{
		TurnID:          "turn-resume-1",
		ParentSessionID: "sess-resume-1",
		RootScopeID:     "sess-resume-1",
		Trigger:         supervision.ResumeTriggerTerminal,
		WakeReasons:     []string{supervision.WakeReasonLifecycleFailed},
		WakeIDs:         []string{"wake-1"},
		PendingCount:    0,
		Status:          "failed",
		Terminal:        true,
		Summary:         "batch_resume_terminal 失败",
	})

	require.Len(t, resumed, 1)
	event := resumed[0]
	require.Equal(t, runtimeevents.EventTurnResumed, event.Type)
	require.Equal(t, "sess-resume-1", event.SessionID)
	require.Equal(t, "turn-resume-1", event.Payload["turn_id"])
	require.Equal(t, supervision.ResumeTriggerTerminal, event.Payload["trigger"])
	require.Equal(t, 0, event.Payload["pending_count"])
	require.Equal(t, true, event.Payload["terminal"])
	require.Equal(t, []string{"lifecycle_failed"}, event.Payload["wake_reasons"])
	require.Equal(t, "batch_resume_terminal 失败", event.Payload["summary"])

	// 空宿主/空总线是 no-op（best-effort 观察者不得 panic 或污染投递路径）。
	(&localChatRuntimeHost{}).publishLocalTurnResumed(supervision.ResumeAnnouncement{})
}

// TestLocalSupervisionSourcesBuildResumeContextWithVerdict 钉住 G2 的数据面：
// 宿主把 durable batch 账本接成 wake scheduler 的 obligation source 后，同一 turn
// 的 resume 上下文必须给出 pending_count（I1 收尾判据）、终态 rollup 与 turn 锚点，
// 而不是 pending=-1 / status=unknown 的降级上下文（AC-P3-1c 的输入面）。
func TestLocalSupervisionSourcesBuildResumeContextWithVerdict(t *testing.T) {
	ctx := context.Background()
	host := newLocalSupervisionTestHost(t)
	host.SubagentBatches = newTestSubagentBatchStore(t)

	const parentSessionID = "resume-parent"
	now := time.Now().UTC()
	batch := &subagentbatch.SubagentBatch{
		BatchID:         "batch_resume_terminal",
		RootScopeID:     parentSessionID,
		ParentSessionID: parentSessionID,
		ParentTurnID:    "turn_parked",
		ExecutionMode:   subagentbatch.ExecutionModeBackground,
		Status:          subagentbatch.BatchFailed,
		TaskCount:       1,
		FailedCount:     1,
		FinishedAt:      &now,
		HeartbeatAt:     now,
		CreatedAt:       now,
		UpdatedAt:       now,
		Version:         1,
	}
	created, err := host.SubagentBatches.CreateBatch(ctx, batch, nil)
	require.NoError(t, err)
	require.True(t, created)

	// 接线前：没有账本投影 ⇒ 必须显式降级（pending 未知），而不是假装账本为空。
	degraded := host.Supervision.Wakes.BuildResumeContext(ctx, supervision.ResumeContextRequest{
		ParentSessionID: parentSessionID,
		RootScopeID:     parentSessionID,
	})
	require.NotNil(t, degraded)
	require.Equal(t, -1, degraded.PendingCount, "未接线时必须如实报告 unknown")
	require.Equal(t, "unknown", degraded.Status)

	host.wireLocalSupervisionSources()

	resume := host.Supervision.Wakes.BuildResumeContext(ctx, supervision.ResumeContextRequest{
		ParentSessionID: parentSessionID,
		RootScopeID:     parentSessionID,
	})
	require.Equal(t, 0, resume.PendingCount, "全终态账本 ⇒ pending_count=0（I1 放行收尾）")
	require.True(t, resume.Terminal)
	require.Equal(t, "turn_parked", resume.TurnID, "resume 必须锚定挂起 turn（I3）")
	require.Contains(t, resume.Text, "batch_resume_terminal")

	prompt := supervision.AutoWakePromptFor(resume)
	require.Contains(t, prompt, "[supervision] resume", "投递的 prompt 必须是 resume 文本，不是 legacy 提示")
	require.Contains(t, prompt, "turn_id=turn_parked")
	require.Contains(t, prompt, "所有 obligation 均已终态：请直接产出终局报告")
	require.Contains(t, prompt, "batch_resume_terminal")
}
