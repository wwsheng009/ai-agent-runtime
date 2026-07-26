package agent

import (
	"context"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
	runtimeskill "github.com/wwsheng009/ai-agent-runtime/internal/skill"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolprotocol"
	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

type progressReportingMCPManager struct {
	mu       sync.Mutex
	reported bool
}

func (m *progressReportingMCPManager) FindTool(toolName string) (runtimeskill.ToolInfo, error) {
	return runtimeskill.ToolInfo{
		Name:    toolName,
		Enabled: true,
		MCPName: "progress-mcp",
	}, nil
}

func (m *progressReportingMCPManager) CallTool(ctx interface{}, mcpName, toolName string, args map[string]interface{}) (interface{}, error) {
	out, _, err := m.CallToolWithMeta(ctx, mcpName, toolName, args)
	return out, err
}

func (m *progressReportingMCPManager) CallToolWithMeta(ctx interface{}, mcpName, toolName string, args map[string]interface{}) (interface{}, map[string]interface{}, error) {
	callCtx, _ := ctx.(context.Context)
	if callCtx == nil {
		callCtx = context.Background()
	}
	toolprotocol.Report(callCtx, toolprotocol.Progress{
		Message: "halfway",
		Percent: 50,
		Partial: "chunk-1",
	})
	m.mu.Lock()
	m.reported = true
	m.mu.Unlock()
	return "done", map[string]interface{}{"phase": "complete"}, nil
}

func (m *progressReportingMCPManager) ListTools() []runtimeskill.ToolInfo {
	return []runtimeskill.ToolInfo{
		{Name: "progress_probe", Description: "Emit progress", Enabled: true, MCPName: "progress-mcp"},
	}
}

func TestExecuteToolCall_EmitsToolProgressEvent(t *testing.T) {
	manager := &progressReportingMCPManager{}
	agent := &Agent{
		config: &Config{
			Name:  "progress-agent",
			Model: "test-model",
		},
		mcpManager: manager,
	}

	bus := runtimeevents.NewBus()
	var (
		mu        sync.Mutex
		progress  []runtimeevents.Event
		completed []runtimeevents.Event
	)
	bus.Subscribe(toolprotocol.EventTypeProgress, func(event runtimeevents.Event) {
		mu.Lock()
		defer mu.Unlock()
		progress = append(progress, event)
	})
	bus.Subscribe("tool.completed", func(event runtimeevents.Event) {
		mu.Lock()
		defer mu.Unlock()
		completed = append(completed, event)
	})
	agent.SetEventBus(bus)

	call := types.ToolCall{
		ID:   "call-progress-1",
		Name: "progress_probe",
		Args: map[string]interface{}{"phase": "test"},
	}
	message, err := agent.ExecuteToolCall(context.Background(), "session-progress", call, nil, []types.ToolCall{call})
	require.NoError(t, err)
	require.NotNil(t, message)
	assert.Contains(t, message.Content, "done")

	manager.mu.Lock()
	reported := manager.reported
	manager.mu.Unlock()
	require.True(t, reported, "tool should have reported progress")

	mu.Lock()
	defer mu.Unlock()
	require.Len(t, progress, 1, "expected one tool.progress event")
	assert.Equal(t, toolprotocol.EventTypeProgress, progress[0].Type)
	assert.Equal(t, "session-progress", progress[0].SessionID)
	assert.Equal(t, "progress_probe", progress[0].ToolName)
	assert.Equal(t, "call-progress-1", progress[0].Payload["tool_call_id"])
	assert.Equal(t, "halfway", progress[0].Payload["message"])
	assert.Equal(t, float64(50), progress[0].Payload["percent"])
	assert.Equal(t, "chunk-1", progress[0].Payload["partial"])
	assert.Equal(t, string(toolprotocol.NotificationProgress), progress[0].Payload["kind"])
	require.NotEmpty(t, progress[0].Payload["trace_id"])
	require.Len(t, completed, 1)
}

func TestExecuteApprovedToolCall_EmitsToolProgressEvent(t *testing.T) {
	manager := &progressReportingMCPManager{}
	agent := &Agent{
		config: &Config{
			Name:  "progress-agent-approved",
			Model: "test-model",
		},
		mcpManager: manager,
	}

	bus := runtimeevents.NewBus()
	var (
		mu       sync.Mutex
		progress []runtimeevents.Event
	)
	bus.Subscribe(toolprotocol.EventTypeProgress, func(event runtimeevents.Event) {
		mu.Lock()
		defer mu.Unlock()
		progress = append(progress, event)
	})
	agent.SetEventBus(bus)

	call := types.ToolCall{
		ID:   "call-progress-approved",
		Name: "progress_probe",
		Args: map[string]interface{}{},
	}
	message, err := agent.ExecuteApprovedToolCall(context.Background(), "session-progress-approved", call, nil)
	require.NoError(t, err)
	require.NotNil(t, message)

	mu.Lock()
	defer mu.Unlock()
	require.Len(t, progress, 1)
	assert.Equal(t, "call-progress-approved", progress[0].Payload["tool_call_id"])
	assert.Equal(t, "session-progress-approved", progress[0].SessionID)
}

func TestWithToolProgressReporter_FillsDefaults(t *testing.T) {
	agent := &Agent{config: &Config{Name: "defaults-agent"}}
	bus := runtimeevents.NewBus()
	var got runtimeevents.Event
	bus.Subscribe(toolprotocol.EventTypeProgress, func(event runtimeevents.Event) {
		got = event
	})
	agent.SetEventBus(bus)

	ctx := agent.withToolProgressReporter(context.Background(), "sess-1", "trace-1", types.ToolCall{
		ID:   "call-x",
		Name: "shell",
	})
	toolprotocol.Report(ctx, toolprotocol.Progress{Message: "tick", Percent: 10})
	assert.Equal(t, "shell", got.ToolName)
	assert.Equal(t, "call-x", got.Payload["tool_call_id"])
	assert.Equal(t, "trace-1", got.Payload["trace_id"])
	assert.Equal(t, "sess-1", got.Payload["session_id"])
	assert.NotEmpty(t, got.Payload["timestamp"])
}
