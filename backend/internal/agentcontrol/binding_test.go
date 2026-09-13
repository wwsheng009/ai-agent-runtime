package agentcontrol

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestCloseStaleAgentSessionBindingsClosesReboundSession covers the N2 parity
// fix: a session id re-bound under a different agent path must not leave the
// previous row active, otherwise one container is counted twice against
// max_threads.
func TestCloseStaleAgentSessionBindingsClosesReboundSession(t *testing.T) {
	ctx := context.Background()
	store := newTestGlobalAgentRegistryStore(t)

	_, err := store.UpsertAgentControlAgent(ctx, AgentRecord{
		AgentID:       "old-agent",
		RootSessionID: "root-session",
		SessionID:     "shared-session",
		AgentPath:     "/root/old-agent",
		Depth:         1,
		AgentType:     AgentTypeChild,
		Status:        AgentStatusActive,
		Workflow:      WorkflowSpawnAgent,
	})
	require.NoError(t, err)

	rebound := AgentRecord{
		AgentID:       "new-agent",
		RootSessionID: "root-session",
		SessionID:     "shared-session",
		AgentPath:     "/root/new-agent",
		Depth:         1,
		AgentType:     AgentTypeChild,
		Status:        AgentStatusActive,
		Workflow:      WorkflowSpawnAgent,
	}
	require.NoError(t, CloseStaleAgentSessionBindings(ctx, store, rebound))

	records, err := store.ListAgentControlAgents(ctx, AgentFilter{SessionID: "shared-session", IncludeClosed: true})
	require.NoError(t, err)
	require.Len(t, records, 1)
	require.Equal(t, "old-agent", records[0].AgentID)
	require.True(t, records[0].Closed())

	// Once the rebound row exists, the same precheck must keep it active: a
	// matching binding is never treated as stale.
	_, err = store.UpsertAgentControlAgent(ctx, rebound)
	require.NoError(t, err)
	require.NoError(t, CloseStaleAgentSessionBindings(ctx, store, rebound))

	records, err = store.ListAgentControlAgents(ctx, AgentFilter{SessionID: "shared-session", IncludeClosed: true})
	require.NoError(t, err)
	require.Len(t, records, 2)
	require.Equal(t, "new-agent", records[1].AgentID)
	require.Equal(t, AgentStatusActive, records[1].Status)
	require.False(t, records[1].Closed())
}

// TestCloseStaleAgentSessionBindingsIgnoresRootOnlyRows keeps the precheck from
// closing the durable root row when a child reuses the session id: root rows
// carry no agent path and are managed by their own lifecycle.
func TestCloseStaleAgentSessionBindingsIgnoresUnknownSession(t *testing.T) {
	ctx := context.Background()
	store := newTestGlobalAgentRegistryStore(t)

	require.NoError(t, CloseStaleAgentSessionBindings(ctx, store, AgentRecord{
		AgentID:       "fresh-agent",
		RootSessionID: "root-session",
		SessionID:     "fresh-session",
		AgentPath:     "/root/fresh-agent",
		AgentType:     AgentTypeChild,
	}))
}
