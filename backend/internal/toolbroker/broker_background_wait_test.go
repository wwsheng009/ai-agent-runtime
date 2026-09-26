package toolbroker

import (
	"context"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wwsheng009/ai-agent-runtime/internal/background"
	runtimeerrors "github.com/wwsheng009/ai-agent-runtime/internal/errors"
)

func newBackgroundTestBroker(t *testing.T) *Broker {
	t.Helper()
	manager := background.NewManager(background.Config{
		LogDir: filepath.Join(t.TempDir(), "logs"),
	})
	t.Cleanup(func() { require.NoError(t, manager.Close()) })
	return &Broker{Background: manager}
}

func submitTestJob(t *testing.T, broker *Broker, command string) string {
	t.Helper()
	raw, _, err := broker.Execute(context.Background(), "session-wait", ToolBackgroundTask, map[string]interface{}{
		"command": command,
	})
	require.NoError(t, err)
	result, ok := raw.(BackgroundTaskResult)
	require.True(t, ok)
	require.NotEmpty(t, result.JobID)
	return result.JobID
}

func backgroundSleeperCommand() string {
	if runtime.GOOS == "windows" {
		return "powershell -NoProfile -Command \"Start-Sleep -Seconds 30\""
	}
	return "sleep 30"
}

// backgroundBriefCommand 跑一小段但一定活到 wait 登记之后：用于验证「等待期间
// 进入终态」的观察标记，命令本身不能快到抢在登记前结束。
func backgroundBriefCommand() string {
	if runtime.GOOS == "windows" {
		return "powershell -NoProfile -Command \"Start-Sleep -Milliseconds 800; Write-Output done\""
	}
	return "sleep 0.8; echo done"
}

// TestTaskOutputWaitExitMarksTerminalObserved 钉住抑制链路的起点：wait=exit
// 期间 job 进入终态时，manager 把终态标成「模型即将从本次 wait 读到」，宿主
// 终态投影据此不再补一轮冗余唤醒。
func TestTaskOutputWaitExitMarksTerminalObserved(t *testing.T) {
	broker := newBackgroundTestBroker(t)
	jobID := submitTestJob(t, broker, backgroundBriefCommand())

	raw, metadata, err := broker.Execute(context.Background(), "session-wait", ToolTaskOutput, map[string]interface{}{
		"job_id":     jobID,
		"wait":       "exit",
		"timeout_ms": 15000,
	})
	require.NoError(t, err)
	assert.Equal(t, "exit", metadata["wait_condition"])
	result, ok := raw.(TaskOutputResult)
	require.True(t, ok)
	require.True(t, background.IsTerminalStatus(background.JobStatus(result.Status)))

	job, err := broker.Background.GetJob(context.Background(), jobID)
	require.NoError(t, err)
	assert.True(t, background.TerminalObserved(job),
		"a terminal transition observed through wait=exit must be flagged")
}

// TestTaskOutputImmediateReadDoesNotMarkObserved 是反向保证：wait=none 从不登记
// waiter，因此读到一个已经结束的 job 不会抑制它本应产生的终态唤醒。
func TestTaskOutputImmediateReadDoesNotMarkObserved(t *testing.T) {
	broker := newBackgroundTestBroker(t)
	jobID := submitTestJob(t, broker, "echo ok")

	_, metadata, err := broker.Execute(context.Background(), "session-wait", ToolTaskOutput, map[string]interface{}{
		"job_id": jobID,
		"wait":   "none",
	})
	require.NoError(t, err)
	// wait=none 不报「等待条件」（本来就没有等）：与既有语义一致。
	require.Nil(t, metadata["wait_condition"])

	require.Eventually(t, func() bool {
		job, err := broker.Background.GetJob(context.Background(), jobID)
		return err == nil && job != nil && background.IsTerminalStatus(job.Status)
	}, 15*time.Second, 50*time.Millisecond, "job should finish")

	job, err := broker.Background.GetJob(context.Background(), jobID)
	require.NoError(t, err)
	assert.False(t, background.TerminalObserved(job),
		"an immediate read must not suppress the terminal wake")
}

func TestTaskOutputWaitExitReturnsOnTerminalState(t *testing.T) {
	broker := newBackgroundTestBroker(t)
	jobID := submitTestJob(t, broker, "echo ok")

	raw, metadata, err := broker.Execute(context.Background(), "session-wait", ToolTaskOutput, map[string]interface{}{
		"job_id":     jobID,
		"wait":       "exit",
		"timeout_ms": 10000,
	})
	require.NoError(t, err)
	result, ok := raw.(TaskOutputResult)
	require.True(t, ok)
	assert.Equal(t, "exit", metadata["wait_condition"])
	assert.True(t, background.IsTerminalStatus(background.JobStatus(result.Status)), "status should be terminal: %s", result.Status)
}

