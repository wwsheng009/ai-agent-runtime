package toolbroker

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/background"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBrokerExecuteBackgroundTaskReturnsRestartPolicy(t *testing.T) {
	ctx := context.Background()
	manager := background.NewManager(background.Config{
		LogDir: filepath.Join(t.TempDir(), "logs"),
	})

	broker := &Broker{Background: manager}
	raw, _, err := broker.Execute(ctx, "session-1", ToolBackgroundTask, map[string]interface{}{
		"command":        "echo ok",
		"restart_policy": "rerun",
	})
	require.NoError(t, err)

	result, ok := raw.(BackgroundTaskResult)
	require.True(t, ok)
	assert.NotEmpty(t, result.JobID)
	assert.Equal(t, string(background.StatusPending), result.Status)
	assert.Equal(t, background.RestartPolicyRerun, result.RestartPolicy)
}
