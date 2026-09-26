package runtimeapi

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	runtimeagent "github.com/wwsheng009/ai-agent-runtime/internal/agent"
	"github.com/wwsheng009/ai-agent-runtime/internal/agentcontrol"
	runtimecfg "github.com/wwsheng009/ai-agent-runtime/internal/config"
)

// wait_team resolves its observation window inside the tool broker, so an
// operator config honored by wait_agent / read_agent_events must reach the
// broker as well. This guards the host wiring itself: if the stamp is dropped
// (or moved into a branch a plain session does not take), wait_team silently
// falls back to the shared defaults and agents.maxWaitTimeoutMs stops bounding
// every wait path — the exact drift P2-11 asks to rule out.
func TestApplyAgentRuntimeServicesStampsBrokerWaitTimeoutPolicy(t *testing.T) {
	cfg := runtimecfg.DefaultRuntimeConfig()
	cfg.Agents.DefaultWaitTimeoutMs = 45000
	cfg.Agents.MinWaitTimeoutMs = 20000
	cfg.Agents.MaxWaitTimeoutMs = 60000
	cfg.Agents.WaitTimeoutMode = agentcontrol.WaitTimeoutModeError

	handler := &Handler{runtimeConfig: cfg}
	apiAgent := runtimeagent.NewAgent(&runtimeagent.Config{Name: "wait-policy-wiring", Model: "test-model"}, nil)

	handler.applyAgentRuntimeServices(apiAgent, nil)

	broker := apiAgent.GetToolBroker()
	require.NotNil(t, broker, "expected the runtime services pass to install a tool broker")
	require.NotNil(t, broker.WaitTimeoutPolicy, "the broker must resolve wait_team windows from the live agents policy")

	policy := broker.WaitTimeoutPolicy().Normalize()
	assert.Equal(t, 45000, policy.DefaultMs)
	assert.Equal(t, 20000, policy.MinMs)
	assert.Equal(t, 60000, policy.MaxMs)
	assert.Equal(t, agentcontrol.WaitTimeoutModeError, policy.Mode)

	// The provider has to read the handler config per call instead of caching the
	// value at stamp time: reloading the config must move the next wait window.
	handler.runtimeConfig = nil
	fallback := broker.WaitTimeoutPolicy().Normalize()
	assert.Equal(t, agentcontrol.DefaultWaitTimeoutMs, fallback.DefaultMs)
	assert.Equal(t, agentcontrol.WaitTimeoutModeClamp, fallback.Mode)
}
