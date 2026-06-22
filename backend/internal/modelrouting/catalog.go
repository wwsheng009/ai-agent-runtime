package modelrouting

import (
	"strings"

	agentconfig "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	"github.com/wwsheng009/ai-agent-runtime/internal/llm"
)

// RuntimeCatalog adapts the registered LLM runtime provider catalog for route
// validation.
type RuntimeCatalog struct {
	Runtime *llm.LLMRuntime
}

func NewRuntimeCatalog(runtime *llm.LLMRuntime) RuntimeCatalog {
	return RuntimeCatalog{Runtime: runtime}
}

func (c RuntimeCatalog) ResolveProviderName(name string) string {
	if c.Runtime == nil {
		return strings.TrimSpace(name)
	}
	return c.Runtime.ResolveProviderName(name)
}

func (c RuntimeCatalog) DefaultModel(provider string) string {
	if c.Runtime == nil {
		return ""
	}
	provider = c.ResolveProviderName(provider)
	if provider == "" {
		return ""
	}
	runtimeProvider, err := c.Runtime.GetProvider(provider)
	if err != nil || runtimeProvider == nil {
		return ""
	}
	if withDefault, ok := runtimeProvider.(interface{ DefaultModelName() string }); ok {
		return strings.TrimSpace(withDefault.DefaultModelName())
	}
	if withDefault, ok := runtimeProvider.(interface{ DefaultModel() string }); ok {
		return strings.TrimSpace(withDefault.DefaultModel())
	}
	return ""
}

func (c RuntimeCatalog) SupportsModel(provider, model string) (bool, bool) {
	if c.Runtime == nil {
		return false, false
	}
	provider = c.ResolveProviderName(provider)
	if provider == "" {
		return false, false
	}
	model = strings.TrimSpace(model)
	if model == "" {
		model = c.DefaultModel(provider)
	}
	if model == "" {
		return false, false
	}
	if _, _, _, ok := llm.ResolveRuntimeModelCapability(c.Runtime, provider, model); ok {
		return true, true
	}
	runtimeProvider, err := c.Runtime.GetProvider(provider)
	if err != nil || runtimeProvider == nil {
		return false, false
	}
	if withCapabilities, ok := runtimeProvider.(interface {
		ListModelCapabilities() map[string]agentconfig.ModelCapabilitySpec
	}); ok && len(withCapabilities.ListModelCapabilities()) > 0 {
		return false, true
	}
	return false, false
}

func (c RuntimeCatalog) SupportsReasoningEffort(provider, model, effort string) (bool, bool) {
	effort = strings.TrimSpace(effort)
	if effort == "" {
		return true, true
	}
	_, _, capability, ok := llm.ResolveRuntimeModelCapability(c.Runtime, provider, model)
	if !ok {
		return false, false
	}
	if len(capability.ReasoningEfforts) == 0 {
		return capability.ReasoningModel, true
	}
	for _, allowed := range capability.ReasoningEfforts {
		if strings.EqualFold(strings.TrimSpace(allowed), effort) {
			return true, true
		}
	}
	return false, true
}

func (c RuntimeCatalog) SupportedReasoningEfforts(provider, model string) ([]string, bool) {
	_, _, capability, ok := llm.ResolveRuntimeModelCapability(c.Runtime, provider, model)
	if !ok {
		return nil, false
	}
	if len(capability.ReasoningEfforts) == 0 {
		return nil, true
	}
	efforts := make([]string, 0, len(capability.ReasoningEfforts))
	for _, effort := range capability.ReasoningEfforts {
		if effort = strings.TrimSpace(effort); effort != "" {
			efforts = append(efforts, effort)
		}
	}
	return efforts, true
}
