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
	ctx      Context
	adapters []Adapter
}

// NewChain constructs a compatibility chain for the given provider context.
func NewChain(ctx Context) Chain {
	normalized := normalizeContext(ctx)
	return Chain{
		ctx:      normalized,
		adapters: adaptersForContext(normalized),
	}
}

func normalizeContext(ctx Context) Context {
	ctx.ProviderName = strings.TrimSpace(ctx.ProviderName)
	ctx.Protocol = strings.ToLower(strings.TrimSpace(ctx.Protocol))
	ctx.BaseURL = strings.TrimSpace(ctx.BaseURL)
	ctx.APIPath = strings.TrimSpace(ctx.APIPath)
	ctx.Model = strings.TrimSpace(ctx.Model)
	return ctx
}

func cloneCapabilitySpec(spec agentconfig.ModelCapabilitySpec) agentconfig.ModelCapabilitySpec {
	if len(spec.InputModalities) > 0 {
		spec.InputModalities = append([]string(nil), spec.InputModalities...)
	}
	if len(spec.ReasoningEfforts) > 0 {
		spec.ReasoningEfforts = append([]string(nil), spec.ReasoningEfforts...)
	}
	if len(spec.ReasoningEffortBudgets) > 0 {
		budgets := make(map[string]int, len(spec.ReasoningEffortBudgets))
		for key, value := range spec.ReasoningEffortBudgets {
			budgets[key] = value
		}
		spec.ReasoningEffortBudgets = budgets
	}
	return spec
}

func cloneStringSlice(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	return append([]string(nil), values...)
}

func cloneCapabilityMap(input map[string]agentconfig.ModelCapabilitySpec) map[string]agentconfig.ModelCapabilitySpec {
	if len(input) == 0 {
		return nil
	}
	output := make(map[string]agentconfig.ModelCapabilitySpec, len(input))
	for key, value := range input {
		if strings.TrimSpace(key) == "" {
			continue
		}
		output[strings.TrimSpace(key)] = cloneCapabilitySpec(value)
	}
	if len(output) == 0 {
		return nil
	}
	return output
}

// DefaultRuntimeCapability returns a provider-specific fallback capability used
// by runtime request filtering.
func (c Chain) DefaultRuntimeCapability() (agentconfig.ModelCapabilitySpec, bool) {
	for _, adapter := range c.adapters {
		if capability, ok := adapter.DefaultRuntimeCapability(c.ctx); ok {
			return cloneCapabilitySpec(capability), true
		}
	}
	return agentconfig.ModelCapabilitySpec{}, false
}

// DefaultCapabilities returns the provider fallback capability catalog used by
// runtime request filtering.
func (c Chain) DefaultCapabilities() map[string]agentconfig.ModelCapabilitySpec {
	capability, ok := c.DefaultRuntimeCapability()
	if !ok {
		return nil
	}
	return map[string]agentconfig.ModelCapabilitySpec{
		"*": cloneCapabilitySpec(capability),
	}
}

// MergeCapabilities merges configured model capabilities with provider fallback
// capabilities. Configured wildcard capabilities always win.
func (c Chain) MergeCapabilities(capabilities map[string]agentconfig.ModelCapabilitySpec) map[string]agentconfig.ModelCapabilitySpec {
	if len(capabilities) == 0 && len(c.ctx.ConfiguredCapabilities) > 0 {
		capabilities = c.ctx.ConfiguredCapabilities
	}
	defaults := c.DefaultCapabilities()
	if len(defaults) == 0 {
		return capabilities
	}
	if len(capabilities) == 0 {
		return defaults
	}
	if _, exists := capabilities["*"]; exists {
		return capabilities
	}
	merged := cloneCapabilityMap(capabilities)
	if merged == nil {
		merged = make(map[string]agentconfig.ModelCapabilitySpec, len(defaults))
	}
	for model, capability := range defaults {
		if _, exists := merged[model]; exists {
			continue
		}
		merged[model] = cloneCapabilitySpec(capability)
	}
	return merged
}

// DefaultLoginReasoningEfforts returns the default reasoning effort list used
// by login/model discovery when a provider does not advertise one explicitly.
func (c Chain) DefaultLoginReasoningEfforts() []string {
	for _, adapter := range c.adapters {
		if efforts, ok := adapter.DefaultLoginReasoningEfforts(c.ctx); ok {
			return cloneStringSlice(efforts)
		}
	}
	return nil
}

