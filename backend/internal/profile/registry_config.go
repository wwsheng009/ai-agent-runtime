package profile

import "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"

// NewRegistryFromProfilesConfig builds a registry from global profile config.
func NewRegistryFromProfilesConfig(cfg *agentconfig.ProfilesConfig) *Registry {
	if cfg == nil {
		return NewRegistry("")
	}
	registry := NewRegistry(cfg.Root)
	for name, item := range cfg.Items {
		if item.Root == "" {
			continue
		}
		_ = registry.Register(name, item.Root)
	}
	return registry
}

// ResolveRef resolves a profile reference via registry and executes Resolve.
func ResolveRef(registry *Registry, ref string, options ResolveOptions) (*ResolvedAgent, error) {
	root, err := registry.Resolve(ref)
	if err != nil {
		return nil, err
	}
	options.Root = root
	return Resolve(options)
}
