package runtimeapi

import (
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/wwsheng009/ai-agent-runtime/internal/chat"
	"github.com/wwsheng009/ai-agent-runtime/internal/planmode"
	runtimepolicy "github.com/wwsheng009/ai-agent-runtime/internal/policy"
	"github.com/wwsheng009/ai-agent-runtime/internal/sessionmeta"
)

func TestInitialAgentChatPermissionModeRequiresNewSession(t *testing.T) {
	tests := []struct {
		name        string
		request     agentChatRequest
		wantMode    runtimepolicy.Mode
		wantApply   bool
		wantMessage string
	}{
		{
			name:      "safe mode",
			request:   agentChatRequest{PermissionMode: "accept_edits"},
			wantMode:  runtimepolicy.ModeAcceptEdits,
			wantApply: true,
		},
		{
			name:      "plan mode",
			request:   agentChatRequest{PermissionMode: "plan"},
			wantMode:  runtimepolicy.ModePlan,
			wantApply: true,
		},
		{
			name:        "bypass requires confirmation",
			request:     agentChatRequest{PermissionMode: "bypass_permissions"},
			wantMessage: "permission_mode=bypass_permissions requires permission_mode_confirm=true",
		},
		{
			name:      "confirmed bypass",
			request:   agentChatRequest{PermissionMode: "bypass_permissions", PermissionModeConfirm: true},
			wantMode:  runtimepolicy.ModeBypassPermissions,
			wantApply: true,
		},
		{
			name:        "existing session cannot override",
			request:     agentChatRequest{SessionID: "session-1", PermissionMode: "accept_edits"},
			wantMessage: "permission_mode may only initialize a new session",
		},
		{
			name:        "confirmation without mode is rejected",
			request:     agentChatRequest{PermissionModeConfirm: true},
			wantMessage: "permission_mode_confirm requires permission_mode=bypass_permissions",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mode, apply, err := initialAgentChatPermissionMode(&tt.request)
			if tt.wantMessage != "" {
				require.EqualError(t, err, tt.wantMessage)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tt.wantMode, mode)
			require.Equal(t, tt.wantApply, apply)
		})
	}
}

func TestApplyInitialAgentChatPermissionModeUsesDurablePlanEntry(t *testing.T) {
	session := chat.NewSession("user-1")

	require.NoError(t, applyInitialAgentChatPermissionMode(session, runtimepolicy.ModePlan))

	state := planmode.Load(session)
	require.True(t, planmode.IsActive(state))
	require.Equal(t, planmode.DefaultPlanPath, state.PlanPath)
	require.Equal(t, string(runtimepolicy.ModePlan),
		sessionmeta.String(session.Metadata.Context, sessionmeta.PermissionMode))
}

func TestApplyInitialAgentChatPermissionModePersistsSafeModes(t *testing.T) {
	session := chat.NewSession("user-1")

	require.NoError(t,
		applyInitialAgentChatPermissionMode(session, runtimepolicy.ModeDontAsk))

	require.Equal(t, string(runtimepolicy.ModeDontAsk),
		sessionmeta.String(session.Metadata.Context, sessionmeta.PermissionMode))
	require.Equal(t, string(runtimepolicy.ModeDontAsk),
		sessionmeta.String(session.Metadata.Context, sessionmeta.EffectivePermissionMode))
}
