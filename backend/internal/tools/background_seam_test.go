package tools

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/wwsheng009/ai-agent-runtime/internal/background"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolctx"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolkit/tools"
)

func TestShellDetachRoutesThroughBackgroundSubmitter(t *testing.T) {
	manager := background.NewManager(background.Config{})
	t.Cleanup(func() { require.NoError(t, manager.Close()) })

	toolManager := NewDefaultManager(nil)
	toolManager.SetBackgroundSubmitter(NewBackgroundSubmitter(manager))

	shell, ok := toolManager.toolkit.Get("shell")
	require.True(t, ok, "shell must be registered")
	_, wired := shell.(interface{ SetBackgroundSubmitter(tools.BackgroundSubmitter) })
	require.True(t, wired, "shell tool must expose the background submitter seam")

	ctx := toolctx.WithSessionID(context.Background(), "session-detach-managed")
	output, err := toolManager.Execute(ctx, "shell", map[string]interface{}{
		"command": "echo detach-managed-ok",
		"detach":  true,
	})
	require.NoError(t, err)
	assert.Contains(t, output, "受管")
	assert.Contains(t, output, "task_output")

	// The submitted job exists in the background manager and is attributed to
	// the ambient session.
	jobs, err := manager.ListJobs(ctx, background.JobFilter{SessionID: "session-detach-managed"})
	require.NoError(t, err)
	require.Len(t, jobs, 1)
	assert.Equal(t, "echo detach-managed-ok", jobs[0].Command)
}

func TestShellDetachWithoutSubmitterKeepsLegacyPath(t *testing.T) {
	toolManager := NewDefaultManager(nil)
	// No SetBackgroundSubmitter call: the legacy fire-and-forget launcher stays.
	ctx := context.Background()
	output, err := toolManager.Execute(ctx, "shell", map[string]interface{}{
		"command": "echo legacy-detach",
		"detach":  true,
	})
	// The legacy path succeeds (or refuses on gate) but never reports the
	// managed marker.
	require.NoError(t, err)
	assert.NotContains(t, output, "受管")
}

func TestShellDetachOpensInteractiveSessionForServers(t *testing.T) {
	manager := background.NewManager(background.Config{})
	t.Cleanup(func() { require.NoError(t, manager.Close()) })

	toolManager := NewDefaultManager(nil)
	toolManager.SetBackgroundSubmitter(NewBackgroundSubmitter(manager))

	ctx := toolctx.WithSessionID(context.Background(), "session-detach-interactive")
	output, err := toolManager.Execute(ctx, "shell", map[string]interface{}{
		"command": "npm run dev",
		"detach":  true,
	})
	require.NoError(t, err)
	assert.Contains(t, output, "受管交互会话")

	jobs, err := manager.ListJobs(ctx, background.JobFilter{SessionID: "session-detach-interactive"})
	require.NoError(t, err)
	require.Len(t, jobs, 1)
	assert.Equal(t, "npm run dev", jobs[0].Command)
	assert.True(t, jobs[0].Interactive, "a dev-server detach must open an interactive session")

	_, err = manager.CancelJob(ctx, jobs[0].ID)
	require.NoError(t, err)
}
