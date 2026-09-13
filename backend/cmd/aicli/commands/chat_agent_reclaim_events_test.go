package commands

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/wwsheng009/ai-agent-runtime/internal/agentcontrol"
	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolbroker"
)

// TestLocalActorRegistry_ReclaimPublishesProductEventAndDebugSummary pins plan
// §P2-8 方案 4 end to end on the CLI host: an automatic eviction performed by
// the spawn gate reaches the runtime event bus, gets mirrored into the durable
// parent session event stream, and shows up in the /debug & /agents panel tally.
func TestLocalActorRegistry_ReclaimPublishesProductEventAndDebugSummary(t *testing.T) {
	host, rootSession, _ := newLocalQuotaHarness(t, 1, 1)

	_, err := host.ActorRegistry.Spawn(context.Background(), rootSession.ID, toolbroker.SpawnAgentArgs{ID: "reclaim-event-child"})
	require.NoError(t, err, "the idle child must be evicted so the spawn can proceed")

	reclaimEvents := localReclaimEvents(host.EventBus.Recent(64))
	require.Len(t, reclaimEvents, 1, "exactly one eviction pass happened")
	event := reclaimEvents[0]
	require.Equal(t, agentcontrol.EventAgentReclaimed, event.Type)
	require.Equal(t, rootSession.ID, event.SessionID, "the product event belongs to the parent session stream")
	require.Equal(t, agentcontrol.ReclaimSourceSpawnGate, event.Payload["source"])
	require.Equal(t, 1, event.Payload["reclaimed"])
	require.Equal(t, "/root/held-child", event.Payload["agent_path"])
	require.Equal(t, []string{agentcontrol.ReclaimReasonIdleTimeout}, event.Payload["reasons"])
	require.Equal(t, rootSession.ID, event.Payload["root_session_id"])
	require.NotEmpty(t, event.Payload["summary"])

	// Durable mirror: /runtime/events and the frontend drill-down keep the
	// eviction after the in-process bus rotated it away.
	persisted, err := host.EventStore.ListEvents(context.Background(), rootSession.ID, 0, 64)
	require.NoError(t, err)
	require.Len(t, localReclaimEvents(persisted), 1, "the eviction must be persisted into the parent session event stream")

	// /debug + /agents panel visibility.
	summary := host.localAgentReclaimSummary()
	require.Contains(t, summary, "reclaim events=1")
	require.Contains(t, summary, "rows=1")
	require.Contains(t, summary, "source="+agentcontrol.ReclaimSourceSpawnGate)
	require.Contains(t, summary, "reasons="+agentcontrol.ReclaimReasonIdleTimeout)
	require.Equal(t, rootSession.ID, reclaimEvents[0].SessionID)

	session := chatAgentCleanupHarnessSession(host, rootSession)
	lines := strings.Join(chatAgentControlConsistencyLines(session), "\n")
	require.Contains(t, lines, "reclaim events=1")
	require.Contains(t, lines, "source="+agentcontrol.ReclaimSourceSpawnGate)
}

// TestChatAgentCleanup_PublishesManualReclaimEvent keeps the two entry points on
// one payload contract: only `source` differs, so an operator can tell an
// automatic eviction from their own /agents cleanup in the same stream.
func TestChatAgentCleanup_PublishesManualReclaimEvent(t *testing.T) {
	host, rootSession, _ := newLocalQuotaHarness(t, 1, 0)
	session := chatAgentCleanupHarnessSession(host, rootSession)

	// The opt-in idle policy keeps the live child reclaimable at cleanup time
	// (a terminal-but-unprojected child would instead be closed by the stale
	// binding sweep, which is a different action and must not claim to be a
	// quota reclaim).
	text, err := runChatAgentCleanupCommand(session, "cleanup --idle 1ns")
	require.NoError(t, err)
	require.Contains(t, text, "reclaimed=1")
	require.Contains(t, text, "reclaim_reasons="+agentcontrol.ReclaimReasonIdleTimeout)

	reclaimEvents := localReclaimEvents(host.EventBus.Recent(64))
	require.Len(t, reclaimEvents, 1)
	require.Equal(t, agentcontrol.ReclaimSourceManualCleanup, reclaimEvents[0].Payload["source"])
	require.Equal(t, "/root/held-child", reclaimEvents[0].Payload["agent_path"])
	require.Equal(t, []string{agentcontrol.ReclaimReasonIdleTimeout}, reclaimEvents[0].Payload["reasons"])
	require.Contains(t, host.localAgentReclaimSummary(), "source="+agentcontrol.ReclaimSourceManualCleanup)
}

// TestLocalAgentReclaimSummaryDistinguishesUnavailableFromEmpty keeps the /debug
// line honest: an unwired bus is "unavailable" while a wired-but-quiet host is
// "events=0" (no eviction observed), so operators can tell silence from a
// missing wiring.
func TestLocalAgentReclaimSummaryDistinguishesUnavailableFromEmpty(t *testing.T) {
	require.Equal(t, "reclaim=unavailable", (*localChatRuntimeHost)(nil).localAgentReclaimSummary())
	require.Equal(t, "reclaim=unavailable", (&localChatRuntimeHost{}).localAgentReclaimSummary())

	quiet := &localChatRuntimeHost{EventBus: runtimeevents.NewBusWithRetention(8)}
	require.Equal(t, "reclaim events=0", quiet.localAgentReclaimSummary())

	quiet.EventBus.Publish(runtimeevents.Event{Type: "tool.completed", SessionID: "s1"})
	require.Equal(t, "reclaim events=0", quiet.localAgentReclaimSummary(), "unrelated events must not be counted")
}

func localReclaimEvents(events []runtimeevents.Event) []runtimeevents.Event {
	out := make([]runtimeevents.Event, 0, len(events))
	for _, event := range events {
		if strings.TrimSpace(event.Type) == agentcontrol.EventAgentReclaimed {
			out = append(out, event)
		}
	}
	return out
}
