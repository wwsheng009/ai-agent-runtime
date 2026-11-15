package toolbroker

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
)

func TestAgentEventsFlowTimesOutEmptyWindow(t *testing.T) {
	flow := AgentEventsFlow{
		Subscribe: func(context.Context, string, bool) (<-chan struct{}, func()) {
			return make(chan struct{}), func() {}
		},
		List: func(context.Context, string, int64, int) ([]runtimeevents.Event, error) {
			return nil, nil
		},
		Build: BuildAgentEventsResult,
	}
	result, err := flow.Run(context.Background(), "child-1", ReadAgentEventsArgs{WaitMs: 5})
	require.NoError(t, err)
	require.True(t, result.TimedOut)
	require.Contains(t, result.NextAction, "stop_empty_event_poll")
}

func TestAgentEventsFlowPaginatesAndProbesUnread(t *testing.T) {
	events := []runtimeevents.Event{
		{Type: "one", Payload: map[string]interface{}{"seq": 1}},
		{Type: "two", Payload: map[string]interface{}{"seq": 2}},
		{Type: "three", Payload: map[string]interface{}{"seq": 3}},
	}
	unreadAfter := int64(0)
	flow := AgentEventsFlow{
		Subscribe: func(context.Context, string, bool) (<-chan struct{}, func()) {
			return make(chan struct{}), func() {}
		},
		List: func(context.Context, string, int64, int) ([]runtimeevents.Event, error) {
			return events, nil
		},
		Build: BuildAgentEventsResult,
		Unread: func(_ context.Context, _ string, after int64) int {
			unreadAfter = after
			return 7
		},
	}
	result, err := flow.Run(context.Background(), "child-1", ReadAgentEventsArgs{Limit: 2})
	require.NoError(t, err)
	require.Equal(t, 2, result.Count)
	require.Equal(t, int64(2), result.LatestSeq)
	require.True(t, result.HasMore)
	require.Equal(t, 7, result.UnreadCount)
	require.Equal(t, int64(2), unreadAfter)
}

func TestAgentEventsFlowSoftTimeoutReturnsBoundaryEvents(t *testing.T) {
	listCalls := 0
	flow := AgentEventsFlow{
		Subscribe: func(context.Context, string, bool) (<-chan struct{}, func()) {
			return make(chan struct{}), func() {}
		},
		List: func(ctx context.Context, _ string, _ int64, _ int) ([]runtimeevents.Event, error) {
			listCalls++
			if listCalls == 1 {
				return nil, nil
			}
			return []runtimeevents.Event{{Type: "late", Payload: map[string]interface{}{"seq": 9}}}, nil
		},
		Build: BuildAgentEventsResult,
	}
	result, err := flow.Run(context.Background(), "child-1", ReadAgentEventsArgs{WaitMs: 5})
	require.NoError(t, err)
	require.False(t, result.TimedOut)
	require.Equal(t, 1, result.Count)
	require.Equal(t, int64(9), result.LatestSeq)
}

func TestWaitForAgentEventsTick(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	require.True(t, waitForAgentEventsTick(ctx, make(chan struct{})))

	wake := make(chan struct{}, 1)
	wake <- struct{}{}
	start := time.Now()
	require.False(t, waitForAgentEventsTick(context.Background(), wake))
	require.Less(t, time.Since(start), 500*time.Millisecond)
}