func TestTaskOutputWaitTimeoutIsContentResult(t *testing.T) {
	broker := newBackgroundTestBroker(t)
	jobID := submitTestJob(t, broker, backgroundSleeperCommand())

	raw, metadata, err := broker.Execute(context.Background(), "session-wait", ToolTaskOutput, map[string]interface{}{
		"job_id":     jobID,
		"wait":       "exit",
		"timeout_ms": 1200,
	})
	require.NoError(t, err)
	result, ok := raw.(TaskOutputResult)
	require.True(t, ok)
	assert.Equal(t, "timeout", metadata["wait_condition"])
	assert.False(t, background.IsTerminalStatus(background.JobStatus(result.Status)), "job should still be running: %s", result.Status)
	assert.GreaterOrEqual(t, metadata["waited_ms"], int64(0))

	// 清理：确保长睡任务不会泄漏到测试结束之后。
	_, _, killErr := broker.Execute(context.Background(), "session-wait", ToolTaskKill, map[string]interface{}{"job_id": jobID})
	require.NoError(t, killErr)
}

func TestTaskOutputWaitRejectsUnknownMode(t *testing.T) {
	broker := newBackgroundTestBroker(t)
	jobID := submitTestJob(t, broker, "echo ok")

	_, _, err := broker.Execute(context.Background(), "session-wait", ToolTaskOutput, map[string]interface{}{
		"job_id": jobID,
		"wait":   "eventually",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unsupported wait mode")
}

func TestTaskKillUnknownJobReturnsActionableError(t *testing.T) {
	broker := newBackgroundTestBroker(t)

	_, _, err := broker.Execute(context.Background(), "session-kill", ToolTaskKill, map[string]interface{}{
		"job_id": "job_missing_1",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "exact job_id returned by background_task")
	assert.True(t, runtimeerrors.Is(err, runtimeerrors.ErrJobNotFound))
}

func TestTaskKillFinishedJobReportsNotCancelled(t *testing.T) {
	broker := newBackgroundTestBroker(t)
	jobID := submitTestJob(t, broker, "echo ok")

	_, _, err := broker.Execute(context.Background(), "session-kill", ToolTaskOutput, map[string]interface{}{
		"job_id":     jobID,
		"wait":       "exit",
		"timeout_ms": 10000,
	})
	require.NoError(t, err)

	raw, metadata, err := broker.Execute(context.Background(), "session-kill", ToolTaskKill, map[string]interface{}{
		"task_id": jobID,
		"reason":  "test cleanup",
	})
	require.NoError(t, err)
	result, ok := raw.(map[string]interface{})
	require.True(t, ok)
	assert.Equal(t, false, result["cancelled"])
	assert.Contains(t, result["message"], "already finished")
	assert.Equal(t, "test cleanup", metadata["reason"])
}

func TestTaskKillRunningJobCancels(t *testing.T) {
	broker := newBackgroundTestBroker(t)
	jobID := submitTestJob(t, broker, backgroundSleeperCommand())

	raw, metadata, err := broker.Execute(context.Background(), "session-kill", ToolTaskKill, map[string]interface{}{
		"job_id": jobID,
	})
	require.NoError(t, err)
	result, ok := raw.(map[string]interface{})
	require.True(t, ok)
	assert.Equal(t, true, result["cancelled"])
	assert.Equal(t, true, metadata["cancelled"])

	// cancel 后应能观察到终态（轮询等待，避免依赖固定 sleep）。
	_, waitMetadata, waitErr := broker.Execute(context.Background(), "session-kill", ToolTaskOutput, map[string]interface{}{
		"job_id":     jobID,
		"wait":       "exit",
		"timeout_ms": 10000,
	})
	require.NoError(t, waitErr)
	assert.Equal(t, "exit", waitMetadata["wait_condition"])
}

func TestEffectiveTaskOutputWaitTimeoutClamps(t *testing.T) {
	assert.Equal(t, int64(taskOutputWaitDefaultMs), effectiveTaskOutputWaitTimeout(0).Milliseconds())
	assert.Equal(t, int64(taskOutputWaitMinMs), effectiveTaskOutputWaitTimeout(10).Milliseconds())
	assert.Equal(t, int64(5000), effectiveTaskOutputWaitTimeout(5000).Milliseconds())
	assert.Equal(t, int64(taskOutputWaitMaxMs), effectiveTaskOutputWaitTimeout(10_000_000).Milliseconds())
}
