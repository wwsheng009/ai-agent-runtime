package llm

import (
	"strings"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	"github.com/wwsheng009/ai-agent-runtime/internal/llm/adapter"
	"github.com/wwsheng009/ai-agent-runtime/internal/llm/providercompat"
	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

type providerAdapterRequestInput struct {
	ProviderName            string
	Protocol                string
	BaseURL                 string
	Model                   string
	SupportsMaxOutputTokens *bool
	ModelCapabilities       map[string]agentconfig.ModelCapabilitySpec

	Messages []map[string]interface{}
	Tools    []types.ToolDefinition
	Metadata map[string]interface{}

	ReasoningEffort        string
	ReasoningEffortBudgets map[string]int
	ReasoningModel         bool
	Thinking               *ThinkingConfig
	Stream                 bool
	MaxTokens              int
	Temperature            float64
	Timeout                time.Duration
}

func buildProviderAdapterRequest(input providerAdapterRequestInput) adapter.RequestConfig {
	metadata := cloneMapStringAny(input.Metadata)
	protocol := strings.ToLower(strings.TrimSpace(input.Protocol))
	compatCtx := providercompat.Context{
		ProviderName:            input.ProviderName,
		Protocol:                input.Protocol,
		BaseURL:                 input.BaseURL,
		Model:                   input.Model,
		SupportsMaxOutputTokens: input.SupportsMaxOutputTokens,
		ConfiguredCapabilities:  input.ModelCapabilities,
	}
	modelCapabilities := providercompat.MergeCapabilities(compatCtx, input.ModelCapabilities)
	capability, hasCapability := ResolveModelCapabilitySpec(input.Model, modelCapabilities)
	compat := providercompat.NewChain(compatCtx)

	messages := input.Messages
	switch protocol {
	case "codex":
		before := len(messages)
		messages = sanitizeCodexProtocolMessages(messages)
		if dropped := before - len(messages); dropped > 0 {
			metadata["tool_replay_sanitized"] = true
			metadata["tool_replay_dropped_messages"] = dropped
		}
		if !compat.SupportsMaxOutputTokens() {
			metadata[codexSupportsMaxOutputTokensMetadataKey] = false
		}
	case "openai":
		before := len(messages)
		messages = sanitizeOpenAICompatibleProtocolMessages(messages)
		if dropped := before - len(messages); dropped > 0 {
			metadata["tool_replay_sanitized"] = true
			metadata["tool_replay_dropped_messages"] = dropped
		}
		before = len(messages)
		messages = compat.NormalizeOpenAICompatibleMessages(messages)
		if merged := before - len(messages); merged > 0 {
			metadata["provider_compat_system_messages_merged"] = merged
		}
	case "anthropic":
		before := len(messages)
		messages = sanitizeAnthropicProtocolMessages(messages)
		if dropped := before - len(messages); dropped > 0 {
			metadata["tool_replay_sanitized"] = true
			metadata["tool_replay_dropped_messages"] = dropped
		}
	}

	var tools interface{}
	if !metadataDisablesTools(metadata) {
		tools = BuildToolDefinitionsForRequestWithImageOptions(
			input.Tools,
			input.Protocol,
			input.Model,
			modelCapabilities,
			!metadataDisablesMetaTools(metadata),
			CodexImageGenerationOptionsFromMetadata(metadata),
		)
	}

	reasoningConfig := resolveRequestReasoningConfig(input.ReasoningEffort, input.Thinking, input.Metadata)
	requestReasoningEffort := supportedProviderReasoningEffort(reasoningConfig.ReasoningEffort, capability, hasCapability)
	reasoningModel := ReasoningModelEnabled(capability, input.ReasoningModel)
	reasoningEffortBudgets := input.ReasoningEffortBudgets
	if len(reasoningEffortBudgets) == 0 && hasCapability {
		reasoningEffortBudgets = capability.ReasoningEffortBudgets
	}

	// Cap MaxTokens by model capability limit when available.
	// This prevents provider-level max_tokens_limit (e.g. 131072 for
	// the anthropic.messages template) from exceeding a specific model's
	// actual output-token ceiling (e.g. 128000 for claude-opus-4-7).
	maxTokens := input.MaxTokens
	if hasCapability && capability.MaxTokens > 0 && maxTokens > capability.MaxTokens {
		maxTokens = capability.MaxTokens
	}

	return adapter.RequestConfig{
		Model:                  input.Model,
		Messages:               messages,
		Stream:                 input.Stream,
		MaxTokens:              maxTokens,
		ReasoningEffort:        requestReasoningEffort,
		ReasoningEffortBudgets: reasoningEffortBudgets,
		ReasoningModel:         reasoningModel,
		Thinking:               reasoningConfig.Thinking,
		Temperature:            input.Temperature,
		Functions:              tools,
		ToolChoice:             metadata["tool_choice"],
		StopSequences:          providerAdapterStopSequences(metadata),
		Timeout:                input.Timeout,
		Metadata:               metadata,
	}
}

func providerAdapterStopSequences(metadata map[string]interface{}) []string {
	if len(metadata) == 0 {
		return nil
	}
	raw, ok := metadata["stop_sequences"]
	if !ok {
		return nil
	}
	switch values := raw.(type) {
	case []string:
		return append([]string(nil), values...)
	case []interface{}:
		result := make([]string, 0, len(values))
		for _, value := range values {
			text, ok := value.(string)
			if !ok {
				continue
			}
			result = append(result, text)
		}
		return result
	default:
		return nil
	}
}

func chatToolsToToolDefinitions(tools []Tool) []types.ToolDefinition {
	if len(tools) == 0 {
		return nil
	}
	normalized := make([]types.ToolDefinition, 0, len(tools))
	for _, tool := range tools {
		normalized = append(normalized, types.ToolDefinition{
			Name:        tool.Function.Name,
			Description: tool.Function.Description,
			Parameters:  cloneDeepMapStringAny(tool.Function.Parameters),
		})
	}
	return normalized
}
