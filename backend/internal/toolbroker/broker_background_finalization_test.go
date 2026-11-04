package toolbroker

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/wwsheng009/ai-agent-runtime/internal/background"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolctx"
)

func requiredJobTestContext() context.Context {
	ctx := context.Background()
	ctx = toolctx.WithSessionID(ctx, "session-required-job")
	ctx = toolctx.WithTurnID(ctx, "turn-required-job")
	ctx = toolctx.WithEpisodeID(ctx, "episode-required-job")
	return ctx
}

func TestBrokerJobStartRequiredPolicyCapturesParentIdentity(t *testing.T) {
	manager := background.NewManager(background.Config{
		StorePath: filepath.Join(t.TempDir(), "background.sqlite"),
		LogDir:    filepath.Join(t.TempDir(), "logs"),
	})
	t.Cleanup(func() { _ = manager.Close() })
	broker := &Broker{Background: manager}

	ctx := requiredJobTestContext()
	raw, metadata, err := broker.Execute(ctx, "session-required-job", ToolJobStart, map[string]interface{}{
		"command":             "echo required",
		"finalization_policy": "required",
	})
	require.NoError(t, err)

	result, ok := raw.(BackgroundTaskResult)
	require.True(t, ok)
	require.Equal(t, background.JobPolicyRequired, result.FinalizationPolicy)
	require.Equal(t, "turn-required-job", result.ParentTurnID)
	require.Equal(t, "episode-required-job", result.ParentEpisodeID)
	require.Equal(t, string(background.JobPolicyRequired), metadata["finalization_policy"])
	require.Equal(t, "turn-required-job", metadata["parent_turn_id"])
	require.Equal(t, "episode-required-job", metadata["parent_episode_id"])

	job, err := manager.GetJob(ctx, result.JobID)
	require.NoError(t, err)
	origin := background.JobOriginFromMetadata(job.Metadata)
	require.Equal(t, string(background.JobPolicyRequired), origin.FinalizationPolicy)
	require.Equal(t, "turn-required-job", origin.ParentTurnID)
	require.Equal(t, "episode-required-job", origin.ParentEpisodeID)
}

func TestBrokerJobStartDefaultPolicyStaysDetached(t *testing.T) {
	manager := background.NewManager(background.Config{LogDir: filepath.Join(t.TempDir(), "logs")})
	t.Cleanup(func() { _ = manager.Close() })
	broker := &Broker{Background: manager}

	raw, metadata, err := broker.Execute(context.Background(), "session-detached-job", ToolJobStart, map[string]interface{}{
		"command": "echo detached",
	})
	require.NoError(t, err)
	result, ok := raw.(BackgroundTaskResult)
	require.True(t, ok)
	require.Equal(t, background.JobPolicyDetached, result.FinalizationPolicy)
	require.Equal(t, string(background.JobPolicyDetached), metadata["finalization_policy"])
}

func TestBrokerJobStartRequiredFailsClosedWithoutResumeIdentity(t *testing.T) {
	manager := background.NewManager(background.Config{
		StorePath: filepath.Join(t.TempDir(), "background.sqlite"),
		LogDir:    filepath.Join(t.TempDir(), "logs"),
	})
	t.Cleanup(func() { _ = manager.Close() })
	broker := &Broker{Background: manager}

	_, _, err := broker.Execute(context.Background(), "session-no-identity", ToolJobStart, map[string]interface{}{
		"command":             "echo no-identity",
		"finalization_policy": "required",
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "turn and episode identity")
}

func TestBrokerJobStartRequiredFailsClosedWithoutDurableJobStore(t *testing.T) {
	manager := background.NewManager(background.Config{LogDir: filepath.Join(t.TempDir(), "logs")})
	t.Cleanup(func() { _ = manager.Close() })
	broker := &Broker{Background: manager}

	ctx := requiredJobTestContext()
	_, _, err := broker.Execute(ctx, "session-required-job", ToolJobStart, map[string]interface{}{
		"command":             "echo no-durable-store",
		"finalization_policy": "required",
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "durable background job store")
}

func TestBrokerJobStartRejectsUnknownFinalizationPolicy(t *testing.T) {
	manager := background.NewManager(background.Config{LogDir: filepath.Join(t.TempDir(), "logs")})
	t.Cleanup(func() { _ = manager.Close() })
	broker := &Broker{Background: manager}

	_, _, err := broker.Execute(context.Background(), "session-invalid-policy", ToolJobStart, map[string]interface{}{
		"command":             "echo invalid",
		"finalization_policy": "sometimes",
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "unsupported finalization_policy")
}
