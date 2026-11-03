package runtimeapi

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/wwsheng009/ai-agent-runtime/internal/llm"
)

func TestStreamEventNameMapsRetryReset(t *testing.T) {
	require.Equal(t, "reset", streamEventName(llm.EventTypeReset))
}

func TestBuildStreamChunkPayloadCarriesRetryResetIdentity(t *testing.T) {
	payload := buildStreamChunkPayload(llm.StreamChunk{
		Type:     llm.EventTypeReset,
		StreamID: "stream-1",
		TurnID:   "turn-1",
		Step:     2,
		Metadata: map[string]interface{}{"reason": "retry_reset"},
	}, 7, 0)

	assert.Equal(t, "reset", payload["type"])
	assert.Equal(t, "stream-1", payload["stream_id"])
	assert.Equal(t, "turn-1", payload["turn_id"])
	assert.Equal(t, 2, payload["step"])
	assert.Equal(t, map[string]interface{}{"reason": "retry_reset"}, payload["metadata"])
}
