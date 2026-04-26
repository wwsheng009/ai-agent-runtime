package agent

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

func TestExecuteToolCall_PreservesRawOutputOnToolError(t *testing.T) {
	agent := &Agent{
		config: &Config{
			Name:  "test-agent",
			Model: "test-model",
		},
		mcpManager: &MockRichSequenceMCPManager{
			output: "stderr line 1\nstderr line 2",
			err:    fmt.Errorf("exit status 1"),
		},
	}

	bus := runtimeevents.NewBus()
	var completed []runtimeevents.Event
	bus.Subscribe("tool.completed", func(event runtimeevents.Event) {
		completed = append(completed, event)
	})
	agent.SetEventBus(bus)

	call := types.ToolCall{
		ID:   "call-tool-error",
		Name: "execute_shell_command",
		Args: map[string]interface{}{
			"command": "git log --oneline --all | head -60",
		},
	}

	message, err := agent.ExecuteToolCall(context.Background(), "session-tool-error", call, nil, []types.ToolCall{call})
	require.NoError(t, err)
	require.NotNil(t, message)
	assert.Equal(t, "Tool execution failed: exit status 1\nstderr line 1\nstderr line 2", message.Content)
	require.NotNil(t, message.Metadata)
	assert.Equal(t, "exit status 1", message.Metadata["tool_error"])

	require.Len(t, completed, 1)
	assert.Equal(t, "exit status 1", completed[0].Payload["error"])
	summaryLines, ok := completed[0].Payload["summary_lines"].([]string)
	require.True(t, ok, "expected []string summary_lines, got %#v", completed[0].Payload["summary_lines"])
	assert.Equal(t, []string{"stderr line 1", "stderr line 2"}, summaryLines)
}

func TestExecuteApprovedToolCall_PreservesRawOutputOnToolError(t *testing.T) {
	agent := &Agent{
		config: &Config{
			Name:  "test-agent",
			Model: "test-model",
		},
		mcpManager: &MockRichSequenceMCPManager{
			output: "error: invalid option: --no-stat",
			err:    fmt.Errorf("exit status 1"),
		},
	}

	bus := runtimeevents.NewBus()
	var completed []runtimeevents.Event
	bus.Subscribe("tool.completed", func(event runtimeevents.Event) {
		completed = append(completed, event)
	})
	agent.SetEventBus(bus)

	call := types.ToolCall{
		ID:   "call-approved-tool-error",
		Name: "execute_shell_command",
		Args: map[string]interface{}{
			"command": "git diff --no-color --no-stat",
		},
	}

	message, err := agent.ExecuteApprovedToolCall(context.Background(), "session-approved-tool-error", call, nil)
	require.NoError(t, err)
	require.NotNil(t, message)
	assert.Equal(t, "Tool execution failed: exit status 1\nerror: invalid option: --no-stat", message.Content)
	require.NotNil(t, message.Metadata)
	assert.Equal(t, "exit status 1", message.Metadata["tool_error"])

	require.Len(t, completed, 1)
	assert.Equal(t, "exit status 1", completed[0].Payload["error"])
	summaryLines, ok := completed[0].Payload["summary_lines"].([]string)
	require.True(t, ok, "expected []string summary_lines, got %#v", completed[0].Payload["summary_lines"])
	assert.Equal(t, []string{"error: invalid option: --no-stat"}, summaryLines)
}
