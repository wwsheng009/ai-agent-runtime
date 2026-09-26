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
