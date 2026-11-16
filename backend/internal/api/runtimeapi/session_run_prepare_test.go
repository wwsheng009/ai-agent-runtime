package runtimeapi

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/wwsheng009/ai-agent-runtime/internal/agent"
	"github.com/wwsheng009/ai-agent-runtime/internal/chat"
	"github.com/wwsheng009/ai-agent-runtime/internal/planmode"
	runtimepolicy "github.com/wwsheng009/ai-agent-runtime/internal/policy"
	"github.com/wwsheng009/ai-agent-runtime/internal/sessionmeta"
)

func TestPrepareSessionAgentForRunRefreshesRootsPromptAndPlanMode(t *testing.T) {
	ctx := context.Background()
	handler := NewHandler(nil, nil, nil)
	session := chat.NewSession("run-prepare-user")
	allowedRoot := filepath.Join(t.TempDir(), "allowed")
	session.SetContext(chat.AllowedRootsContextKey, []string{allowedRoot})
	session.SetContext(sessionmeta.PermissionMode, string(runtimepolicy.ModeDefault))
	planmode.Save(session, planmode.Enter(string(runtimepolicy.ModeDefault), "plan.md"))

	apiAgent := agent.NewAgent(&agent.Config{
		Name:         "run-prepare",
		Model:        "test-model",
		SystemPrompt: "composed-head",
		Options:      map[string]interface{}{"stale": true},
	}, nil)

	require.NoError(t, handler.prepareSessionAgentForRun(ctx, apiAgent, nil, true, session))
	require.Equal(t, "composed-head", sessionmeta.String(session.Metadata.Context, sessionmeta.SystemPromptFrozen))
	require.Equal(t, []string{filepath.Clean(allowedRoot)}, apiAgent.GetConfig().Options["allowed_roots"])
	require.NotNil(t, apiAgent.GetPermissionEngine())
	require.Equal(t, runtimepolicy.ModePlan, apiAgent.GetPermissionEngine().Mode)

	// A later run reuses the anchor instead of accepting a rebuilt prompt.
	apiAgent.GetConfig().SystemPrompt = "rewritten-head"
	session.SetContext(sessionmeta.SystemPromptFrozen, "anchored-head")
	require.NoError(t, handler.prepareSessionAgentForRun(ctx, apiAgent, nil, true, session))
	require.Equal(t, "anchored-head", apiAgent.GetConfig().SystemPrompt)
}
