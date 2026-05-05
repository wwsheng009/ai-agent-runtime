package providercompat

import (
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

// Context captures the provider-specific inputs needed to resolve request and
// capability compatibility.
type Context struct {
	ProviderName            string
	Protocol                string
	BaseURL                 string
	APIPath                 string
	Model                   string
	SupportsMaxOutputTokens *bool
	ConfiguredCapabilities  map[string]agentconfig.ModelCapabilitySpec
}

// Chain is a light-weight compatibility pipeline for one provider context.
// The first implementation keeps the surface small while still centralizing the
// provider-specific decisions that used to be duplicated across call sites.
type Chain struct {
	ctx Context
}

// NewChain constructs a compatibility chain for the given provider context.
func NewChain(ctx Context) Chain {
	return Chain{ctx: normalizeContext(ctx)}
}

func normalizeContext(ctx Context) Context {
	ctx.ProviderName = strings.TrimSpace(ctx.ProviderName)
	ctx.Protocol = strings.ToLower(strings.TrimSpace(ctx.Protocol))
	ctx.BaseURL = strings.TrimSpace(ctx.BaseURL)
	ctx.APIPath = strings.TrimSpace(ctx.APIPath)
	ctx.Model = strings.TrimSpace(ctx.Model)
	return ctx
}

// DefaultRuntimeCapability returns a provider-specific fallback capability used
// by runtime request filtering.
func (c Chain) DefaultRuntimeCapability() (agentconfig.ModelCapabilitySpec, bool) {
	switch {
	case c.IsSensenova():
		return agentconfig.ModelCapabilitySpec{
			ReasoningModel:   true,
			ReasoningEfforts: []string{"low", "medium", "high", "none"},
		}, true
	case c.IsNVIDIA():
		return agentconfig.ModelCapabilitySpec{
			ReasoningModel:   true,
			ReasoningEfforts: []string{"minimal", "low", "medium", "high"},
		}, true
	case c.IsDeepSeek():
		return agentconfig.ModelCapabilitySpec{
			ReasoningModel:   true,
			ReasoningEfforts: []string{"high", "max"},
		}, true
	default:
		return agentconfig.ModelCapabilitySpec{}, false
	}
}

// DefaultLoginReasoningEfforts returns the default reasoning effort list used
// by login/model discovery when a provider does not advertise one explicitly.
func (c Chain) DefaultLoginReasoningEfforts() []string {
	switch {
	case c.IsNVIDIA():
		return []string{"minimal", "low", "medium", "high"}
	case c.IsSensenova():
		return []string{"low", "medium", "high", "none"}
	case c.IsDeepSeek():
		return []string{"high", "max"}
	case c.ctx.Protocol == "codex":
		return []string{"low", "medium", "high", "xhigh", "none"}
	case c.ctx.Protocol == "openai":
		return []string{"low", "medium", "high", "xhigh", "none"}
	default:
		return nil
	}
}

// LoginModelUsesDefaultReasoningEfforts reports whether a discovered model
// should inherit the provider's default effort list.
func (c Chain) LoginModelUsesDefaultReasoningEfforts(modelID string) bool {
	if c.LoginUsesWildcardReasoningEfforts() {
		return true
	}
	if c.IsNVIDIA() || c.IsSensenova() {
		return true
	}
	if IsDeepSeekModel(modelID) {
		return true
	}
	if c.ctx.Protocol == "openai" {
		return LooksLikeOpenAIReasoningModel(modelID)
	}
	return false
}

// LoginUsesWildcardReasoningEfforts reports whether a wildcard capability
// should receive the provider default list.
func (c Chain) LoginUsesWildcardReasoningEfforts() bool {
	if c.ctx.Protocol == "codex" {
		return true
	}
	if c.IsSensenova() {
		return true
	}
	return strings.Contains(strings.ToLower(c.ctx.BaseURL), "/codex")
}

// NormalizeOpenAICompatibleMessages applies provider-specific OpenAI-compatible
// message fixes on top of the generic request sanitizer.
func (c Chain) NormalizeOpenAICompatibleMessages(messages []map[string]interface{}) []map[string]interface{} {
	if len(messages) == 0 || !c.IsSensenova() {
		return messages
	}

	filtered := make([]map[string]interface{}, 0, len(messages))
	var pendingSystem map[string]interface{}
	flushSystem := func() {
		if pendingSystem == nil {
			return
		}
		filtered = append(filtered, pendingSystem)
		pendingSystem = nil
	}

	for _, msg := range messages {
		role, _ := msg["role"].(string)
		if strings.EqualFold(strings.TrimSpace(role), "system") {
			if pendingSystem == nil {
				pendingSystem = cloneMapStringAny(msg)
				continue
			}
			mergeOpenAICompatibleStringContent(pendingSystem, msg)
			continue
		}
		flushSystem()
		filtered = append(filtered, msg)
	}
	flushSystem()

	return filtered
}

// NormalizeToolCallArguments converts empty or null tool arguments to a JSON
// object literal so OpenAI-compatible providers do not reject the replay.
func (c Chain) NormalizeToolCallArguments(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" || strings.EqualFold(trimmed, "null") {
		return "{}"
	}
	return raw
}

