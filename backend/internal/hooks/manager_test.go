package hooks

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type staticExecutor struct {
	decision Decision
	err      error
}

func (e *staticExecutor) Execute(ctx context.Context, hook HookConfig, payload map[string]interface{}) (Decision, error) {
	return e.decision, e.err
}

func TestManagerDispatchAggregatesNotifyAndEnrich(t *testing.T) {
	manager := NewManager([]HookConfig{
		{ID: "notify-1", Event: EventPreToolUse, Exec: ExecConfig{Type: "notify_stub"}},
		{ID: "enrich-1", Event: EventPreToolUse, Exec: ExecConfig{Type: "enrich_stub"}},
	})
	manager.executors["notify_stub"] = &staticExecutor{
		decision: Decision{
			Action:  DecisionNotify,
			Message: "notify message",
		},
	}
	manager.executors["enrich_stub"] = &staticExecutor{
		decision: Decision{
			Action: DecisionEnrich,
			ExtraContext: map[string]string{
				"ticket": "GW-123",
			},
		},
	}

	decision, err := manager.Dispatch(context.Background(), EventPreToolUse, map[string]interface{}{"tool_name": "edit"})
	require.NoError(t, err)
	assert.Equal(t, DecisionEnrich, decision.Action)
	assert.Equal(t, "notify message", decision.Message)
	assert.Equal(t, map[string]string{"ticket": "GW-123"}, decision.ExtraContext)
}
