package chat

import (
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

// P3 fail-loud observability: the identity_missing counter must tick for
// windows that still carry id-less rows, and stay silent for fully-identified
// windows (the protocol end state).
func TestIdentityMissingObservation(t *testing.T) {
	before := IdentityMissingTotal()

	// Fully identified window: no observation.
	identified := []types.Message{
		withIDForTest(types.NewUserMessage("u1"), "msg_u1", "turn_1"),
		withIDForTest(types.NewAssistantMessage("a1"), "msg_a1", "turn_1"),
	}
	stored := []types.Message{
		withIDForTest(types.NewUserMessage("u1"), "msg_u1", "turn_1"),
	}
	start, anchored := identityAlignedAppendStart(identified, stored)
	require.True(t, anchored)
	// The stored tail backs only the user row: the assistant is new, so the
	// anchor sits at index 1 and that row appends.
	require.Equal(t, 1, start)
	require.Equal(t, before, IdentityMissingTotal(),
		"fully identified windows must not tick the counter")

	// Window with id-less rows: observation ticks by the missing count.
	idless := append(append([]types.Message{}, identified...),
		*types.NewAssistantMessage("no id"))
	start, anchored = identityAlignedAppendStart(idless, stored)
	require.True(t, anchored)
	require.Equal(t, before+1, IdentityMissingTotal(),
		"one id-less row must tick the counter exactly once")
	require.Equal(t, 1, start)
}

// Backfill semantics (P3 prerequisite): BackfillCanonicalIdentities mints ids
// for id-less rows and is idempotent — a second run is a no-op. The primitive
// is tested directly (no store round trip): AddMessage mints ids before
// persistence, so id-less rows only exist in legacy durable payloads.
func TestBackfillCanonicalIdentitiesIdempotent(t *testing.T) {
	idless := types.NewUserMessage("legacy row")
	require.Empty(t, types.MessageID(*idless), "precondition: constructor leaves the row id-less for the legacy shape")

	stamped := types.NewAssistantMessage("protocol row")
	stamped.Metadata = types.NewMetadata()
	stamped.Metadata.Set(types.MetadataKeyMessageID, "msg_protocol")

	rows := []types.Message{*idless, *stamped}
	granted := BackfillCanonicalIdentities(rows)
	require.Equal(t, 1, granted, "only the id-less row should be granted an id")
	require.NotEmpty(t, types.MessageID(rows[0]))
	require.Equal(t, "msg_protocol", types.MessageID(rows[1]),
		"rows that already carry an id keep it")

	// Idempotent: second pass grants nothing.
	require.Equal(t, 0, BackfillCanonicalIdentities(rows))
}

func withIDForTest(message *types.Message, messageID, turnID string) types.Message {
	if message.Metadata == nil {
		message.Metadata = types.NewMetadata()
	}
	message.Metadata.Set(types.MetadataKeyMessageID, messageID)
	message.Metadata.Set(types.MetadataKeyTurnID, turnID)
	return *message
}