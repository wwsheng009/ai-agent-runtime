package policy

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	runtimehooks "github.com/wwsheng009/ai-agent-runtime/internal/hooks"
)

type staticHookDispatcher struct {
	decision runtimehooks.Decision
	err      error
}

type captureApprovalHandler struct {
	request ApprovalRequest
}

func (h *captureApprovalHandler) RequestApproval(_ context.Context, req ApprovalRequest) (ApprovalResponse, error) {
	h.request = req
	return ApprovalResponse{Allowed: true}, nil
}

func (d staticHookDispatcher) Dispatch(ctx context.Context, event runtimehooks.Event, payload map[string]interface{}) (runtimehooks.Decision, error) {
	return d.decision, d.err
}

func TestEngineEvaluatePreservesHookNotifyAndEnrichMetadata(t *testing.T) {
	engine := &Engine{
		Hooks: staticHookDispatcher{
			decision: runtimehooks.Decision{
				Action:  runtimehooks.DecisionEnrich,
				Message: "approval context",
				ExtraContext: map[string]string{
					"ticket": "GW-123",
				},
			},
		},
	}

	decision, err := engine.Evaluate(context.Background(), EvalRequest{
		ToolName: "read_task_spec",
	})
	require.NoError(t, err)
	assert.Equal(t, DecisionAllow, decision.Type)
	assert.Equal(t, "approval context", decision.HookMessage)
	assert.Equal(t, map[string]string{"ticket": "GW-123"}, decision.HookContext)
}

func TestEngineResolveAskAssignsApprovalExpiry(t *testing.T) {
	handler := &captureApprovalHandler{}
	engine := &Engine{
		AskHandler:      handler,
		ApprovalTimeout: 2 * time.Minute,
	}
	startedAt := time.Now().UTC()

	decision, err := engine.resolveAsk(context.Background(), Decision{Type: DecisionAsk}, EvalRequest{
		SessionID:  "session-expiry",
		ToolCallID: "tool-expiry",
		ToolName:   "write_file",
	})
	require.NoError(t, err)
	assert.Equal(t, DecisionAllow, decision.Type)
	assert.WithinDuration(t, startedAt.Add(2*time.Minute), handler.request.ExpiresAt, time.Second)
}

func TestEngineUsesCapabilityScopeBeforeApprovalDecision(t *testing.T) {
	engine := &Engine{
		Mode:   ModeBypassPermissions,
		Policy: NewCapabilityScopedToolExecutionPolicy(nil, []Capability{CapReadOnly}),
	}
	decision, err := engine.Evaluate(context.Background(), EvalRequest{ToolName: "spawn_agent"})
	require.NoError(t, err)
	assert.Equal(t, DecisionDeny, decision.Type)
	assert.Contains(t, decision.Reason, "agent_management")

	decision, err = engine.Evaluate(context.Background(), EvalRequest{ToolName: "list_agents"})
	require.NoError(t, err)
	assert.Equal(t, DecisionAllow, decision.Type)
}
