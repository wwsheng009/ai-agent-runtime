package llm

import (
	"reflect"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

func TestBuildProviderAdapterRequest_WrapperAndGatewayMatch(t *testing.T) {
	const (
		model   = "sensenova-6.7-flash-lite"
		baseURL = "https://token.sensenova.cn/v1"
	)

	toolParameters := map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"path": map[string]interface{}{"type": "string"},
		},
	}
	metadata := map[string]interface{}{
		"tool_choice":    "auto",
		"stop_sequences": []interface{}{"END"},
	}

	provider, err := NewProvider(&ProviderConfig{
		Type:    "openai",
		BaseURL: baseURL,
	})
	if err != nil {
		t.Fatalf("create provider: %v", err)
	}
	wrapper := provider.(*ProviderWrapper)
	wrapperConfig := wrapper.convertRequest(ChatRequest{
		Model:           model,
		Stream:          true,
		MaxTokens:       512,
		Temperature:     0.3,
		ReasoningEffort: "high",
		Metadata:        cloneMapStringAny(metadata),
		Tools: []Tool{{
			Type: "function",
			Function: ToolFunction{
				Name:        "list_files",
				Description: "List files",
				Parameters:  toolParameters,
			},
		}},
		Messages: []Message{
			{Role: "system", Content: "first"},
			{Role: "system", Content: "second"},
			{Role: "user", Content: "ls"},
			{
				Role: "assistant",
				ToolCalls: []ToolCall{{
					ID:   "call_1",
					Type: "function",
					Function: ToolCallFunc{
						Name:      "list_files",
						Arguments: "",
					},
				}},
			},
			{Role: "tool", Content: "ok", ToolCallID: "call_1"},
		},
	})

	client := &GatewayClient{}
	gatewayConfig := client.buildAdapterRequest(model, &LLMRequest{
		Model:           model,
		Stream:          true,
		MaxTokens:       512,
		Temperature:     0.3,
		ReasoningEffort: "high",
		Metadata:        cloneMapStringAny(metadata),
		Tools: []types.ToolDefinition{{
			Name:        "list_files",
			Description: "List files",
			Parameters:  toolParameters,
		}},
		Messages: []types.Message{
			{Role: "system", Content: "first"},
			{Role: "system", Content: "second"},
			{Role: "user", Content: "ls"},
			{
				Role: "assistant",
				ToolCalls: []types.ToolCall{{
					ID:   "call_1",
					Name: "list_files",
				}},
			},
			{Role: "tool", Content: "ok", ToolCallID: "call_1"},
		},
	}, &SelectedResource{
		Provider: &ProviderResource{
			Name:    "sensenova",
			Type:    "openai",
			BaseURL: baseURL,
		},
	}, "openai")

	assertRequestConfigEqual(t, "messages", wrapperConfig.Messages, gatewayConfig.Messages)
	assertRequestConfigEqual(t, "functions", wrapperConfig.Functions, gatewayConfig.Functions)
	assertRequestConfigEqual(t, "metadata", wrapperConfig.Metadata, gatewayConfig.Metadata)
	assertRequestConfigEqual(t, "stop_sequences", wrapperConfig.StopSequences, gatewayConfig.StopSequences)
	assertRequestConfigEqual(t, "tool_choice", wrapperConfig.ToolChoice, gatewayConfig.ToolChoice)
	assertRequestConfigEqual(t, "reasoning_effort", wrapperConfig.ReasoningEffort, gatewayConfig.ReasoningEffort)
	assertRequestConfigEqual(t, "reasoning_model", wrapperConfig.ReasoningModel, gatewayConfig.ReasoningModel)
	assertRequestConfigEqual(t, "model", wrapperConfig.Model, gatewayConfig.Model)
	assertRequestConfigEqual(t, "stream", wrapperConfig.Stream, gatewayConfig.Stream)
	assertRequestConfigEqual(t, "max_tokens", wrapperConfig.MaxTokens, gatewayConfig.MaxTokens)
	assertRequestConfigEqual(t, "temperature", wrapperConfig.Temperature, gatewayConfig.Temperature)
}

func assertRequestConfigEqual(t *testing.T, field string, want, got interface{}) {
	t.Helper()
	if !reflect.DeepEqual(want, got) {
		t.Fatalf("request config %s mismatch:\nwant: %#v\ngot:  %#v", field, want, got)
	}
}

func TestBuildProviderAdapterRequest_CapsMaxTokensByCapability(t *testing.T) {
	input := providerAdapterRequestInput{
		Protocol: "anthropic",
		Model:    "claude-opus-4-7",
		MaxTokens: 131072,
		ModelCapabilities: map[string]agentconfig.ModelCapabilitySpec{
			"claude-opus-4-7": {
				MaxTokens:         128000,
				MaxContextTokens:  1000000,
				ReasoningModel:    true,
				ReasoningEfforts:  []string{"low", "medium", "high", "xhigh", "max"},
				InputModalities:   []string{"text", "image"},
			},
		},
		Messages: []map[string]interface{}{
			{"role": "user", "content": "hello"},
		},
	}

	result := buildProviderAdapterRequest(input)

	if result.MaxTokens != 128000 {
		t.Fatalf("expected MaxTokens to be capped at 128000 (capability), got %d", result.MaxTokens)
	}
}

func TestBuildProviderAdapterRequest_NoCapWhenNoCapability(t *testing.T) {
	input := providerAdapterRequestInput{
		Protocol: "anthropic",
		Model:    "unknown-model",
		MaxTokens: 131072,
		Messages: []map[string]interface{}{
			{"role": "user", "content": "hello"},
		},
	}

	result := buildProviderAdapterRequest(input)

	if result.MaxTokens != 131072 {
		t.Fatalf("expected MaxTokens to remain 131072 (no capability), got %d", result.MaxTokens)
	}
}

func TestBuildProviderAdapterRequest_NoCapWhenWithinLimit(t *testing.T) {
	input := providerAdapterRequestInput{
		Protocol: "anthropic",
		Model:    "claude-opus-4-7",
		MaxTokens: 8192,
		ModelCapabilities: map[string]agentconfig.ModelCapabilitySpec{
			"claude-opus-4-7": {
				MaxTokens:        128000,
				MaxContextTokens: 1000000,
			},
		},
		Messages: []map[string]interface{}{
			{"role": "user", "content": "hello"},
		},
	}

	result := buildProviderAdapterRequest(input)

	if result.MaxTokens != 8192 {
		t.Fatalf("expected MaxTokens to remain 8192 (within limit), got %d", result.MaxTokens)
	}
}
