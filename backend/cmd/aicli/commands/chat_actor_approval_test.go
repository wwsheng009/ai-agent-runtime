package commands

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	"github.com/wwsheng009/ai-agent-runtime/internal/supervision"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolbroker"
)

// TestLocalAgentApprovalBridgeProjectsDigestAndResolves verifies the CLI host
// mirrors the API P0-2 bridge: a child approval edge reaches the durable
// parent inbox with its request details and the resolution edge clears it.
func TestLocalAgentApprovalBridgeProjectsDigestAndResolves(t *testing.T) {
	host := newLocalSupervisionTestHost(t)
	host.SessionStore = runtimechat.NewInMemoryStorage()
	registry := &localActorRegistry{Host: host}
	ctx := context.Background()

	child := runtimechat.NewSession("user-cli-approval")
	child.ID = "child-cli-approval-1"
	child.SetContext(toolbroker.AgentSessionContextParentSessionID, "parent-cli-1")
	require.NoError(t, host.SessionStore.Save(ctx, child))

	const childPath = "/root/child-cli-approval-1"
	registry.projectLocalAgentApproval(ctx, "parent-cli-1", child.ID, childPath, runtimechat.EventApprovalRequested, map[string]interface{}{
		"request_id": "req-cli-1",
		"tool_name":  "write_file",
	})

	digest, err := supervision.BuildDigest(ctx, host.Supervision.Store, supervision.DigestRequest{
		RootScopeID:           "parent-cli-1",
		TargetParentSessionID: "parent-cli-1",
	})
	require.NoError(t, err)
	require.Equal(t, 1, digest.ActionRequired)
	require.Contains(t, digest.Text, "request_id=req-cli-1")
	require.Contains(t, digest.Text, "tool=write_file")
	require.Contains(t, digest.Text, childPath)

	registry.resolveLocalAgentApproval(ctx, "parent-cli-1", child.ID, childPath, runtimechat.EventApprovalResolved, map[string]interface{}{
		"request_id": "req-cli-1",
		"allowed":    true,
	})

	digest, err = supervision.BuildDigest(ctx, host.Supervision.Store, supervision.DigestRequest{
		RootScopeID:           "parent-cli-1",
		TargetParentSessionID: "parent-cli-1",
	})
	require.NoError(t, err)
	require.Equal(t, 0, digest.ActionRequired)
	require.Empty(t, digest.Items)
}

// TestLocalAgentQuestionBridgeStaysDigestOnly verifies the CLI host does not
// auto-wake the parent for a child question.
func TestLocalAgentQuestionBridgeStaysDigestOnly(t *testing.T) {
	host := newLocalSupervisionTestHost(t)
	host.SessionStore = runtimechat.NewInMemoryStorage()
	registry := &localActorRegistry{Host: host}
	ctx := context.Background()

	child := runtimechat.NewSession("user-cli-question")
	child.ID = "child-cli-question-1"
	child.SetContext(toolbroker.AgentSessionContextParentSessionID, "parent-cli-2")
	require.NoError(t, host.SessionStore.Save(ctx, child))

	registry.projectLocalAgentApproval(ctx, "parent-cli-2", child.ID, "", runtimechat.EventQuestionAsked, map[string]interface{}{
		"question_id": "q-cli-1",
		"prompt":      "Which environment should the migration target?",
	})

	digest, err := supervision.BuildDigest(ctx, host.Supervision.Store, supervision.DigestRequest{
		RootScopeID:           "parent-cli-2",
		TargetParentSessionID: "parent-cli-2",
	})
	require.NoError(t, err)
	require.Equal(t, 1, digest.ActionRequired)
	require.Contains(t, digest.Text, "q-cli-1")
	require.Contains(t, digest.Text, "Which environment")

	pending, err := host.Supervision.Store.ListWakePending(ctx, supervision.WakeFilter{
		RootScopeID:          "parent-cli-2",
		TargetParentSessionID: "parent-cli-2",
		UnclaimedOnly:        true,
	})
	require.NoError(t, err)
	require.Empty(t, pending, "questions stay digest-only")
}
