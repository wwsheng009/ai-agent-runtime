package commands

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	agentcontrol "github.com/wwsheng009/ai-agent-runtime/internal/agentcontrol"
)

// TestLocalRegistryMaterialize_UnchangedPassDoesNotAppendWakeEvents pins the
// write-amplification fix end to end: every spawn gate, list refresh and the
// periodic reconcile re-runs materialization over all sessions, so a pass that
// changes nothing must not append wake events. The durable wake log tracks
// identity changes, not the reconcile cadence; before the fix each pass appended
// one "upsert" event per projected row.
func TestLocalRegistryMaterialize_UnchangedPassDoesNotAppendWakeEvents(t *testing.T) {
	ctx := context.Background()
	host, rootSession, agentStore := newLocalQuotaHarness(t, 2, 0)

	require.NoError(t, host.ActorRegistry.materializeLocalAgentRegistry(ctx))
	firstSeq, err := agentStore.LastAgentControlAgentWakeSeq(ctx, agentcontrol.AgentWakeFilter{RootSessionID: rootSession.ID})
	require.NoError(t, err)
	require.Greater(t, firstSeq, int64(0), "the first pass writes the root and child identities")

	for i := 0; i < 3; i++ {
		require.NoError(t, host.ActorRegistry.materializeLocalAgentRegistry(ctx))
	}
	repeatedSeq, err := agentStore.LastAgentControlAgentWakeSeq(ctx, agentcontrol.AgentWakeFilter{RootSessionID: rootSession.ID})
	require.NoError(t, err)
	require.Equal(t, firstSeq, repeatedSeq, "unchanged projection passes must not append wake events")
}
