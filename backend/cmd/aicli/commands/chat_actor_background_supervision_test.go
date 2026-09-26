package commands

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wwsheng009/ai-agent-runtime/internal/background"
	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	"github.com/wwsheng009/ai-agent-runtime/internal/supervision"
)

func localBackgroundJobTestEvent(jobID, eventType, sessionID string, payload map[string]interface{}) background.JobEvent {
	if payload == nil {
		payload = map[string]interface{}{}
	}
	payload["job_id"] = jobID
	payload["session_id"] = sessionID
	return background.JobEvent{
		JobID:     jobID,
		Type:      eventType,
		Payload:   payload,
		CreatedAt: time.Now().UTC(),
	}
}

// TestLocalBackgroundJobRelay_ProjectsTerminalJobOnce 覆盖 CLI 宿主的接线契约：
// output 热路径同步返回且零副作用；终态事件异步投影出「恰一条」inbox 记录，
// 重放同一次终态不再新增。
func TestLocalBackgroundJobRelay_ProjectsTerminalJobOnce(t *testing.T) {
	host := newLocalSupervisionTestHost(t)
	relay := &localBackgroundJobEventRelay{}
	relay.bind(host)
	ctx := context.Background()
	sessionID := "sess-cli-1"

	// 非终态事件（output 在 manager 的热路径上）：同步返回、不产生任何记录。
	relay.handle(localBackgroundJobTestEvent("job-cli-1", "output", sessionID, map[string]interface{}{
		"status": "running",
		"chunk":  "noise",
	}))
	notifications, err := host.Supervision.Store.ListNotifications(ctx, supervision.NotificationFilter{RootScopeID: sessionID})
	require.NoError(t, err)
	require.Empty(t, notifications, "output events must not create inbox rows")

	event := localBackgroundJobTestEvent("job-cli-1", "completed", sessionID, map[string]interface{}{
		"status":    "completed",
		"exit_code": 0,
	})
	relay.handle(event)

	require.Eventually(t, func() bool {
		rows, err := host.Supervision.Store.ListNotifications(ctx, supervision.NotificationFilter{RootScopeID: sessionID})
		return err == nil && len(rows) == 1
	}, 5*time.Second, 10*time.Millisecond, "terminal job event must project exactly one inbox row")

	rows, err := host.Supervision.Store.ListNotifications(ctx, supervision.NotificationFilter{RootScopeID: sessionID})
	require.NoError(t, err)
	require.Equal(t, supervision.SubjectJob, rows[0].SubjectKind)
	require.Equal(t, "job-cli-1", rows[0].SubjectID)
	require.Equal(t, "background_job_completed", rows[0].EventType)
	require.Equal(t, sessionID, rows[0].TargetParentSessionID)
	require.Contains(t, rows[0].Reason, "exit_code=0")

	// 同一终态重放：仍是同一条记录（稳定 epoch + 通知身份）。
	relay.handle(event)
	require.Eventually(t, func() bool {
		rows, err := host.Supervision.Store.ListNotifications(ctx, supervision.NotificationFilter{RootScopeID: sessionID})
		return err == nil && len(rows) == 1
	}, time.Second, 10*time.Millisecond)
}

// TestLocalBackgroundJobRelay_SafeWithoutHostOrPlane 保证 relay 在 host 尚未
// 绑定 / 宿主没有 supervision 控制面时都是安全 no-op（manager 生命周期内可能
// 早于 host 装配，测试宿主也可能没有控制面）。
func TestLocalBackgroundJobRelay_SafeWithoutHostOrPlane(t *testing.T) {
	bare := &localBackgroundJobEventRelay{}
	require.NotPanics(t, func() {
		bare.handle(localBackgroundJobTestEvent("job-1", "failed", "sess-1", nil))
	})

	host := &localChatRuntimeHost{}
	require.NotPanics(t, func() {
		host.handleLocalBackgroundEvent(localBackgroundJobTestEvent("job-1", "completed", "sess-1", nil))
		host.projectLocalBackgroundJobTerminal(localBackgroundJobTestEvent("job-1", "completed", "sess-1", nil), "sess-1")
	})

	// 缺 job_id / session_id：直接返回，不投影。
	noIdentity := newLocalSupervisionTestHost(t)
	noIdentity.handleLocalBackgroundEvent(background.JobEvent{Type: "failed", CreatedAt: time.Now()})
	require.Never(t, func() bool {
		rows, err := noIdentity.Supervision.Store.ListNotifications(context.Background(), supervision.NotificationFilter{})
		return err == nil && len(rows) > 0
	}, 200*time.Millisecond, 20*time.Millisecond)
}

