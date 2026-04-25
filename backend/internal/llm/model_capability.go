package llm

import (
	"strings"

	agentconfig "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
)

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
	return cloned
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
