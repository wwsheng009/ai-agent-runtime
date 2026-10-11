package toolbroker

import (
	"context"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/wwsheng009/ai-agent-runtime/internal/background"
	runtimeexecutor "github.com/wwsheng009/ai-agent-runtime/internal/executor"
)

func brokerInteractiveEchoCommand() string {
	if runtime.GOOS == "windows" {
		return `$input | ForEach-Object { Write-Output ("echo:" + $_) }`
	}
	return `while IFS= read -r line; do printf 'echo:%s\n' "$line"; done`
}

func TestBrokerWriteStdinRoundTrip(t *testing.T) {
	if runtime.GOOS == "windows" && runtimeexecutor.DefaultUserShell().Type == runtimeexecutor.ShellTypeCmd {
		t.Skip("interactive stdin echo is not portable under cmd.exe")
	}
	ctx := context.Background()
	manager := background.NewManager(background.Config{LogDir: filepath.Join(t.TempDir(), "logs")})
	t.Cleanup(func() { require.NoError(t, manager.Close()) })
	broker := &Broker{Background: manager}

	raw, _, err := broker.Execute(ctx, "session-write-stdin", ToolJobStart, map[string]interface{}{
		"command":     brokerInteractiveEchoCommand(),
		"interactive": true,
	})
	require.NoError(t, err)
	result, ok := raw.(BackgroundTaskResult)
	require.True(t, ok)
	require.NotEmpty(t, result.JobID)

	out, metadata, err := broker.Execute(ctx, "session-write-stdin", ToolJobInput, map[string]interface{}{
		"job_id":        result.JobID,
		"chars":         "hello\n",
		"yield_time_ms": 5000,
	})
	require.NoError(t, err)
	rendered, ok := out.(map[string]interface{})
	require.True(t, ok)
	assert.Contains(t, rendered["output"], "echo:hello")
	assert.Equal(t, "output", metadata["wait_condition"])
}

func TestBrokerWriteStdinOnOneShotJobIsContentResult(t *testing.T) {
	ctx := context.Background()
	manager := background.NewManager(background.Config{LogDir: filepath.Join(t.TempDir(), "logs")})
	t.Cleanup(func() { require.NoError(t, manager.Close()) })
	broker := &Broker{Background: manager}

	raw, _, err := broker.Execute(ctx, "session-one-shot", ToolJobStart, map[string]interface{}{
		"command": "echo ok",
	})
	require.NoError(t, err)
	result, ok := raw.(BackgroundTaskResult)
	require.True(t, ok)
	require.NotEmpty(t, result.JobID)

	out, metadata, err := broker.Execute(ctx, "session-one-shot", ToolJobInput, map[string]interface{}{
		"job_id": result.JobID,
		"chars":  "x\n",
	})
	require.NoError(t, err)
	rendered, ok := out.(map[string]interface{})
	require.True(t, ok)
	assert.Equal(t, false, rendered["ok"])
	assert.Equal(t, "NOT_INTERACTIVE", metadata["error_code"])
}