func TestLocalBackgroundJobHelpers(t *testing.T) {
	host := newLocalSupervisionTestHost(t)
	require.Equal(t, "sess-7", host.localBackgroundJobRootScope("sess-7"),
		"a host without a bound root session keeps the job session as its scope")

	epoch := localBackgroundJobEventEpoch(background.JobEvent{CreatedAt: time.Unix(1700000000, 0).UTC()})
	require.Equal(t, time.Unix(1700000000, 0).UTC().UnixNano(), epoch)
	require.Greater(t, localBackgroundJobEventEpoch(background.JobEvent{}), int64(0),
		"a zero timestamp must still yield a usable epoch")

	require.Equal(t, "137", localBackgroundJobEventString(float64(137)))
	require.Equal(t, "TOOL_TIMEOUT", localBackgroundJobEventString("TOOL_TIMEOUT"))
	require.Equal(t, "", localBackgroundJobEventString(nil))
}

// newObservedLocalBackgroundManager 造一个「终态已由 task_output wait 送达」
// 的 job：CLI 宿主通过 manager.GetJob 读到标记后不再补一次冗余唤醒。
func newObservedLocalBackgroundManager(t *testing.T, jobID string) *background.Manager {
	t.Helper()
	path := filepath.Join(t.TempDir(), "background.sqlite")
	seed, err := background.NewSQLiteStore(&background.StoreConfig{Path: path})
	require.NoError(t, err)
	require.NoError(t, seed.SaveJob(context.Background(), background.Job{
		ID:        jobID,
		SessionID: "sess-cli-1",
		Kind:      "shell",
		Status:    background.StatusCompleted,
		Command:   "go test ./...",
		CreatedAt: time.Now().UTC(),
		Metadata:  map[string]interface{}{background.MetadataTerminalObserved: true},
	}))
	require.NoError(t, seed.Close())
	manager := background.NewManager(background.Config{StorePath: path})
	t.Cleanup(func() { _ = manager.Close() })
	return manager
}

func TestProjectLocalBackgroundJobTerminal_ObservedJobSkipsTheWake(t *testing.T) {
	host := newLocalSupervisionTestHost(t)
	ctx := context.Background()
	host.Background = newObservedLocalBackgroundManager(t, "job-observed")

	host.projectLocalBackgroundJobTerminal(
		localBackgroundJobTestEvent("job-observed", "completed", "sess-cli-1", map[string]interface{}{"status": "completed"}),
		"sess-cli-1",
	)

	rows, err := host.Supervision.Store.ListNotifications(ctx, supervision.NotificationFilter{RootScopeID: "sess-cli-1"})
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Contains(t, rows[0].Reason, "command: go test ./...")
	pending, err := host.Supervision.Store.ListWakePending(ctx, supervision.WakeFilter{RootScopeID: "sess-cli-1", UnclaimedOnly: true})
	require.NoError(t, err)
	require.Empty(t, pending, "an observed terminal state must not schedule a wake")
}

// TestResolveLocalChatSessionPendingWakes 覆盖退出路径清理：本会话的待投递 wake
// 在关闭时作废，其他会话不受影响，重复调用/nil 安全。
func TestResolveLocalChatSessionPendingWakes(t *testing.T) {
	host := newLocalSupervisionTestHost(t)
	session := &ChatSession{
		LocalRuntimeHost: host,
		RuntimeSession:   &runtimechat.Session{ID: "sess-close"},
	}
	ctx := context.Background()
	scheduler := supervision.NewWakeScheduler(host.Supervision.Store, supervision.WakeSchedulerConfig{})
	_, err := scheduler.ScheduleWake(ctx, supervision.WakeRequest{
		RootScopeID:           "sess-close",
		TargetParentSessionID: "sess-close",
		WakeReason:            "background_job_completed",
	})
	require.NoError(t, err)
	_, err = scheduler.ScheduleWake(ctx, supervision.WakeRequest{
		RootScopeID:           "sess-other",
		TargetParentSessionID: "sess-other",
		WakeReason:            "child_failed",
	})
	require.NoError(t, err)

	require.Equal(t, 1, resolveLocalChatSessionPendingWakes(session))

	left, err := host.Supervision.Store.ListWakePending(ctx, supervision.WakeFilter{RootScopeID: "sess-close"})
	require.NoError(t, err)
	require.Empty(t, left, "the closing session must leave no pending wake behind")
	other, err := host.Supervision.Store.ListWakePending(ctx, supervision.WakeFilter{RootScopeID: "sess-other"})
	require.NoError(t, err)
	require.Len(t, other, 1, "another session's durable wake must survive")

	require.Zero(t, resolveLocalChatSessionPendingWakes(session), "cleanup must be idempotent")
	require.Zero(t, resolveLocalChatSessionPendingWakes(nil))
	require.Zero(t, resolveLocalChatSessionPendingWakes(&ChatSession{}))
}
