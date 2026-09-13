package toolbroker

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCapAgentEventsUnread(t *testing.T) {
	require.Equal(t, 0, CapAgentEventsUnread(0))
	require.Equal(t, 0, CapAgentEventsUnread(-3))
	require.Equal(t, 7, CapAgentEventsUnread(7))
	require.Equal(t, AgentEventsUnreadProbeLimit, CapAgentEventsUnread(AgentEventsUnreadProbeLimit))
	require.Equal(t, AgentEventsUnreadProbeLimit, CapAgentEventsUnread(AgentEventsUnreadProbeLimit+50))
}

func TestApplyAgentEventsPagination_SetsWindowMetadataAndAdvice(t *testing.T) {
	result := &AgentEventsResult{
		SessionID:  "child-1",
		Count:      2,
		LatestSeq:  7,
		NextAction: "consume_events: use returned events now",
	}

	got := ApplyAgentEventsPagination(result, true, 3)

	require.Same(t, result, got)
	require.True(t, got.HasMore)
	require.Equal(t, 3, got.UnreadCount)
	require.Contains(t, got.NextAction, "after_seq=7")
	require.Contains(t, got.NextAction, "3 more event(s) remain")
}

func TestApplyAgentEventsPagination_CapsUnreadAndSaysAtLeast(t *testing.T) {
	result := &AgentEventsResult{SessionID: "child-1", Count: 20, LatestSeq: 120}

	got := ApplyAgentEventsPagination(result, true, AgentEventsUnreadProbeLimit+99)

	require.True(t, got.HasMore)
	require.Equal(t, AgentEventsUnreadProbeLimit, got.UnreadCount)
	require.Contains(t, got.NextAction, "at least 200 more event(s) remain")
}

func TestApplyAgentEventsPagination_UnknownUnreadStillReportsMore(t *testing.T) {
	result := &AgentEventsResult{SessionID: "child-1", Count: 1, LatestSeq: 4}

	got := ApplyAgentEventsPagination(result, true, 0)

	require.True(t, got.HasMore)
	require.Zero(t, got.UnreadCount)
	require.Contains(t, got.NextAction, "more event(s) remain")
}

func TestApplyAgentEventsPagination_NoMoreLeavesResultUntouched(t *testing.T) {
	result := &AgentEventsResult{
		SessionID:  "child-1",
		Count:      1,
		LatestSeq:  4,
		NextAction: "stop_empty_event_poll: 0 events returned",
	}

	got := ApplyAgentEventsPagination(result, false, 0)

	require.Same(t, result, got)
	require.False(t, got.HasMore)
	require.Zero(t, got.UnreadCount)
	require.Equal(t, "stop_empty_event_poll: 0 events returned", got.NextAction)
}

func TestApplyAgentEventsPagination_NilResult(t *testing.T) {
	require.Nil(t, ApplyAgentEventsPagination(nil, true, 5))
}