// ReplayableOpenAIReasoningContent returns the reasoning content that should
// be replayed into an OpenAI-compatible request transcript.
func (c Chain) ReplayableOpenAIReasoningContent(toolCalls []map[string]interface{}, reasoning *types.ReasoningBlock) (string, bool) {
	provider := c.ctx.ProviderName
	if reasoning != nil {
		if normalized := strings.ToLower(strings.TrimSpace(reasoning.Provider)); normalized != "" {
			provider = normalized
		}
		if c.IsDeepSeek() || IsDeepSeek(provider, "", reasoning.Provider) {
			return reasoning.RawDisplayText(), true
		}
		if len(toolCalls) == 0 && !reasoning.ReplayRequired {
			return "", false
		}
		if text := strings.TrimSpace(reasoning.DisplayText()); text != "" {
			return text, true
		}
		return "", false
	}
	if c.IsDeepSeek() || IsDeepSeek(provider, "", "") {
		if len(toolCalls) > 0 {
			return "", true
		}
	}
	return "", false
}

// SupportsMaxOutputTokens reports whether the provider backend should allow
// max_output_tokens passthrough.
func (c Chain) SupportsMaxOutputTokens() bool {
	if c.ctx.SupportsMaxOutputTokens != nil {
		return *c.ctx.SupportsMaxOutputTokens
	}
	return !c.IsChatGPTCodexBackend()
}

// IsSensenova reports whether the current context targets Sensenova.
func (c Chain) IsSensenova() bool {
	return IsSensenova(c.ctx.ProviderName, c.ctx.BaseURL)
}

// IsNVIDIA reports whether the current context targets NVIDIA.
func (c Chain) IsNVIDIA() bool {
	return IsNVIDIA(c.ctx.ProviderName, c.ctx.BaseURL)
}

// IsDeepSeek reports whether the current context targets DeepSeek.
func (c Chain) IsDeepSeek() bool {
	return IsDeepSeek(c.ctx.ProviderName, c.ctx.BaseURL, c.ctx.Model)
}

// IsChatGPTCodexBackend reports whether the current context targets the
// ChatGPT Codex backend.
func (c Chain) IsChatGPTCodexBackend() bool {
	return IsChatGPTCodexBackend(c.ctx.BaseURL)
}

func cloneMapStringAny(input map[string]interface{}) map[string]interface{} {
	if len(input) == 0 {
		return make(map[string]interface{})
	}
	output := make(map[string]interface{}, len(input))
	for key, value := range input {
		output[key] = value
	}
	return output
}

func mergeOpenAICompatibleStringContent(dst, src map[string]interface{}) {
	if dst == nil || src == nil {
		return
	}
	existing, _ := dst["content"].(string)
	additional, _ := src["content"].(string)
	if strings.TrimSpace(additional) == "" {
		return
	}
	if strings.TrimSpace(existing) == "" {
		dst["content"] = additional
		return
	}
	dst["content"] = strings.TrimRight(existing, "\n") + "\n\n" + strings.TrimLeft(additional, "\n")
}

// DefaultRuntimeCapability is a convenience helper for callers that do not
// need to retain a Chain instance.
func DefaultRuntimeCapability(ctx Context) (agentconfig.ModelCapabilitySpec, bool) {
	return NewChain(ctx).DefaultRuntimeCapability()
}

// DefaultLoginReasoningEfforts is a convenience helper for login flows.
func DefaultLoginReasoningEfforts(ctx Context) []string {
	return NewChain(ctx).DefaultLoginReasoningEfforts()
}

