package toolbroker

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/wwsheng009/ai-agent-runtime/internal/background"
	runtimeerrors "github.com/wwsheng009/ai-agent-runtime/internal/errors"
	runtimeexecutor "github.com/wwsheng009/ai-agent-runtime/internal/executor"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolresult"
	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

func TestReliabilityEvalBrokerTimeoutRetryUsesNewInvocationWithoutDuplicateSideEffect(t *testing.T) {
	const (
		defaultTimeout = 300 * time.Millisecond
		commandDelay   = 1200 * time.Millisecond
		effectFile     = "retry-effect.txt"
	)

	ctx := context.Background()
	workdir := t.TempDir()
	manager := background.NewManager(background.Config{
		MaxConcurrentJobs: 1,
		DefaultTimeout:    defaultTimeout,
	})
	defer func() { require.NoError(t, manager.Close()) }()
	broker := &Broker{
		Background:          manager,
		SessionContextStore: newFakeSessionContextStore(),
	}

	firstRaw, firstMeta, err := broker.ExecuteToolCall(ctx, "session-retry-eval", types.ToolCall{
		ID:   "call-retry-attempt-1",
		Name: ToolBackgroundTask,
		Args: map[string]interface{}{
			"command": brokerReliabilityAppendCommand(commandDelay, effectFile, "timed-out-attempt"),
			"cwd":     workdir,
		},
	})
	require.NoError(t, err)
	first, ok := firstRaw.(BackgroundTaskResult)
	require.True(t, ok)
	firstActualID, ok := firstMeta["job_id"].(string)
	require.True(t, ok)
	require.NotEmpty(t, firstActualID)
	require.NotEqual(t, firstActualID, first.JobID)
	require.Equal(t, toolresult.SourceBroker, firstMeta[toolresult.SourceKey])
	require.EqualValues(t, defaultTimeout.Milliseconds(), firstMeta["timeout_requested_ms"])
	require.EqualValues(t, defaultTimeout.Milliseconds(), firstMeta["timeout_effective_ms"])
	require.Equal(t, "tool_default", firstMeta["timeout_source"])

	require.Eventually(t, func() bool {
		job, getErr := manager.GetJob(ctx, firstActualID)
		return getErr == nil && job != nil && job.Status == background.StatusTimedOut
	}, 15*time.Second, 25*time.Millisecond)
	firstJob, err := manager.GetJob(ctx, firstActualID)
	require.NoError(t, err)
	require.NotNil(t, firstJob.StartedAt)

	firstOutputRaw, firstOutputMeta, err := broker.ExecuteToolCall(ctx, "session-retry-eval", types.ToolCall{
		ID:   "call-retry-output-1",
		Name: ToolTaskOutput,
		Args: map[string]interface{}{"job_id": first.JobID},
	})
	require.NoError(t, err)
	firstOutput, ok := firstOutputRaw.(TaskOutputResult)
	require.True(t, ok)
	require.Equal(t, string(background.StatusTimedOut), firstOutput.Status)
	require.Equal(t, string(runtimeerrors.ErrToolTimeout), firstOutput.ErrorCode)
	require.EqualValues(t, defaultTimeout.Milliseconds(), firstOutput.TimeoutRequestedMs)
	require.EqualValues(t, defaultTimeout.Milliseconds(), firstOutput.TimeoutEffectiveMs)
	require.Equal(t, "tool_default", firstOutput.TimeoutSource)
	require.Equal(t, string(runtimeerrors.ErrToolTimeout), firstOutputMeta["error_code"])
	require.Equal(t, toolresult.SourceBroker, firstOutputMeta[toolresult.SourceKey])

	retryRaw, retryMeta, err := broker.ExecuteToolCall(ctx, "session-retry-eval", types.ToolCall{
		ID:   "call-retry-attempt-2",
		Name: ToolBackgroundTask,
		Args: map[string]interface{}{
			"command":     brokerReliabilityAppendCommand(0, effectFile, "retry-succeeded"),
			"cwd":         workdir,
			"timeout_sec": 5,
		},
	})
	require.NoError(t, err)
	retry, ok := retryRaw.(BackgroundTaskResult)
	require.True(t, ok)
	retryActualID, ok := retryMeta["job_id"].(string)
	require.True(t, ok)
	require.NotEqual(t, first.JobID, retry.JobID)
	require.NotEqual(t, firstActualID, retryActualID)
	require.Equal(t, "tool_argument", retryMeta["timeout_source"])

	require.Eventually(t, func() bool {
		job, getErr := manager.GetJob(ctx, retryActualID)
		return getErr == nil && job != nil && job.Status == background.StatusCompleted
	}, 15*time.Second, 25*time.Millisecond)

	retryOutputRaw, _, err := broker.ExecuteToolCall(ctx, "session-retry-eval", types.ToolCall{
		ID:   "call-retry-output-2",
		Name: ToolTaskOutput,
		Args: map[string]interface{}{"job_id": retry.JobID},
	})
	require.NoError(t, err)
	retryOutput, ok := retryOutputRaw.(TaskOutputResult)
	require.True(t, ok)
	require.Equal(t, string(background.StatusCompleted), retryOutput.Status)

	brokerReliabilityWaitPastCommand(t, *firstJob.StartedAt, commandDelay)
	data, err := os.ReadFile(filepath.Join(workdir, effectFile))
	require.NoError(t, err)
	require.Equal(t, []string{"retry-succeeded"}, strings.Fields(string(data)))
}

func brokerReliabilityWaitPastCommand(t *testing.T, startedAt time.Time, commandDelay time.Duration) {
	t.Helper()
	waitUntil := startedAt.Add(commandDelay + 300*time.Millisecond)
	if remaining := time.Until(waitUntil); remaining > 0 {
		time.Sleep(remaining)
	}
}

func brokerReliabilityAppendCommand(delay time.Duration, fileName, marker string) string {
	shell := runtimeexecutor.DefaultUserShell()
	switch shell.Type {
	case runtimeexecutor.ShellTypePowerShell, runtimeexecutor.ShellTypePwsh:
		prefix := ""
		if delay > 0 {
			prefix = fmt.Sprintf("Start-Sleep -Milliseconds %d; ", delay.Milliseconds())
		}
		return fmt.Sprintf("%s[System.IO.File]::AppendAllText('%s', '%s' + [Environment]::NewLine)", prefix, fileName, marker)
	case runtimeexecutor.ShellTypeCmd:
		prefix := ""
		if delay > 0 {
			prefix = "ping -n 2 127.0.0.1 >nul & "
		}
		return fmt.Sprintf("%secho %s>>%s", prefix, marker, fileName)
	default:
		prefix := ""
		if delay > 0 {
			prefix = fmt.Sprintf("sleep %.3f; ", delay.Seconds())
		}
		return fmt.Sprintf("%sprintf '%s\\n' >> '%s'", prefix, marker, fileName)
	}
}
