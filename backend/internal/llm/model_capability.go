package llm

import (
	"strings"

	agentconfig "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
)

const defaultCompactMaxTokens = 2048
const defaultCompactReasoningEffort = "none"

// ModelCapabilityResolver allows runtime providers to expose provider/model
// capability metadata without forcing callers to know the concrete provider type.
type ModelCapabilityResolver interface {
	ResolveModelCapability(requestedModel string) (resolvedModel string, capability agentconfig.ModelCapabilitySpec, ok bool)
}

// ResolveModelCapabilitySpec finds a model capability by exact model name first
// and then falls back to the wildcard entry.
func ResolveModelCapabilitySpec(
	model string,
	modelCapabilities map[string]agentconfig.ModelCapabilitySpec,
) (agentconfig.ModelCapabilitySpec, bool) {
	if len(modelCapabilities) == 0 {
		return agentconfig.ModelCapabilitySpec{}, false
	}
	if exact, ok := modelCapabilities[strings.TrimSpace(model)]; ok {
		return CloneModelCapabilitySpec(exact), true
	}
	if wildcard, ok := modelCapabilities["*"]; ok {
		return CloneModelCapabilitySpec(wildcard), true
	}
	return agentconfig.ModelCapabilitySpec{}, false
}

// CloneModelCapabilitySpec returns a detached copy of the capability spec.
func CloneModelCapabilitySpec(input agentconfig.ModelCapabilitySpec) agentconfig.ModelCapabilitySpec {
	cloned := input
	if len(input.InputModalities) > 0 {
		cloned.InputModalities = append([]string(nil), input.InputModalities...)
	}
	if len(input.ReasoningEfforts) > 0 {
		cloned.ReasoningEfforts = append([]string(nil), input.ReasoningEfforts...)
	}
	if len(input.ReasoningEffortBudgets) > 0 {
		cloned.ReasoningEffortBudgets = make(map[string]int, len(input.ReasoningEffortBudgets))
		for key, value := range input.ReasoningEffortBudgets {
			cloned.ReasoningEffortBudgets[key] = value
		}
	}
	return cloned
}

// CloneModelCapabilityMap returns a detached copy of the capability map.
func CloneModelCapabilityMap(input map[string]agentconfig.ModelCapabilitySpec) map[string]agentconfig.ModelCapabilitySpec {
	if len(input) == 0 {
		return nil
	}
	output := make(map[string]agentconfig.ModelCapabilitySpec, len(input))
	for key, value := range input {
		output[key] = CloneModelCapabilitySpec(value)
	}
	return output
}

// ReasoningModelEnabled returns the explicit reasoning-model flag from config,
// with an explicit request override for legacy callers that still pass it
// separately.
func ReasoningModelEnabled(capability agentconfig.ModelCapabilitySpec, explicit bool) bool {
	if capability.ReasoningModel {
		return true
	}
	return explicit
}

// CompactSummarySettings resolves compact-specific request settings from a
// model capability with safe defaults when the capability does not define them.
func CompactSummarySettings(capability agentconfig.ModelCapabilitySpec) (maxTokens int, reasoningEffort string) {
	maxTokens = capability.MaxTokens
	if maxTokens <= 0 {
		maxTokens = defaultCompactMaxTokens
	}

	reasoningEffort = strings.TrimSpace(capability.CompactReasoningEffort)
	if reasoningEffort == "" {
		reasoningEffort = defaultCompactReasoningEffort
	}
	return maxTokens, reasoningEffort
}

// ResolveRuntimeModelCapability resolves the effective provider/model pair for
// the next runtime call and returns the matched model capability when available.
func ResolveRuntimeModelCapability(
	runtime *LLMRuntime,
	providerName string,
	model string,
) (resolvedProvider string, resolvedModel string, capability agentconfig.ModelCapabilitySpec, ok bool) {
	if runtime == nil {
		return "", "", agentconfig.ModelCapabilitySpec{}, false
	}

	resolvedProvider = strings.TrimSpace(providerName)
	if resolvedProvider == "" {
		resolvedProvider = runtime.resolveRegisteredProviderName(model)
	}
	if resolvedProvider == "" && runtime.config != nil {
		resolvedProvider = strings.TrimSpace(runtime.config.DefaultProvider)
	}
	if resolvedProvider == "" {
		return "", strings.TrimSpace(model), agentconfig.ModelCapabilitySpec{}, false
	}

	if strings.TrimSpace(model) == "" && runtime.config != nil {
		model = strings.TrimSpace(runtime.config.DefaultModel)
	}
	resolvedModel = strings.TrimSpace(model)

	provider, err := runtime.GetProvider(resolvedProvider)
	if err != nil || provider == nil {
		return resolvedProvider, resolvedModel, agentconfig.ModelCapabilitySpec{}, false
	}

	resolver, hasResolver := provider.(ModelCapabilityResolver)
	if !hasResolver || resolver == nil {
		return resolvedProvider, resolvedModel, agentconfig.ModelCapabilitySpec{}, false
	}

	resolvedModel, capability, ok = resolver.ResolveModelCapability(resolvedModel)
	if !ok {
		return resolvedProvider, strings.TrimSpace(resolvedModel), agentconfig.ModelCapabilitySpec{}, false
	}
	return resolvedProvider, strings.TrimSpace(resolvedModel), capability, true
}
