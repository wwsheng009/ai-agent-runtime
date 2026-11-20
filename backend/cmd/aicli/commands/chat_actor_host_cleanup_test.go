package commands

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAppendLocalChatHostCleanupPreservesExistingCallbacks(t *testing.T) {
	host := &localChatRuntimeHost{}
	calls := 0
	host.cleanupFns = append(host.cleanupFns, func() { calls++ })

	appendLocalChatHostCleanup(host, func() { calls += 10 })

	require.Len(t, host.cleanupFns, 2)
	for _, fn := range host.cleanupFns {
		fn()
	}
	require.Equal(t, 11, calls)
}
