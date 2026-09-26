package supervision

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wwsheng009/ai-agent-runtime/internal/background"
)

// TestClassifyBackgroundJobEventPinsTheManagerVocabulary 钉住两侧的事件词表：
// supervision 用纯字符串常量保持包依赖干净，必须与 manager 真正发的事件名一致。
func TestClassifyBackgroundJobEventPinsTheManagerVocabulary(t *testing.T) {
	require.Equal(t, background.MonitorCheckEventType, BackgroundJobMonitorCheckEventType)

	for name, tc := range map[string]struct {
		eventType string
		family    string
	}{
		"completed":      {"completed", BackgroundJobFamilyTerminal},
		"failed":         {"failed", BackgroundJobFamilyTerminal},
		"timed out":      {"timed_out", BackgroundJobFamilyTerminal},
		"monitor check":  {background.MonitorCheckEventType, BackgroundJobFamilyMonitor},
		"monitor spaced": {"  MONITOR_CHECK ", BackgroundJobFamilyMonitor},
		"output":         {"output", BackgroundJobFamilyNone},
		"running":        {"running", BackgroundJobFamilyNone},
		"empty":          {"", BackgroundJobFamilyNone},
	} {
		require.Equal(t, tc.family, ClassifyBackgroundJobEvent(tc.eventType), name)
	}
}

func TestProjectBackgroundJobMonitorSchedulesProgressWake(t *testing.T) {
	store := newTestStore(t, "job-monitor")
	ctx := context.Background()
	scheduler := NewWakeScheduler(store, WakeSchedulerConfig{})

	notification, err := ProjectBackgroundJobMonitor(ctx, store, scheduler, BackgroundJobMonitorInput{
		RootScopeID:           "root-1",
		TargetParentSessionID: "parent-1",
		JobID:                 "job-slow",
		Status:                "running",
		Command:               "go test ./...",
		Elapsed:               92 * time.Second,
		CheckAfter:            90 * time.Second,
		MaxDuration:           5 * time.Minute,
		Epoch:                 1700000000000000000,
	})
	require.NoError(t, err)
	require.Equal(t, ResolutionUnresolved, notification.ResolutionState)
	require.Equal(t, SubjectJob, notification.SubjectKind)
	require.Equal(t, "job-slow", notification.SubjectID)
	require.Equal(t, EventBackgroundJobMonitor, notification.EventType)
	require.Equal(t, SeverityInfo, notification.Severity)
	require.Contains(t, notification.Reason, "still running after 1m32s")
	require.Contains(t, notification.Reason, "command: go test ./...")
	require.Contains(t, notification.Reason, "auto-terminate at 5m")
	require.Contains(t, notification.Reason, "task_output")

	pending, err := store.ListWakePending(ctx, WakeFilter{
		RootScopeID:           "root-1",
		TargetParentSessionID: "parent-1",
		UnclaimedOnly:         true,
	})
	require.NoError(t, err)
	require.Len(t, pending, 1, "a check must wake the owning session")
	require.Equal(t, EventBackgroundJobMonitor, pending[0].WakeReason)
	// 进度族、非 failure 预算：巡检型唤醒不能挤占真正的失败配额。
	require.Equal(t, WakeBudgetClassOther, WakeBudgetClassOf(pending[0].WakeReason))

	// 同一个 check epoch 重放不产生第二条 wake。
	replayed, err := ProjectBackgroundJobMonitor(ctx, store, scheduler, BackgroundJobMonitorInput{
		RootScopeID:           "root-1",
		TargetParentSessionID: "parent-1",
		JobID:                 "job-slow",
		Status:                "running",
		Epoch:                 1700000000000000000,
	})
	require.NoError(t, err)
	require.Equal(t, notification.NotificationID, replayed.NotificationID, "a replayed check updates the same durable row")

	// 新的 check epoch（下一次巡检）落一条新的 durable 行；pending wake 按
	// (target, event_type) 合并成一条，避免连续巡检堆出 N 个待投递 turn。
	second, err := ProjectBackgroundJobMonitor(ctx, store, scheduler, BackgroundJobMonitorInput{
		RootScopeID:           "root-1",
		TargetParentSessionID: "parent-1",
		JobID:                 "job-slow",
		Status:                "pending",
		Epoch:                 1700000000000000001,
	})
	require.NoError(t, err)
	require.NotEqual(t, notification.NotificationID, second.NotificationID)
	rows, err := store.ListNotifications(ctx, NotificationFilter{RootScopeID: "root-1"})
	require.NoError(t, err)
	require.Len(t, rows, 2, "each check epoch keeps its own durable evidence")
	pending, err = store.ListWakePending(ctx, WakeFilter{
		RootScopeID:           "root-1",
		TargetParentSessionID: "parent-1",
		UnclaimedOnly:         true,
	})
	require.NoError(t, err)
	require.Len(t, pending, 1, "pending checks coalesce into one wake per (target, event type)")

	// 合并只是「还没投递」时的去重：本条被消费（resolve）后，下一次巡检仍能唤醒。
	require.NoError(t, store.ResolveWakePending(ctx, pending[0].WakeID))
	_, err = ProjectBackgroundJobMonitor(ctx, store, scheduler, BackgroundJobMonitorInput{
		RootScopeID:           "root-1",
		TargetParentSessionID: "parent-1",
		JobID:                 "job-slow",
		Status:                "running",
		Epoch:                 1700000000000000002,
	})
	require.NoError(t, err)
	pending, err = store.ListWakePending(ctx, WakeFilter{
		RootScopeID:           "root-1",
		TargetParentSessionID: "parent-1",
		UnclaimedOnly:         true,
	})
	require.NoError(t, err)
	require.Len(t, pending, 1, "a consumed check must not suppress the next one")
}

func TestProjectBackgroundJobMonitorValidatesIdentity(t *testing.T) {
	store := newTestStore(t, "job-monitor-invalid")
	ctx := context.Background()
	scheduler := NewWakeScheduler(store, WakeSchedulerConfig{})

	for name, in := range map[string]BackgroundJobMonitorInput{
		"missing job":    {RootScopeID: "root-1", TargetParentSessionID: "parent-1"},
		"missing target": {RootScopeID: "root-1", JobID: "job-1"},
		"missing root":   {TargetParentSessionID: "parent-1", JobID: "job-1"},
	} {
		_, err := ProjectBackgroundJobMonitor(ctx, store, scheduler, in)
		require.Error(t, err, name)
	}

	_, err := ProjectBackgroundJobMonitor(ctx, nil, scheduler, BackgroundJobMonitorInput{
		RootScopeID: "root-1", TargetParentSessionID: "parent-1", JobID: "job-1",
	})
	require.Error(t, err)

	rows, err := store.ListNotifications(ctx, NotificationFilter{RootScopeID: "root-1"})
	require.NoError(t, err)
	require.Empty(t, rows, "an invalid identity must not persist anything")
}
