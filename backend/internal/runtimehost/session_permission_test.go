package runtimehost

import (
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/wwsheng009/ai-agent-runtime/internal/agent"
	runtimepolicy "github.com/wwsheng009/ai-agent-runtime/internal/policy"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolbroker"
)

func TestSessionPermissionModeExtractionAndApplication(t *testing.T) {
	for _, mode := range []string{"bypass_permissions", "plan", "dont_ask", "default", "accept_edits"} {
		t.Run(mode, func(t *testing.T) {
			session := sessionWithContext(map[string]interface{}{
				toolbroker.AgentSessionContextPermissionMode:          mode,
				toolbroker.AgentSessionContextRequestedPermissionMode: "plan",
			})
			sessionCtx := ExtractSessionContext(session)
			require.Equal(t, mode, sessionCtx.PermissionMode)
			a := agent.NewAgent(&agent.Config{Name: "child"}, nil)
			policy := runtimepolicy.NewToolExecutionPolicy(nil, true)
			a.SetToolExecutionPolicy(policy)
			engine := &runtimepolicy.Engine{Policy: policy, DisableBypass: true}
			a.SetPermissionEngine(engine)
			ApplySessionPermissionMode(a, sessionCtx.PermissionMode)
			require.Same(t, engine, a.GetPermissionEngine())
			require.Equal(t, runtimepolicy.Mode(mode), engine.Mode)
			require.True(t, engine.DisableBypass, "restoring a mode must not drop security overlays")
			require.True(t, a.GetToolExecutionPolicy().ReadOnly)
			if mode == "plan" {
				require.NotEmpty(t, engine.PlanWriteAllowPaths)
			}
		})
	}
}

func TestApplySessionPermissionModeIgnoresMissingAndInvalidModes(t *testing.T) {
	a := agent.NewAgent(&agent.Config{Name: "child"}, nil)
	for _, raw := range []string{"", "unknown"} {
		ApplySessionPermissionMode(a, raw)
		require.Nil(t, a.GetPermissionEngine())
	}
	ApplySessionPermissionMode(a, "dont_ask")
	ApplySessionPermissionMode(a, "unknown")
	require.Equal(t, runtimepolicy.ModeDontAsk, a.GetPermissionEngine().Mode)
	ApplySessionPermissionMode(nil, "plan")
}
