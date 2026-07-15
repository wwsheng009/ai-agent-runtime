package modelrouting

import (
	"sort"
	"strings"

	agentconfig "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	runtimellm "github.com/wwsheng009/ai-agent-runtime/internal/llm"
)

// ProviderBrief is the diagnostic subset of a configured provider.
type ProviderBrief struct {
	Name           string   `json:"name"`
	DefaultModel   string   `json:"default_model,omitempty"`
	Supported      []string `json:"supported_models,omitempty"`
	CapabilityKeys []string `json:"capability_keys,omitempty"`
}

// ConfigCatalog adapts the runtime config provider catalog to the route resolver.
// Provider names always win over model aliases. Ambiguous model aliases are
// deliberately left unresolved instead of depending on Go map iteration order.
type ConfigCatalog struct {
	cfg       *agentconfig.Config
	providers map[string]string
	aliases   map[string]string
	ambiguous map[string]struct{}
}

func NewConfigCatalog(cfg *agentconfig.Config) *ConfigCatalog {
	catalog := &ConfigCatalog{
		cfg:       cfg,
		providers: map[string]string{},
		aliases:   map[string]string{},
		ambiguous: map[string]struct{}{},
	}
	if cfg == nil {
		return catalog
	}

	for name, provider := range cfg.Providers.Items {
		if provider.Enabled {
			catalog.providers[normalizeCatalogKey(name)] = name
		}
	}
	for name, provider := range cfg.Providers.Items {
		if !provider.Enabled {
			continue
		}
		catalog.addModelAlias(provider.DefaultModel, name)
		for _, model := range provider.SupportedModels {
			catalog.addModelAlias(model, name)
		}
	}
	return catalog
}

func (c *ConfigCatalog) addModelAlias(alias, provider string) {
	key := normalizeCatalogKey(alias)
	provider = strings.TrimSpace(provider)
	if key == "" || provider == "" {
		return
	}
	if _, isProviderName := c.providers[key]; isProviderName {
		return
	}
	if _, ambiguous := c.ambiguous[key]; ambiguous {
		return
	}
	if current, exists := c.aliases[key]; exists && !strings.EqualFold(current, provider) {
		delete(c.aliases, key)
		c.ambiguous[key] = struct{}{}
		return
	}
	c.aliases[key] = provider
}

func normalizeCatalogKey(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

func (c *ConfigCatalog) ResolveProviderName(name string) string {
	if c == nil {
		return ""
	}
	key := normalizeCatalogKey(name)
	if key == "" {
		return ""
	}
	if provider := c.providers[key]; provider != "" {
		return provider
	}
	if _, ambiguous := c.ambiguous[key]; ambiguous {
		return ""
	}
	return c.aliases[key]
}

func (c *ConfigCatalog) DefaultModel(provider string) string {
	cfgProvider, _, ok := c.resolveProviderModel(provider, "")
	if !ok {
		return ""
	}
	model := strings.TrimSpace(cfgProvider.DefaultModel)
	if model == "" {
		return ""
	}
	return agentconfig.ApplyModelMapping(&cfgProvider, model)
}

func (c *ConfigCatalog) SupportsModel(provider, model string) (bool, bool) {
	cfgProvider, mappedModel, ok := c.resolveProviderModel(provider, model)
	if !ok {
		return false, false
	}
	if len(cfgProvider.ModelCapabilities) > 0 {
		if _, found := runtimellm.ResolveModelCapabilitySpec(mappedModel, cfgProvider.ModelCapabilities); found {
			return true, true
		}
		originalModel := strings.TrimSpace(model)
		if originalModel != "" && mappedModel != originalModel {
			if _, found := runtimellm.ResolveModelCapabilitySpec(originalModel, cfgProvider.ModelCapabilities); found {
				return true, true
			}
		}
		return false, true
	}
	if len(cfgProvider.SupportedModels) > 0 {
		for _, supported := range cfgProvider.SupportedModels {
			if strings.EqualFold(strings.TrimSpace(supported), strings.TrimSpace(model)) ||
				strings.EqualFold(strings.TrimSpace(supported), mappedModel) {
				return true, true
			}
		}
		return false, true
	}
	return false, false
}

func (c *ConfigCatalog) SupportsReasoningEffort(provider, model, effort string) (bool, bool) {
	effort = strings.TrimSpace(effort)
	if effort == "" {
		return true, true
	}
	capability, ok := c.resolveModelCapability(provider, model)
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

func (c *ConfigCatalog) SupportedReasoningEfforts(provider, model string) ([]string, bool) {
	capability, ok := c.resolveModelCapability(provider, model)
	if !ok {
		return nil, false
	}
	efforts := make([]string, 0, len(capability.ReasoningEfforts))
	for _, effort := range capability.ReasoningEfforts {
		if effort = strings.TrimSpace(effort); effort != "" {
			efforts = append(efforts, effort)
		}
	}
	return efforts, true
}

func (c *ConfigCatalog) resolveProviderModel(provider, model string) (agentconfig.Provider, string, bool) {
	if c == nil || c.cfg == nil {
		return agentconfig.Provider{}, "", false
	}
	provider = c.ResolveProviderName(provider)
	if provider == "" {
		return agentconfig.Provider{}, "", false
	}
	cfgProvider, ok := c.cfg.Providers.Items[provider]
	if !ok || !cfgProvider.Enabled {
		return agentconfig.Provider{}, "", false
	}
	model = strings.TrimSpace(model)
	if model == "" {
		model = strings.TrimSpace(cfgProvider.DefaultModel)
	}
	if model == "" {
		return cfgProvider, "", false
	}
	return cfgProvider, agentconfig.ApplyModelMapping(&cfgProvider, model), true
}

func (c *ConfigCatalog) resolveModelCapability(provider, model string) (agentconfig.ModelCapabilitySpec, bool) {
	cfgProvider, mappedModel, ok := c.resolveProviderModel(provider, model)
	if !ok {
		return agentconfig.ModelCapabilitySpec{}, false
	}
	capability, found := runtimellm.ResolveModelCapabilitySpec(mappedModel, cfgProvider.ModelCapabilities)
	originalModel := strings.TrimSpace(model)
	if !found && mappedModel != originalModel {
		capability, found = runtimellm.ResolveModelCapabilitySpec(originalModel, cfgProvider.ModelCapabilities)
	}
	return capability, found
}

func (c *ConfigCatalog) ProviderBriefs() []ProviderBrief {
	if c == nil || c.cfg == nil {
		return nil
	}
	names := make([]string, 0, len(c.cfg.Providers.Items))
	for name, provider := range c.cfg.Providers.Items {
		if provider.Enabled {
			names = append(names, name)
		}
	}
	sort.Strings(names)

	briefs := make([]ProviderBrief, 0, len(names))
	for _, name := range names {
		provider := c.cfg.Providers.Items[name]
		capabilityKeys := make([]string, 0, len(provider.ModelCapabilities))
		for key := range provider.ModelCapabilities {
			capabilityKeys = append(capabilityKeys, key)
		}
		sort.Strings(capabilityKeys)
		briefs = append(briefs, ProviderBrief{
			Name:           name,
			DefaultModel:   strings.TrimSpace(provider.DefaultModel),
			Supported:      append([]string(nil), provider.SupportedModels...),
			CapabilityKeys: capabilityKeys,
		})
	}
	return briefs
}

var _ ProviderCatalog = (*ConfigCatalog)(nil)
