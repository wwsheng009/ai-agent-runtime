package tools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	runtimeerrors "github.com/wwsheng009/ai-agent-runtime/internal/errors"
	runtimeexecution "github.com/wwsheng009/ai-agent-runtime/internal/execution"
	runtimeexecutor "github.com/wwsheng009/ai-agent-runtime/internal/executor"
)

func TestReliabilityEvalParentDeadlineStopsRealToolExecution(t *testing.T) {
	const (
		requestedTimeout = 5 * time.Second
		parentTimeout    = 400 * time.Millisecond
		commandDelay     = 1200 * time.Millisecond
		effectFile       = "deadline-effect.txt"
	)

	workdir := t.TempDir()
	ctx, cancel := runtimeexecution.WithTimeoutSource(
		context.Background(),
		parentTimeout,
		runtimeexecution.TimeoutSourceChatTurnDeadline,
	)
	defer cancel()

	startedAt := time.Now()
	result, err := NewBashTool().Execute(ctx, map[string]interface{}{
		"command":    reliabilityEvalAppendCommand(commandDelay, effectFile, "deadline-effect"),
		"workdir":    workdir,
		"timeout_ms": requestedTimeout.Milliseconds(),
	})
	require.NoError(t, err)
	require.NotNil(t, result)
	require.False(t, result.Success)
	require.Error(t, result.Error)
	require.True(t, runtimeerrors.Is(result.Error, runtimeerrors.ErrTurnDeadlineExceeded), result.Error)
	require.ErrorIs(t, result.Error, context.DeadlineExceeded)

	var runtimeErr *runtimeerrors.RuntimeError
	require.ErrorAs(t, result.Error, &runtimeErr)
	require.Equal(t, runtimeerrors.ErrTurnDeadlineExceeded, runtimeErr.Code)
	require.EqualValues(t, requestedTimeout.Milliseconds(), runtimeErr.Context["timeout_requested_ms"])
	require.Equal(t, string(runtimeexecution.TimeoutSourceChatTurnDeadline), runtimeErr.Context["timeout_source"])
	errorEffectiveTimeout, ok := runtimeErr.Context["timeout_effective_ms"].(int64)
	require.True(t, ok, "error timeout_effective_ms must be an int64: %#v", runtimeErr.Context)
	require.GreaterOrEqual(t, errorEffectiveTimeout, int64(0))
	require.Less(t, errorEffectiveTimeout, requestedTimeout.Milliseconds())

	effectiveTimeout, ok := result.Metadata["timeout_effective_ms"].(int64)
	require.True(t, ok, "timeout_effective_ms must be an int64: %#v", result.Metadata)
	require.Greater(t, effectiveTimeout, int64(0))
	require.Less(t, effectiveTimeout, requestedTimeout.Milliseconds())
	require.EqualValues(t, requestedTimeout.Milliseconds(), result.Metadata["timeout_requested_ms"])
	require.EqualValues(t, effectiveTimeout, result.Metadata["timeout_ms"])
	require.Equal(t, string(runtimeexecution.TimeoutSourceChatTurnDeadline), result.Metadata["timeout_source"])

	reliabilityEvalWaitPastCommand(t, startedAt, commandDelay)
	data, readErr := os.ReadFile(filepath.Join(workdir, effectFile))
	if os.IsNotExist(readErr) {
		return
	}
	require.NoError(t, readErr)
	require.Empty(t, data, "the command committed a side effect after its parent deadline")
}

func reliabilityEvalWaitPastCommand(t *testing.T, startedAt time.Time, commandDelay time.Duration) {
	t.Helper()
	waitUntil := startedAt.Add(commandDelay + 300*time.Millisecond)
	if remaining := time.Until(waitUntil); remaining > 0 {
		time.Sleep(remaining)
	}
}

func reliabilityEvalAppendCommand(delay time.Duration, fileName, marker string) string {
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
