package commands

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestParkedEscapeWatcherLifecycle pins the parked-window ESC consumer
// bookkeeping: acquire is single-flight per parked spell, release clears the
// ref exactly once, and repeated releases stay no-ops (settle/resume, interrupt
// cleanup and teardown all report the same end edge).
func TestParkedEscapeWatcherLifecycle(t *testing.T) {
	bridge := &chatRuntimeEventBridge{session: &ChatSession{}}

	bridge.acquireParkedEscapeWatcher()
	require.NotNil(t, bridge.parkedEscapeStop, "the parked spell must hold exactly one ESC consumer")

	// A second suspend edge during the same spell must not stack another ref.
	bridge.acquireParkedEscapeWatcher()
	require.NotNil(t, bridge.parkedEscapeStop)

	bridge.releaseParkedEscapeWatcher()
	require.Nil(t, bridge.parkedEscapeStop, "resume/cleanup must release the parked consumer")

	// Idempotent: another end edge after the release is a no-op.
	bridge.releaseParkedEscapeWatcher()
	require.Nil(t, bridge.parkedEscapeStop)

	// A later parked spell re-arms cleanly.
	bridge.acquireParkedEscapeWatcher()
	require.NotNil(t, bridge.parkedEscapeStop)
	bridge.releaseParkedEscapeWatcher()
}
