package chat

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

// P1 INV-1 audit item (session-message-identity-protocol-20261017): the tool
// receipt replay path preserves the original message identity. The receipt
// payload is the JSON-marshaled tool message; a future serialization change
// that drops metadata would silently strip message_id/turn_id and reintroduce
// the substance-collision class. This test pins the round trip.
func TestToolReceiptRoundTripPreservesMessageIdentity(t *testing.T) {
	message := types.NewToolMessage("call_replay", "ok")
	message.Metadata = types.NewMetadata()
	message.Metadata.Set(types.MetadataKeyMessageID, "msg_replay_original")
	message.Metadata.Set(types.MetadataKeyTurnID, "turn_replay_original")

	payload, err := json.Marshal(message)
	require.NoError(t, err)

	decoded, ok := decodePendingToolResultMessage(payload)
	require.True(t, ok)
	require.NotNil(t, decoded)
	require.Equal(t, "msg_replay_original", types.MessageID(*decoded),
		"receipt round trip must keep message_id — the replay path relies on it")
	require.Equal(t, "turn_replay_original", types.TurnID(*decoded),
		"receipt round trip must keep turn_id — the replay path relies on it")
}

// The reconcile writer (reconcileToolReceipts) marshals the full message
// including metadata; pin that shape too so a "minify payload" refactor cannot
// silently strip identity.
func TestReconcileReceiptPayloadCarriesIdentity(t *testing.T) {
	message := types.NewToolMessage("call_rc", "ok")
	message.Metadata = types.NewMetadata()
	message.Metadata.Set(types.MetadataKeyMessageID, "msg_rc_identity")

	payload, err := json.Marshal(message)
	require.NoError(t, err)
	require.Contains(t, string(payload), "msg_rc_identity")
	require.Contains(t, string(payload), types.MetadataKeyMessageID)
}
