package background

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// monitorTestFixture 提交一个真实运行的 job 并收集 manager 事件。
type monitorTestFixture struct {
	manager *Manager
	mu      sync.Mutex
	events  []JobEvent
	jobID   string
}

func newMonitorTestFixture(t *testing.T, command string, sessionID string) *monitorTestFixture {
	t.Helper()
	fixture := &monitorTestFixture{}
	fixture.manager = NewManager(Config{
		EventHandler: func(event JobEvent) {
			fixture.mu.Lock()
			fixture.events = append(fixture.events, event)
			fixture.mu.Unlock()
		},
	})
	t.Cleanup(func() { require.NoError(t, fixture.manager.Close()) })

	job, err := fixture.manager.SubmitShell(context.Background(), sessionID, BackgroundTaskArgs{Command: command})
	require.NoError(t, err)
	require.NotNil(t, job)
	fixture.jobID = job.ID
	return fixture
}

func (f *monitorTestFixture) monitorCheckEvents() []JobEvent {
	f.mu.Lock()
	defer f.mu.Unlock()
	checks := make([]JobEvent, 0, len(f.events))
	for _, event := range f.events {
		if event.Type == MonitorCheckEventType {
			checks = append(checks, event)
		}
	}
	return checks
}

func TestScheduleMonitorEmitsOneCheckWhileRunning(t *testing.T) {
	fixture := newMonitorTestFixture(t, shellDelayCommand(3*time.Second, "out"), "session-monitor")
	ctx := context.Background()

	info, err := fixture.manager.ScheduleMonitor(fixture.jobID, MonitorOptions{CheckAfter: 200 * time.Millisecond})
	require.NoError(t, err)
	require.Equal(t, fixture.jobID, info.JobID)
	require.Equal(t, "session-monitor", info.SessionID)
	require.NotEmpty(t, info.MonitorID)
	require.False(t, info.CheckAt.IsZero())
	require.Equal(t, 1, fixture.manager.ActiveMonitorCount())

	require.Eventually(t, func() bool {
		return len(fixture.monitorCheckEvents()) == 1
	}, 5*time.Second, 20*time.Millisecond, "the check deadline must emit exactly one monitor_check event")

	event := fixture.monitorCheckEvents()[0]
	// 到点时 job 可能仍在排队：check 如实报告「尚未进入终态」的当前状态。
	status, ok := event.Payload["status"].(JobStatus)
	require.True(t, ok)
	require.Contains(t, []JobStatus{StatusPending, StatusRunning}, status)
	require.Equal(t, "session-monitor", event.Payload["session_id"])
	require.Equal(t, info.MonitorID, event.Payload["monitor_id"])

	// 一次性：烧掉后不再有余留计时器。
	require.Zero(t, fixture.manager.ActiveMonitorCount())
	time.Sleep(150 * time.Millisecond)
	require.Len(t, fixture.monitorCheckEvents(), 1)
	_, err = fixture.manager.GetJob(ctx, fixture.jobID)
	require.NoError(t, err)
}

func TestScheduleMonitorRejectsFinishedAndUnknownJobs(t *testing.T) {
	fixture := newMonitorTestFixture(t, shellEchoCommand("done"), "session-monitor")
	ctx := context.Background()
	require.NoError(t, waitForJobStatus(ctx, fixture.manager, fixture.jobID, StatusCompleted, backgroundTestTimeout(20*time.Second)))

	_, err := fixture.manager.ScheduleMonitor(fixture.jobID, MonitorOptions{CheckAfter: 200 * time.Millisecond})
	require.ErrorIs(t, err, ErrJobNotRunning)

	_, err = fixture.manager.ScheduleMonitor("job-does-not-exist", MonitorOptions{CheckAfter: 200 * time.Millisecond})
	require.Error(t, err)
	require.Contains(t, err.Error(), "job-does-not-exist")
	require.Zero(t, fixture.manager.ActiveMonitorCount())
}

func TestTerminalTransitionCancelsPendingMonitors(t *testing.T) {
	fixture := newMonitorTestFixture(t, shellDelayCommand(300*time.Millisecond, "done"), "session-monitor")
	ctx := context.Background()

	_, err := fixture.manager.ScheduleMonitor(fixture.jobID, MonitorOptions{CheckAfter: 30 * time.Second})
	require.NoError(t, err)
	require.Equal(t, 1, fixture.manager.ActiveMonitorCount())

	require.NoError(t, waitForJobStatus(ctx, fixture.manager, fixture.jobID, StatusCompleted, backgroundTestTimeout(20*time.Second)))
	require.Zero(t, fixture.manager.ActiveMonitorCount(),
		"a terminal transition must disarm the job's monitors")
	require.Empty(t, fixture.monitorCheckEvents())
}