// LoginModelUsesDefaultReasoningEfforts reports whether a discovered model
// should inherit the provider's default effort list.
func (c Chain) LoginModelUsesDefaultReasoningEfforts(modelID string) bool {
	if c.LoginUsesWildcardReasoningEfforts() {
		return true
	}
	for _, adapter := range c.adapters {
		if uses, ok := adapter.LoginModelUsesDefaultReasoningEfforts(c.ctx, modelID); ok {
			return uses
		}
	}
	return false
}

// LoginUsesWildcardReasoningEfforts reports whether a wildcard capability
// should receive the provider default list.
func (c Chain) LoginUsesWildcardReasoningEfforts() bool {
	for _, adapter := range c.adapters {
		if uses, ok := adapter.LoginUsesWildcardReasoningEfforts(c.ctx); ok {
			return uses
		}
	}
	return false
}

// NormalizeOpenAICompatibleMessages applies provider-specific OpenAI-compatible
// message fixes on top of the generic request sanitizer.
func (c Chain) NormalizeOpenAICompatibleMessages(messages []map[string]interface{}) []map[string]interface{} {
	for _, adapter := range c.adapters {
		if normalized, ok := adapter.NormalizeOpenAICompatibleMessages(c.ctx, messages); ok {
			messages = normalized
		}
	}
	return messages
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

// NormalizeAssistantMessage applies provider-specific response fixes after the
// protocol adapter has parsed the provider response.
func (c Chain) NormalizeAssistantMessage(message map[string]interface{}) map[string]interface{} {
	for _, adapter := range c.adapters {
		if normalized, ok := adapter.NormalizeAssistantMessage(c.ctx, message); ok {
			message = normalized
		}
	}
	return message
}

// ReplayableOpenAIReasoningContent returns the reasoning content that should
// be replayed into an OpenAI-compatible request transcript.
func (c Chain) ReplayableOpenAIReasoningContent(toolCalls []map[string]interface{}, reasoning *types.ReasoningBlock) (string, bool) {
	for _, adapter := range c.adapters {
		if content, ok := adapter.ReplayableOpenAIReasoningContent(c.ctx, toolCalls, reasoning); ok {
			return content, true
		}
	}
	if reasoning != nil {
		if normalizedProvider := strings.TrimSpace(reasoning.Provider); normalizedProvider != "" {
			providerCtx := c.ctx
			providerCtx.ProviderName = normalizedProvider
			providerCtx = normalizeContext(providerCtx)
			for _, adapter := range adaptersForContext(providerCtx) {
				if adapterAlreadySelected(c.adapters, adapter.Name()) {
					continue
				}
				if content, ok := adapter.ReplayableOpenAIReasoningContent(providerCtx, toolCalls, reasoning); ok {
					return content, true
				}
			}
		}
	}

	if reasoning == nil {
		return "", false
	}
	if len(toolCalls) == 0 && !reasoning.ReplayRequired {
		return "", false
	}
	if text := strings.TrimSpace(reasoning.DisplayText()); text != "" {
		return text, true
	}
	return "", false
}

// SupportsMaxOutputTokens reports whether the provider backend should allow
// max_output_tokens passthrough.
func (c Chain) SupportsMaxOutputTokens() bool {
	if c.ctx.SupportsMaxOutputTokens != nil {
		return *c.ctx.SupportsMaxOutputTokens
	}
	for _, adapter := range c.adapters {
		if supported, ok := adapter.SupportsMaxOutputTokens(c.ctx); ok {
			return supported
		}
	}
	return true
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

func isSystemRole(role string) bool {
	return strings.EqualFold(strings.TrimSpace(role), "system")
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

// DefaultCapabilities returns provider fallback model capabilities.
func DefaultCapabilities(ctx Context) map[string]agentconfig.ModelCapabilitySpec {
	return NewChain(ctx).DefaultCapabilities()
}

// MergeCapabilities merges configured capabilities with provider fallback
// capabilities using the selected provider context.
func MergeCapabilities(ctx Context, configured map[string]agentconfig.ModelCapabilitySpec) map[string]agentconfig.ModelCapabilitySpec {
	return NewChain(ctx).MergeCapabilities(configured)
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

// NormalizeAssistantMessage applies provider-specific response compatibility
// fixes after protocol parsing.
func NormalizeAssistantMessage(ctx Context, message map[string]interface{}) map[string]interface{} {
	return NewChain(ctx).NormalizeAssistantMessage(message)
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
