package toolbroker

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolresult"
	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

type capturingPlanModeController struct {
	enterCalls []EnterPlanModeArgs
	exitCalls  []ExitPlanModeArgs
	sessionIDs []string
	enterRes   *PlanModeResult
	exitRes    *PlanModeResult
	enterErr   error
	exitErr    error
}

func (c *capturingPlanModeController) EnterPlanMode(ctx context.Context, sessionID string, args EnterPlanModeArgs) (*PlanModeResult, error) {
	c.sessionIDs = append(c.sessionIDs, sessionID)
	c.enterCalls = append(c.enterCalls, args)
	if c.enterErr != nil {
		return nil, c.enterErr
	}
	if c.enterRes != nil {
		return c.enterRes, nil
	}
	return &PlanModeResult{
		Active:         true,
		Status:         "active",
		PlanPath:       strings.TrimSpace(args.PlanPath),
		PermissionMode: "plan",
		PreviousMode:   "default",
	}, nil
}

func (c *capturingPlanModeController) ExitPlanMode(ctx context.Context, sessionID string, args ExitPlanModeArgs) (*PlanModeResult, error) {
	c.sessionIDs = append(c.sessionIDs, sessionID)
	c.exitCalls = append(c.exitCalls, args)
	if c.exitErr != nil {
		return nil, c.exitErr
	}
	if c.exitRes != nil {
		return c.exitRes, nil
	}
	return &PlanModeResult{
		Active:         false,
		Status:         "exited",
		PermissionMode: "default",
		ExitDecision:   args.Decision,
		Notes:          args.Notes,
	}, nil
}

func TestBrokerDefinitionsIncludePlanModeToolsWhenControllerSet(t *testing.T) {
	without := (&Broker{}).Definitions()
	for _, def := range without {
		if def.Name == ToolEnterPlanMode || def.Name == ToolExitPlanMode {
			t.Fatalf("expected plan mode tools absent without controller, found %s", def.Name)
		}
	}

	with := (&Broker{PlanMode: &capturingPlanModeController{}}).Definitions()
	var foundEnter, foundExit bool
	for _, def := range with {
		switch def.Name {
		case ToolEnterPlanMode:
			foundEnter = true
		case ToolExitPlanMode:
			foundExit = true
			required, _ := def.Parameters["required"].([]string)
			assert.Contains(t, required, "decision")
		}
	}
	require.True(t, foundEnter, "enter_plan_mode definition missing")
	require.True(t, foundExit, "exit_plan_mode definition missing")
}

func TestBrokerExecuteEnterPlanMode(t *testing.T) {
	ctrl := &capturingPlanModeController{
		enterRes: &PlanModeResult{
			Active:         true,
			Status:         "active",
			PlanPath:       "docs/feature-plan.md",
			PermissionMode: "plan",
			PreviousMode:   "accept_edits",
		},
	}
	broker := &Broker{PlanMode: ctrl}

	raw, meta, err := broker.ExecuteToolCall(context.Background(), "session-plan", types.ToolCall{
		ID:   "call_enter_1",
		Name: ToolEnterPlanMode,
		Args: map[string]interface{}{
			"plan_path": "docs/feature-plan.md",
		},
	})
	require.NoError(t, err)
	result, ok := raw.(*PlanModeResult)
	require.True(t, ok, "got %T", raw)
	assert.True(t, result.Active)
	assert.Equal(t, "docs/feature-plan.md", result.PlanPath)
	assert.Equal(t, "plan", result.PermissionMode)
	assert.Equal(t, toolresult.KindStructured, meta[toolresult.MetadataKey])
	assert.Equal(t, true, meta["active"])
	assert.Equal(t, "plan", meta["permission_mode"])
	require.Len(t, ctrl.enterCalls, 1)
	assert.Equal(t, "docs/feature-plan.md", ctrl.enterCalls[0].PlanPath)
	assert.Equal(t, []string{"session-plan"}, ctrl.sessionIDs)
}

func TestBrokerExecuteExitPlanModeRequiresDecision(t *testing.T) {
	broker := &Broker{PlanMode: &capturingPlanModeController{}}
	_, _, err := broker.ExecuteToolCall(context.Background(), "session-plan", types.ToolCall{
		ID:   "call_exit_missing",
		Name: ToolExitPlanMode,
		Args: map[string]interface{}{},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "decision is required")
}

func TestBrokerExecuteExitPlanMode(t *testing.T) {
	ctrl := &capturingPlanModeController{}
	broker := &Broker{PlanMode: ctrl}

	raw, meta, err := broker.ExecuteToolCall(context.Background(), "session-plan", types.ToolCall{
		ID:   "call_exit_1",
		Name: "exit-plan-mode", // alias normalization
		Args: map[string]interface{}{
			"decision": "approve",
			"notes":    "ship it",
		},
	})
	require.NoError(t, err)
	result, ok := raw.(*PlanModeResult)
	require.True(t, ok, "got %T", raw)
	assert.False(t, result.Active)
	assert.Equal(t, "approve", result.ExitDecision)
	assert.Equal(t, "ship it", result.Notes)
	assert.Equal(t, "approve", meta["exit_decision"])
	require.Len(t, ctrl.exitCalls, 1)
	assert.Equal(t, "approve", ctrl.exitCalls[0].Decision)
	assert.Equal(t, "ship it", ctrl.exitCalls[0].Notes)
}

func TestBrokerExecutePlanModeWithoutController(t *testing.T) {
	broker := &Broker{}
	_, _, err := broker.ExecuteToolCall(context.Background(), "session-plan", types.ToolCall{
		ID:   "call_enter_missing",
		Name: ToolEnterPlanMode,
		Args: map[string]interface{}{},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "plan mode controller is not configured")

	_, _, err = broker.ExecuteToolCall(context.Background(), "session-plan", types.ToolCall{
		ID:   "call_exit_missing_ctrl",
		Name: ToolExitPlanMode,
		Args: map[string]interface{}{"decision": "quit"},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "plan mode controller is not configured")
}

func TestNormalizeToolNamePlanMode(t *testing.T) {
	assert.Equal(t, ToolEnterPlanMode, normalizeToolName(ToolEnterPlanMode))
	assert.Equal(t, ToolExitPlanMode, normalizeToolName(ToolExitPlanMode))
	assert.Equal(t, ToolEnterPlanMode, normalizeToolName("EnterPlanMode"))
	assert.Equal(t, ToolExitPlanMode, normalizeToolName("exit-plan-mode"))

	broker := &Broker{}
	assert.True(t, broker.IsBrokerTool(ToolEnterPlanMode))
	assert.True(t, broker.IsBrokerTool(ToolExitPlanMode))
	assert.True(t, broker.IsBrokerTool("EnterPlanMode"))
	assert.True(t, broker.IsBrokerTool("exit-plan-mode"))
}
