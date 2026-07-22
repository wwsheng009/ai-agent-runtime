package toolbroker

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wwsheng009/ai-agent-runtime/internal/background"
	runtimeerrors "github.com/wwsheng009/ai-agent-runtime/internal/errors"
	runtimeoutput "github.com/wwsheng009/ai-agent-runtime/internal/output"
	"github.com/wwsheng009/ai-agent-runtime/internal/types"
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
	assert.Equal(t, "queued", result.LaunchState)
	assert.Equal(t, "process", result.StartupProbe)
	assert.Equal(t, "pending", result.HealthcheckState)
}

func TestBackgroundTaskResultRemainsReusableInModelToolMessage(t *testing.T) {
	manager := background.NewManager(background.Config{LogDir: filepath.Join(t.TempDir(), "logs")})
	t.Cleanup(func() { require.NoError(t, manager.Close()) })
	broker := &Broker{Background: manager}
	call := types.ToolCall{
		ID:   "call-background-contract",
		Name: ToolBackgroundTask,
		Args: map[string]interface{}{"command": "echo ok"},
	}

	raw, metadata, err := broker.ExecuteToolCall(context.Background(), "session-contract", call)
	require.NoError(t, err)
	envelope, err := runtimeoutput.NewGateway(nil).Process(context.Background(), runtimeoutput.RawToolResult{
		SessionID:  "session-contract",
		ToolName:   call.Name,
		ToolCallID: call.ID,
		Content:    raw,
		Metadata:   metadata,
	})
	require.NoError(t, err)
	modelContent := runtimeoutput.RenderToolResultContentForModel(raw, "", envelope)

	result := raw.(BackgroundTaskResult)
	require.NotEmpty(t, result.JobID)
	assert.Contains(t, modelContent, result.JobID)
	assert.Contains(t, modelContent, `"status": "pending"`)
}

func TestBrokerExecuteBackgroundTaskParsesStartupAcceptance(t *testing.T) {
	manager := background.NewManager(background.Config{MaxConcurrentJobs: 1})
	defer func() { require.NoError(t, manager.Close()) }()
	broker := &Broker{Background: manager}

	raw, metadata, err := broker.Execute(context.Background(), "session-startup", ToolBackgroundTask, map[string]interface{}{
		"command": "echo ok",
		"startup_acceptance": map[string]interface{}{
			"probe":           "none",
			"grace_period_ms": float64(15),
		},
	})
	require.NoError(t, err)
	result := raw.(BackgroundTaskResult)
	require.Equal(t, "none", result.StartupProbe)
	require.Equal(t, int64(15), result.StartupGraceMs)
	require.Equal(t, "not_configured", result.HealthcheckState)
	require.Equal(t, "none", metadata["startup_probe"])
}

func TestBrokerRejectsInvalidStartupAcceptance(t *testing.T) {
	manager := background.NewManager(background.Config{})
	defer func() { require.NoError(t, manager.Close()) }()
	broker := &Broker{Background: manager}

	_, _, err := broker.Execute(context.Background(), "session-startup", ToolBackgroundTask, map[string]interface{}{
		"command":            "echo ok",
		"startup_acceptance": map[string]interface{}{"probe": "tcp"},
	})
	require.Error(t, err)
	require.True(t, runtimeerrors.Is(err, runtimeerrors.ErrToolInvalidArgs))
}

func TestBrokerTaskOutputMissingJobReturnsActionableError(t *testing.T) {
	manager := background.NewManager(background.Config{})
	t.Cleanup(func() { require.NoError(t, manager.Close()) })
	broker := &Broker{Background: manager}

	_, _, err := broker.Execute(context.Background(), "session-missing-job", ToolTaskOutput, map[string]interface{}{
		"job_id": "job_guess_1",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "exact job_id returned by background_task")
	assert.True(t, runtimeerrors.Is(err, runtimeerrors.ErrJobNotFound))
}
