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
	APIPath                 string
	CompatibilityProfile    string
	Model                   string
	SupportsMaxOutputTokens *bool
	ModelCapabilities       map[string]agentconfig.ModelCapabilitySpec
	// EnableImageGeneration is the provider-level Codex native image_generation
	// opt-in. Nil/false keeps the tool out of request payloads by default.
	EnableImageGeneration *bool

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
		APIPath:                 input.APIPath,
		Profile:                 input.CompatibilityProfile,
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

	// Tool definitions are part of the provider prompt-cache prefix. Never use a
	// per-request execution decision (compact/disable_tools/tool_choice) to remove
	// or rewrite that prefix. Callers that need a no-tool operation must express
	// it through tool_choice while retaining the frozen session surface.
	if metadataDisablesTools(metadata) {
		if _, exists := metadata["tool_choice"]; !exists {
			metadata["tool_choice"] = "none"
		}
	}
	var tools interface{}
	enableNativeImageGeneration := input.EnableImageGeneration != nil && *input.EnableImageGeneration
	if len(input.Tools) > 0 || enableNativeImageGeneration {
		tools = BuildToolDefinitionsForRequestWithImageOptions(
			input.Tools,
			input.Protocol,
			input.Model,
			modelCapabilities,
			false,
			CodexImageGenerationOptionsFromMetadata(metadata),
			enableNativeImageGeneration,
		)
	}

	reasoningConfig := resolveRequestReasoningConfig(input.ReasoningEffort, input.Thinking, input.Metadata)
	// Model-card efforts describe selectable/default values. Do not turn them
	// into a wire-level allowlist: callers may explicitly use newer or
	// provider-specific values before the local card is updated.
	requestReasoningEffort := reasoningConfig.ReasoningEffort
	reasoningModel := ReasoningModelEnabled(capability, input.ReasoningModel)
	reasoningEffortBudgets := input.ReasoningEffortBudgets
	if len(reasoningEffortBudgets) == 0 && hasCapability {
		reasoningEffortBudgets = capability.ReasoningEffortBudgets
	}

	// Resolve/clamp MaxTokens with Claude Code-style default/upperLimit logic.
	// Positive request budgets are only clamped to the hard ceiling; zero/unset
	// budgets fall back to the capped model default (8k) unless env overrides.
	maxTokens := CapRequestMaxTokens(input.Protocol, input.Model, input.MaxTokens, capability, hasCapability, 0)
	if maxTokens <= 0 {
		resolved := ResolveRequestMaxTokens(input.Protocol, input.Model, 0, capability, hasCapability, 0)
		maxTokens = resolved.Default
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

const defaultClaudeMaxOutputTokens = 128000

func looksLikeClaudeModel(model string) bool {
	normalized := strings.ToLower(strings.TrimSpace(model))
	if normalized == "" {
		return false
	}
	return strings.HasPrefix(normalized, "claude-") ||
		strings.HasPrefix(normalized, "claude.") ||
		strings.HasPrefix(normalized, "anthropic.claude") ||
		strings.Contains(normalized, ".claude-") ||
		strings.Contains(normalized, "/claude-")
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
			Metadata:    cloneDeepMapStringAny(tool.Metadata),
		})
	}
	return normalized
}