func TestMonitorMaxDurationCancelsJob(t *testing.T) {
	fixture := newMonitorTestFixture(t, shellDelayCommand(30*time.Second, "slow"), "session-monitor")
	ctx := context.Background()

	_, err := fixture.manager.ScheduleMonitor(fixture.jobID, MonitorOptions{
		CheckAfter:  20 * time.Second,
		MaxDuration: 200 * time.Millisecond,
	})
	require.NoError(t, err)

	require.NoError(t, waitForJobStatus(ctx, fixture.manager, fixture.jobID, StatusCancelled, backgroundTestTimeout(20*time.Second)))
	job, err := fixture.manager.GetJob(ctx, fixture.jobID)
	require.NoError(t, err)
	require.Equal(t, CancelSourceMonitorMaxDuration, job.Metadata["cancel_source"],
		"the terminal evidence must tell a monitor deadline apart from a user cancellation")
	require.Zero(t, fixture.manager.ActiveMonitorCount())
	require.Empty(t, fixture.monitorCheckEvents(), "a kill deadline must not also emit a check")
}

func TestCancelSessionMonitorsOnlyTouchesThatSession(t *testing.T) {
	ctx := context.Background()
	manager := NewManager(Config{})
	t.Cleanup(func() { require.NoError(t, manager.Close()) })

	first, err := manager.SubmitShell(ctx, "session-a", BackgroundTaskArgs{Command: shellDelayCommand(30*time.Second, "a")})
	require.NoError(t, err)
	second, err := manager.SubmitShell(ctx, "session-b", BackgroundTaskArgs{Command: shellDelayCommand(30*time.Second, "b")})
	require.NoError(t, err)

	_, err = manager.ScheduleMonitor(first.ID, MonitorOptions{CheckAfter: 20 * time.Second})
	require.NoError(t, err)
	_, err = manager.ScheduleMonitor(second.ID, MonitorOptions{CheckAfter: 20 * time.Second})
	require.NoError(t, err)
	require.Equal(t, 2, manager.ActiveMonitorCount())

	require.Equal(t, 1, manager.CancelSessionMonitors("session-a"))
	require.Equal(t, 1, manager.ActiveMonitorCount())
	require.Zero(t, manager.CancelSessionMonitors("session-missing"))
	require.Zero(t, manager.CancelSessionMonitors(""))

	var nilManager *Manager
	require.Zero(t, nilManager.CancelSessionMonitors("session-a"))
	require.Zero(t, nilManager.ActiveMonitorCount())

	// 清理：长睡 job 不留给后续用例。
	_, _ = manager.CancelJob(ctx, first.ID)
	_, _ = manager.CancelJob(ctx, second.ID)
}

// TestCancelJobMonitorsIsIdempotentAndJobScoped 钉住工具面撤单依赖的语义：按 job
// 解除（不动其他 job）、幂等、nil/空输入安全，且 job 本身不受影响。
func TestCancelJobMonitorsIsIdempotentAndJobScoped(t *testing.T) {
	ctx := context.Background()
	manager := NewManager(Config{})
	t.Cleanup(func() { require.NoError(t, manager.Close()) })

	first, err := manager.SubmitShell(ctx, "session-monitor", BackgroundTaskArgs{Command: shellDelayCommand(30*time.Second, "a")})
	require.NoError(t, err)
	second, err := manager.SubmitShell(ctx, "session-monitor", BackgroundTaskArgs{Command: shellDelayCommand(30*time.Second, "b")})
	require.NoError(t, err)
	_, err = manager.ScheduleMonitor(first.ID, MonitorOptions{CheckAfter: 20 * time.Second})
	require.NoError(t, err)
	_, err = manager.ScheduleMonitor(second.ID, MonitorOptions{CheckAfter: 20 * time.Second})
	require.NoError(t, err)

	require.Equal(t, 1, manager.CancelJobMonitors(first.ID))
	require.Zero(t, manager.CancelJobMonitors(first.ID), "disarming twice must stay quiet")
	require.Zero(t, manager.CancelJobMonitors("job-missing"))
	require.Zero(t, manager.CancelJobMonitors("  "))
	require.Equal(t, 1, manager.ActiveMonitorCount(), "other jobs keep their monitors")

	var nilManager *Manager
	require.Zero(t, nilManager.CancelJobMonitors(first.ID))

	// job 不受撤单影响，仍在运行。
	job, err := manager.GetJob(ctx, first.ID)
	require.NoError(t, err)
	require.False(t, IsTerminalStatus(job.Status))

	_, _ = manager.CancelJob(ctx, first.ID)
	_, _ = manager.CancelJob(ctx, second.ID)
}

func TestScheduleMonitorNilAndEmptyInputs(t *testing.T) {
	var nilManager *Manager
	_, err := nilManager.ScheduleMonitor("job", MonitorOptions{})
	require.Error(t, err)

	manager := NewManager(Config{})
	t.Cleanup(func() { require.NoError(t, manager.Close()) })
	_, err = manager.ScheduleMonitor("   ", MonitorOptions{})
	require.Error(t, err)
	require.False(t, errors.Is(err, ErrJobNotRunning))
}