// LoginModelUsesDefaultReasoningEfforts reports whether a model should inherit
// the provider defaults during login.
func LoginModelUsesDefaultReasoningEfforts(ctx Context, modelID string) bool {
	return NewChain(ctx).LoginModelUsesDefaultReasoningEfforts(modelID)
}

// LoginUsesWildcardReasoningEfforts reports whether a wildcard capability
// should inherit provider defaults during login.
func LoginUsesWildcardReasoningEfforts(ctx Context) bool {
	return NewChain(ctx).LoginUsesWildcardReasoningEfforts()
}

// NormalizeOpenAICompatibleMessages applies provider-specific OpenAI
// compatibility fixes.
func NormalizeOpenAICompatibleMessages(ctx Context, messages []map[string]interface{}) []map[string]interface{} {
	return NewChain(ctx).NormalizeOpenAICompatibleMessages(messages)
}

// NormalizeToolCallArguments normalizes OpenAI-compatible tool call arguments.
func NormalizeToolCallArguments(raw string) string {
	return NewChain(Context{}).NormalizeToolCallArguments(raw)
}

// ReplayableOpenAIReasoningContent returns the reasoning content that should
// be replayed into an OpenAI-compatible request transcript.
func ReplayableOpenAIReasoningContent(ctx Context, toolCalls []map[string]interface{}, reasoning *types.ReasoningBlock) (string, bool) {
	return NewChain(ctx).ReplayableOpenAIReasoningContent(toolCalls, reasoning)
}

// SupportsMaxOutputTokens reports whether a provider/backend should allow the
// max_output_tokens field.
func SupportsMaxOutputTokens(baseURL string, explicit *bool) bool {
	return NewChain(Context{BaseURL: baseURL, SupportsMaxOutputTokens: explicit}).SupportsMaxOutputTokens()
}

// IsSensenova reports whether the provider should use the Sensenova adapter.
func IsSensenova(providerName, baseURL string) bool {
	name := strings.ToLower(strings.TrimSpace(providerName))
	normalizedBaseURL := strings.ToLower(strings.TrimSpace(baseURL))
	return strings.Contains(name, "sensenova") || strings.Contains(normalizedBaseURL, "sensenova.cn")
}

// IsNVIDIA reports whether the provider should use the NVIDIA adapter.
func IsNVIDIA(providerName, baseURL string) bool {
	name := strings.ToLower(strings.TrimSpace(providerName))
	normalizedBaseURL := strings.ToLower(strings.TrimSpace(baseURL))
	return name == "nvidia" || strings.Contains(normalizedBaseURL, "integrate.api.nvidia.com")
}

// IsDeepSeek reports whether the provider should use the DeepSeek adapter.
func IsDeepSeek(providerName, baseURL, modelID string) bool {
	name := strings.ToLower(strings.TrimSpace(providerName))
	normalizedBaseURL := strings.ToLower(strings.TrimSpace(baseURL))
	if strings.Contains(name, "deepseek") || strings.Contains(normalizedBaseURL, "deepseek") {
		return true
	}
	return IsDeepSeekModel(modelID)
}

// IsDeepSeekModel reports whether a model identifier looks like DeepSeek.
func IsDeepSeekModel(modelID string) bool {
	modelID = strings.ToLower(strings.TrimSpace(modelID))
	return strings.Contains(modelID, "deepseek")
}

// LooksLikeOpenAIReasoningModel reports whether a model identifier uses the
// OpenAI reasoning-model naming convention.
func LooksLikeOpenAIReasoningModel(modelID string) bool {
	modelID = strings.ToLower(strings.TrimSpace(modelID))
	modelID = strings.TrimPrefix(modelID, "models/")
	if strings.Contains(modelID, "codex") {
		return true
	}
	for _, prefix := range []string{"gpt-5", "o1", "o3", "o4", "o5"} {
		if strings.HasPrefix(modelID, prefix) {
			return true
		}
	}
	return false
}

// IsChatGPTCodexBackend reports whether the base URL targets ChatGPT Codex.
func IsChatGPTCodexBackend(baseURL string) bool {
	lower := strings.ToLower(strings.TrimSpace(baseURL))
	if lower == "" {
		return false
	}
	return strings.Contains(lower, "chatgpt.com/backend-api/codex/responses")
}
