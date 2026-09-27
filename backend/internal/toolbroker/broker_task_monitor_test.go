package toolbroker

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wwsheng009/ai-agent-runtime/internal/background"
)

// TestTaskMonitorSchedulesClampedCheck 覆盖模型面：巡检登记成功、越界值被夹取并
// 回报生效值，模型因此不需要猜边界。
func TestTaskMonitorSchedulesClampedCheck(t *testing.T) {
	broker := newBackgroundTestBroker(t)
	jobID := submitTestJob(t, broker, backgroundSleeperCommand())
	ctx := context.Background()

	raw, metadata, err := broker.Execute(ctx, "session-wait", ToolTaskMonitor, map[string]interface{}{
		"job_id":         jobID,
		"check_after_ms": 100,
	})
	require.NoError(t, err)
	result, ok := raw.(map[string]interface{})
	require.True(t, ok)
	assert.Equal(t, true, result["scheduled"])
	assert.Equal(t, jobID, result["job_id"])
	assert.Equal(t, taskMonitorCheckAfterMinMs, result["check_after_ms"], "out-of-range check deadlines clamp to the minimum")
	assert.NotEmpty(t, result["monitor_id"])
	assert.Equal(t, true, metadata["scheduled"])
	assert.Equal(t, 1, broker.Background.ActiveMonitorCount())

	// 清理：取消长睡 job（终态会自动解除巡检计时器）。
	_, _, err = broker.Execute(ctx, "session-wait", ToolTaskKill, map[string]interface{}{"job_id": jobID})
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		return broker.Background.ActiveMonitorCount() == 0
	}, 10*time.Second, 20*time.Millisecond, "a terminal transition must disarm the monitor")
}

func TestTaskMonitorArmsMaxDurationWatchdog(t *testing.T) {
	broker := newBackgroundTestBroker(t)
	jobID := submitTestJob(t, broker, backgroundSleeperCommand())
	ctx := context.Background()

	raw, _, err := broker.Execute(ctx, "session-wait", ToolTaskMonitor, map[string]interface{}{
		"job_id":          jobID,
		"check_after_ms":  600000,
		"max_duration_ms": 1000,
	})
	require.NoError(t, err)
	result, ok := raw.(map[string]interface{})
	require.True(t, ok)
	assert.Equal(t, taskMonitorMaxDurationMinMs, result["max_duration_ms"], "watchdogs clamp to the minimum")
	assert.Contains(t, result["message"], background.CancelSourceMonitorMaxDuration)

	_, _, err = broker.Execute(ctx, "session-wait", ToolTaskKill, map[string]interface{}{"job_id": jobID})
	require.NoError(t, err)
}

// TestTaskMonitorReportsFinishedJobAsContent 是「已结束不是错误」契约：模型拿到
// scheduled=false + 终态与 exit_code，而不是一个需要重试的失败。
func TestTaskMonitorReportsFinishedJobAsContent(t *testing.T) {
	broker := newBackgroundTestBroker(t)
	jobID := submitTestJob(t, broker, "echo ok")
	ctx := context.Background()
	require.Eventually(t, func() bool {
		job, err := broker.Background.GetJob(ctx, jobID)
		return err == nil && job != nil && background.IsTerminalStatus(job.Status)
	}, 15*time.Second, 50*time.Millisecond, "job should finish")

	raw, metadata, err := broker.Execute(ctx, "session-wait", ToolTaskMonitor, map[string]interface{}{
		"job_id":         jobID,
		"check_after_ms": 5000,
	})
	require.NoError(t, err)
	result, ok := raw.(map[string]interface{})
	require.True(t, ok)
	assert.Equal(t, false, result["scheduled"])
	assert.Contains(t, result["message"], "already finished")
	assert.Equal(t, "completed", result["status"])
	assert.Equal(t, false, metadata["scheduled"])
	assert.Zero(t, broker.Background.ActiveMonitorCount())
}

func TestTaskMonitorRepairHints(t *testing.T) {
	broker := newBackgroundTestBroker(t)
	ctx := context.Background()

	_, _, err := broker.Execute(ctx, "session-wait", ToolTaskMonitor, map[string]interface{}{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "job_id is required")

	_, _, err = broker.Execute(ctx, "session-wait", ToolTaskMonitor, map[string]interface{}{"job_id": "job-not-real"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "exact job_id")
}

// TestTaskMonitorCancelDisarmsWithoutTouchingTheJob 覆盖撤单面：cancel=true 只解除
// 巡检（幂等），job 继续跑；与排期参数混用则明确报错而不是猜语义。
func TestTaskMonitorCancelDisarmsWithoutTouchingTheJob(t *testing.T) {
	broker := newBackgroundTestBroker(t)
	jobID := submitTestJob(t, broker, backgroundSleeperCommand())
	ctx := context.Background()

	_, _, err := broker.Execute(ctx, "session-wait", ToolTaskMonitor, map[string]interface{}{
		"job_id":         jobID,
		"check_after_ms": 600000,
	})
	require.NoError(t, err)
	require.Equal(t, 1, broker.Background.ActiveMonitorCount())

	raw, metadata, err := broker.Execute(ctx, "session-wait", ToolTaskMonitor, map[string]interface{}{
		"job_id": jobID,
		"cancel": true,
	})
	require.NoError(t, err)
	result, ok := raw.(map[string]interface{})
	require.True(t, ok)
	assert.Equal(t, 1, result["cancelled_monitors"])
	assert.Equal(t, false, result["scheduled"])
	assert.Contains(t, result["message"], "terminal transition still wakes")
	assert.Equal(t, 1, metadata["cancelled_monitors"])
	assert.Zero(t, broker.Background.ActiveMonitorCount())

	// 幂等：再撤一次是 0，而不是错误。
	raw, _, err = broker.Execute(ctx, "session-wait", ToolTaskMonitor, map[string]interface{}{
		"job_id": jobID,
		"cancel": true,
	})
	require.NoError(t, err)
	result, ok = raw.(map[string]interface{})
	require.True(t, ok)
	assert.Equal(t, 0, result["cancelled_monitors"])

	// 混用排期参数：明确拒绝，避免「替换还是追加」的歧义。
	_, _, err = broker.Execute(ctx, "session-wait", ToolTaskMonitor, map[string]interface{}{
		"job_id":         jobID,
		"cancel":         true,
		"check_after_ms": 60000,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cannot be combined")

	// job 仍在跑（撤单不等于 kill）。
	job, err := broker.Background.GetJob(ctx, jobID)
	require.NoError(t, err)
	assert.False(t, background.IsTerminalStatus(job.Status))

	_, _, err = broker.Execute(ctx, "session-wait", ToolTaskKill, map[string]interface{}{"job_id": jobID})
	require.NoError(t, err)
}
