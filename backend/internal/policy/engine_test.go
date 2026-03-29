package policy

import (
	"context"
	"testing"

	runtimehooks "github.com/ai-gateway/ai-agent-runtime/internal/hooks"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type staticHookDispatcher struct {
	decision runtimehooks.Decision
	err      error
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
