package tools

import (
	"context"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wwsheng009/ai-agent-runtime/internal/background"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolctx"
)

func TestShellJobFirstUsesSharedBackgroundManager(t *testing.T) {
	manager := background.NewManager(background.Config{})
	t.Cleanup(func() { _ = manager.Close() })

	toolManager := NewDefaultManager(nil)
	toolManager.SetShellJobRuntime(NewShellJobRuntime(manager))
	ctx := toolctx.WithSessionID(context.Background(), "session-job-first")

	output, err := toolManager.Execute(ctx, "shell", map[string]interface{}{
		"command": "echo job-first-marker",
	})
	require.NoError(t, err)
	require.Contains(t, output, "job-first-marker")
	require.Contains(t, output, "Exit code: 0")

	// A quick foreground command is forgotten; it must not remain as a model
	// invisible job in the shared manager.
	jobs, listErr := manager.ListJobs(ctx, background.JobFilter{SessionID: "session-job-first"})
	require.NoError(t, listErr)
	require.Empty(t, jobs)
}

func TestShellJobFirstPromotesTimeout(t *testing.T) {
	manager := background.NewManager(background.Config{})
	t.Cleanup(func() { _ = manager.Close() })

	toolManager := NewDefaultManager(nil)
	toolManager.SetShellJobRuntime(NewShellJobRuntime(manager))
	ctx := toolctx.WithSessionID(context.Background(), "session-job-promoted")

	command := "sleep 30"
	if runtime.GOOS == "windows" {
		command = "Start-Sleep -Seconds 30"
	}
	output, err := toolManager.Execute(ctx, "shell", map[string]interface{}{
		"command":     command,
		"timeout_sec": 1,
	})
	require.NoError(t, err)
	require.True(t, strings.Contains(output, "moved to background job job_"), output)

	jobs, listErr := manager.ListJobs(ctx, background.JobFilter{SessionID: "session-job-promoted"})
	require.NoError(t, listErr)
	require.Len(t, jobs, 1)
	require.Equal(t, background.StatusRunning, jobs[0].Status)
	_, cancelErr := manager.CancelJob(ctx, jobs[0].ID)
	require.NoError(t, cancelErr)
}
