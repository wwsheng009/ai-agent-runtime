package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wwsheng009/ai-agent-runtime/internal/llm/adapter"
	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

func TestProviderWrapperRemoteCompactCodexUsesCompactEndpointAndBuildsReplayableHistory(t *testing.T) {
	var capturedPath string
	var capturedBody map[string]interface{}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedPath = r.URL.Path
		require.NoError(t, json.NewDecoder(r.Body).Decode(&capturedBody))
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{
			"output": [
				{
					"type": "compaction",
					"encrypted_content": "opaque-token-1"
				}
			]
		}`)
	}))
	defer server.Close()

	provider := &ProviderWrapper{
		config: &ProviderConfig{
			Type:    "codex",
			APIKey:  "test-key",
			BaseURL: server.URL,
			Timeout: 5 * time.Second,
		},
		adapter:   &adapter.CodexAdapter{},
		tokenizer: NewTokenizer("openai"),
	}

	response, err := provider.RemoteCompact(context.Background(), RemoteCompactRequest{
		Model:   "gpt-5.4",
		History: remoteCompactTestHistory(),
	})
	require.NoError(t, err)
	require.NotNil(t, response)

	assert.Equal(t, "/v1/responses/compact", capturedPath)
	assert.Equal(t, "gpt-5.4", capturedBody["model"])
	assert.False(t, capturedBody["parallel_tool_calls"].(bool))
	require.Empty(t, decodeSliceOfMaps(capturedBody["tools"]))

	require.Len(t, response.ReplacementHistory, 2)
	assert.Equal(t, "system", response.ReplacementHistory[0].Role)
	assert.Equal(t, "assistant", response.ReplacementHistory[1].Role)
	assert.Equal(t, remoteCompactPlaceholder, response.ReplacementHistory[1].Content)
	assert.Equal(t, "compaction", response.ReplacementHistory[1].Metadata["context_stage"])
	assert.Equal(t, "remote", response.ReplacementHistory[1].Metadata["compact_mode"])

	reasoning := types.GetReasoningBlock(response.ReplacementHistory[1].Metadata)
	require.NotNil(t, reasoning)
	assert.Equal(t, "openai_responses", reasoning.Format)
	assert.True(t, reasoning.ReplayRequired)
	assert.True(t, reasoning.Streamable)
	assert.Equal(t, types.ReasoningVisibilityOpaque, reasoning.Visibility)

	outputItems := canonicalizeCodexOutputItems(reasoning.Metadata[reasoningMetadataCodexOutputItemsKey])
	require.Len(t, outputItems, 1)
	assert.Equal(t, "compaction", outputItems[0]["type"])
	assert.Equal(t, "opaque-token-1", outputItems[0]["encrypted_content"])
}

func TestRemoteCompactReplayFeedsCompactionItemBackIntoCodexInput(t *testing.T) {
	response, err := decodeCodexRemoteCompactResponse(remoteCompactTestHistory(), []byte(`{
		"output": [
			{
				"type": "compaction",
				"encrypted_content": "opaque-token-2"
			}
		]
	}`))
	require.NoError(t, err)
	require.NotNil(t, response)

	request := buildCodexRemoteCompactRequest("gpt-5.4", response.ReplacementHistory)
	inputItems := decodeSliceOfMaps(request["input"])
	require.Len(t, inputItems, 1)
	assert.Equal(t, "compaction", inputItems[0]["type"])
	assert.Equal(t, "opaque-token-2", inputItems[0]["encrypted_content"])
}

func TestGatewayClientRemoteCompactCodexUsesSelectedProviderCompactEndpoint(t *testing.T) {
	var capturedPath string
	var capturedBody map[string]interface{}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedPath = r.URL.Path
		require.NoError(t, json.NewDecoder(r.Body).Decode(&capturedBody))
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{
			"output": [
				{
					"type": "compaction",
					"encrypted_content": "opaque-token-3"
				}
			]
		}`)
	}))
	defer server.Close()

	rm := &gatewayTestResourceManager{
		selected: &SelectedResource{
			Provider: &ProviderResource{
				Name:    "codex_ee",
				Type:    "codex",
				BaseURL: server.URL,
				APIPath: "/custom/responses",
			},
			KeyValue: "test-key",
			Model:    "gpt-5.4-mini",
		},
	}
	client := NewGatewayClient(rm, "gpt-5.4-mini")

	response, err := client.RemoteCompact(context.Background(), RemoteCompactRequest{
		Model:   "gpt-5.4",
		History: remoteCompactTestHistory(),
	})
	require.NoError(t, err)
	require.NotNil(t, response)

	assert.Equal(t, 1, rm.selectCalls)
	require.Len(t, rm.results, 1)
	assert.True(t, rm.results[0].success)
	assert.Equal(t, 200, rm.results[0].statusCode)

	assert.Equal(t, "/custom/responses/compact", capturedPath)
	assert.Equal(t, "gpt-5.4-mini", capturedBody["model"])
	require.Len(t, response.ReplacementHistory, 2)
	reasoning := types.GetReasoningBlock(response.ReplacementHistory[1].Metadata)
	require.NotNil(t, reasoning)
	outputItems := canonicalizeCodexOutputItems(reasoning.Metadata[reasoningMetadataCodexOutputItemsKey])
	require.Len(t, outputItems, 1)
	assert.Equal(t, "opaque-token-3", outputItems[0]["encrypted_content"])
}

func remoteCompactTestHistory() []types.Message {
	return []types.Message{
		*types.NewSystemMessage("You are a helpful assistant."),
		*types.NewUserMessage("Inspect the runtime wiring."),
		*types.NewAssistantMessage("I will review the provider integration."),
	}
}
