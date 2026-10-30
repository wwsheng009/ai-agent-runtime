package tools

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wwsheng009/ai-agent-runtime/internal/background"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolkit/tools"
)

func TestShellJobRuntimeWiresShellTool(t *testing.T) {
	manager := background.NewManager(background.Config{})
	t.Cleanup(func() { require.NoError(t, manager.Close()) })

	toolManager := NewDefaultManager(nil)
	toolManager.SetShellJobRuntime(NewShellJobRuntime(manager))

	shell, ok := toolManager.toolkit.Get("shell")
	require.True(t, ok, "shell must be registered")
	_, wired := shell.(interface{ SetShellJobRuntime(tools.ShellJobRuntime) })
	require.True(t, wired, "shell tool must expose the job runtime seam")
}
