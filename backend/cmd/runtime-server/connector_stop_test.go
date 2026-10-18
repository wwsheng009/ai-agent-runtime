package main

// /stop：runtimeConnectorBackend 复用 runtimeapi 的 interrupt 实现（R11）。

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wwsheng009/ai-agent-runtime/internal/api/runtimeapi"
	"github.com/wwsheng009/ai-agent-runtime/internal/connectors"
)

func (h *fakeRuntimeConnectorHost) interruptCallsSnapshot() []runtimeConnectorInterruptCall {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]runtimeConnectorInterruptCall(nil), h.interruptCalls...)
}

func TestRuntimeConnectorInterruptMapsOutcome(t *testing.T) {
	stopped := runtimeapi.SessionTurnInterruptOutcome{Cancelled: true, Reason: "cancelled", Channel: "active_turn"}
	host := &fakeRuntimeConnectorHost{interruptOutcome: &stopped}
	backend := newRuntimeConnectorCommandBackend(host)

	result, err := backend.Interrupt(context.Background(), connectors.InterruptRequest{
		Session: connectors.SessionRef{SessionID: "sess-1"},
	})
	require.NoError(t, err)
	require.True(t, result.Handled)
	require.True(t, result.Stopped)
	require.Equal(t, []runtimeConnectorInterruptCall{{sessionID: "sess-1"}}, host.interruptCallsSnapshot())
}

func TestRuntimeConnectorInterruptIdleAndUnbound(t *testing.T) {
	// 宿主回 no_active_turn：受理但 Stopped=false（核心按“没有在途回合”回执）。
	host := &fakeRuntimeConnectorHost{}
	backend := newRuntimeConnectorCommandBackend(host)
	result, err := backend.Interrupt(context.Background(), connectors.InterruptRequest{
		Session: connectors.SessionRef{SessionID: "sess-1"},
	})
	require.NoError(t, err)
	require.True(t, result.Handled)
	require.False(t, result.Stopped)
	require.Len(t, host.interruptCallsSnapshot(), 1)

	// 未绑定会话：不调用宿主，Handled=true 且 Stopped=false。
	host2 := &fakeRuntimeConnectorHost{}
	backend2 := newRuntimeConnectorCommandBackend(host2)
	result, err = backend2.Interrupt(context.Background(), connectors.InterruptRequest{})
	require.NoError(t, err)
	require.True(t, result.Handled)
	require.False(t, result.Stopped)
	require.Empty(t, host2.interruptCallsSnapshot())
}
