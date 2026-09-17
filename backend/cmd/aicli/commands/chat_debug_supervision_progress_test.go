package commands

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wwsheng009/ai-agent-runtime/internal/supervision"
)

// TestChatDebugSupervisionTaskProgressText pins the M1 observability line
// (plan §7.3, runbook §5.1): `/debug supervision list` must render the
// task-progress write-back counters so an operator can tell "producer
// disabled" from "producer writing but throttled". A host that never enabled
// the feature and counted nothing must keep the historical output (no line).
func TestChatDebugSupervisionTaskProgressText(t *testing.T) {
	enabledHost := &localChatRuntimeHost{
		supervisionConfig: supervision.Config{TaskProgressInterval: 5 * time.Second},
	}
	text := chatDebugSupervisionTaskProgressText(&ChatSession{LocalRuntimeHost: enabledHost})
	require.Contains(t, text, "进度写回")
	require.Contains(t, text, "writes=0")
	require.Contains(t, text, "window_skipped=0")
	require.Contains(t, text, "conflicts_dropped=0")
	require.Contains(t, text, "errors=0")

	disabledHost := &localChatRuntimeHost{}
	require.Empty(t, chatDebugSupervisionTaskProgressText(&ChatSession{LocalRuntimeHost: disabledHost}),
		"a disabled feature with zero counters must not add a line")
	require.Empty(t, chatDebugSupervisionTaskProgressText(nil))
	require.Empty(t, chatDebugSupervisionTaskProgressText(&ChatSession{}))
}
