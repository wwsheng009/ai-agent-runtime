package tools

import (
	"context"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/toolkit"
)

// These helpers preserve focused unit coverage for pre-job executor behavior.
// Their implementations live in _test.go; production tools never set these
// hooks and therefore require the shared job runtime.
func newTestBashTool() *BashTool {
	tool := NewBashTool()
	tool.directExecutor = func(ctx context.Context, owner *BashTool, command, workdir string, timeout time.Duration, timeoutSource string, captureSettings outputCaptureSettings, mutatedPaths []string) (*toolkit.ToolResult, error) {
		return owner.executeDirectForTests(ctx, command, workdir, timeout, timeoutSource, captureSettings, mutatedPaths)
	}
	tool.batchExecutor = func(ctx context.Context, parent map[string]interface{}, commands []bashCommandBatchItem) (*toolkit.ToolResult, error) {
		return tool.executeBatch(ctx, parent, commands)
	}
	return tool
}

func newTestShellTool() *ShellTool {
	tool := NewShellTool()
	tool.BashTool = newTestBashTool()
	return tool
}

func newTestExecuteShellCommandTool() *ExecuteShellCommandTool {
	tool := NewExecuteShellCommandTool()
	tool.BashTool = newTestBashTool()
	return tool
}
