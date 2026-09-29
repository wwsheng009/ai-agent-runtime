package chat

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestCompactReplacementTokenLimitFitsSendGate pins the pre-turn/mid-turn
// replacement ceiling added for the 2026-09-28 incident: the replacement must
// fit the enforced gate budget minus the frozen tool surface's share, and an
// unresolvable budget (0, or the schema alone exhausting the gate) must leave
// the adapter's own ceiling in place instead of forcing a degenerate 1-token
// replacement.
func TestCompactReplacementTokenLimitFitsSendGate(t *testing.T) {
	require.Equal(t, 78127, compactReplacementTokenLimit(96000, 17873))
	require.Equal(t, 0, compactReplacementTokenLimit(17873, 17873))
	require.Equal(t, 0, compactReplacementTokenLimit(0, 0))
}
