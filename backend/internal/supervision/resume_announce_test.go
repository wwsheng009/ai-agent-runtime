package supervision

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestResumeTriggerForReasons 钉住 §6.8 的触发类型收敛：terminal / progress /
// approval / other 的映射必须与 wake 预算分类同源（WakeBudgetClassOf），并且一批
// 混合原因里 terminal 优先——一次同时含失败终态与周期巡查的恢复，真正的内容是
// 失败终态，UI 不该显示成"例行巡查"。
func TestResumeTriggerForReasons(t *testing.T) {
	cases := []struct {
		name    string
		reasons []string
		want    string
	}{
		{"失败终态", []string{WakeReasonExecutionFailed}, ResumeTriggerTerminal},
		{"超时终态", []string{WakeReasonExecutionTimeout}, ResumeTriggerTerminal},
		{"宿主后缀拼写", []string{"supervision_progress_check"}, ResumeTriggerProgress},
		{"周期巡查", []string{WakeReasonProgressCheck}, ResumeTriggerProgress},
		{"审批", []string{WakeReasonApprovalRequired}, ResumeTriggerApproval},
		{"提问", []string{WakeReasonQuestionAsked}, ResumeTriggerApproval},
		{"无原因", nil, ResumeTriggerOther},
		{"未知原因", []string{"something_new"}, ResumeTriggerOther},
		{"终态优先于巡查", []string{WakeReasonProgressCheck, WakeReasonExecutionFailed}, ResumeTriggerTerminal},
		{"审批优先于巡查", []string{WakeReasonProgressCheck, WakeReasonApprovalRequired}, ResumeTriggerApproval},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, ResumeTriggerForReasons(tc.reasons))
		})
	}
}

// TestWakeConsumerAnnouncesResumeOnlyAfterDelivery 守护 G3 的投递侧契约：
//   - 宿主投递失败（wake 仍留在 durable 队列）不得播报 turn.resumed——否则 UI
//     会显示一次从未发生的恢复；
//   - 投递成功后恰好播报一次，触发类型来自 wake 原因，账本判据来自同一 turn 的
//     resume 上下文；
//   - 未接线账本投影时诚实降级（pending=-1 / status=unknown），不假装全终态；
//   - Announce 钩子 panic 不得影响投递结果（best-effort 观察者）。
func TestWakeConsumerAnnouncesResumeOnlyAfterDelivery(t *testing.T) {
	store := newTestStore(t, "wake-consumer-announce")
	ctx := context.Background()
	scheduler := NewWakeScheduler(store, WakeSchedulerConfig{})
	consumer := &WakeConsumer{
		Wakes:    scheduler,
		Runnable: func(context.Context, string, string, string) bool { return true },
	}

	var mu sync.Mutex
	var announcements []ResumeAnnouncement
	consumer.Announce = func(_ context.Context, announcement ResumeAnnouncement) {
		mu.Lock()
		announcements = append(announcements, announcement)
		mu.Unlock()
	}

	project := func(subject string) {
		t.Helper()
		_, err := ProjectLifecycle(ctx, store, scheduler, LifecycleProjection{
			RootScopeID:           "root-announce",
			TargetParentSessionID: "root-announce",
			SubjectKind:           SubjectAgentRun,
			SubjectID:             subject,
			EventType:             "execution_failed",
			Severity:              SeverityCritical,
			SupervisionState:      SupervisionTimedOut,
		})
		require.NoError(t, err)
	}

	// 1) 投递失败：不播报。
	consumer.Deliver = func(context.Context, string, string, *Digest, []string) error {
		return errors.New("parent turn never started")
	}
	project("child-fail")
	require.Error(t, consumer.MaybeWakeParent(ctx, "root-announce", "", "root-announce"))
	mu.Lock()
	require.Empty(t, announcements, "投递失败的 wake 不得播报 turn.resumed")
	mu.Unlock()

	// 2) 投递成功（resume 路径）：恰好一次播报。
	consumer.DeliverResume = func(context.Context, string, string, *Digest, []string, *ResumeContext) error {
		return nil
	}
	project("child-ok")
	require.NoError(t, consumer.MaybeWakeParent(ctx, "root-announce", "", "root-announce"))

	mu.Lock()
	require.Len(t, announcements, 1)
	announcement := announcements[0]
	mu.Unlock()
	require.Equal(t, "root-announce", announcement.ParentSessionID)
	require.Equal(t, "root-announce", announcement.RootScopeID)
	require.Equal(t, ResumeTriggerTerminal, announcement.Trigger, "execution_failed 必须归入 terminal")
	require.NotEmpty(t, announcement.WakeIDs)
	require.Equal(t, []string{"execution_failed"}, announcement.WakeReasons)
	// 未接线账本投影 ⇒ 诚实降级（pending=-1 表示未知），而不是假装全终态。
	require.Equal(t, -1, announcement.PendingCount)
	require.Equal(t, "unknown", announcement.Status)
	require.NotEmpty(t, announcement.Summary, "播报必须带 digest 摘要")

	// 3) 观察者 panic 不影响投递结果。
	consumer.Announce = func(context.Context, ResumeAnnouncement) { panic("observer exploded") }
	project("child-panic")
	require.NoError(t, consumer.MaybeWakeParent(ctx, "root-announce", "", "root-announce"))
}
