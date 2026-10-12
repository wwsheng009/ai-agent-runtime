package agentconfig

import (
	"fmt"
	"sort"
	"strings"
)

// ProviderLoginTemplate is a provider entry contributed by an enabled preset,
// projected into the interactive login flow as a "use this template" choice:
// protocol / base URL / headers / compatibility profile come pre-filled from
// the preset, and the user only supplies credentials before the models list
// is fetched.
type ProviderLoginTemplate struct {
	// PresetName is the preset the entry was extracted from (shown in the
	// picker detail so users understand where the prefill came from).
	PresetName string
	// Name is the provider key inside the preset's providers.items fragment.
	Name string
	// Protocol is the runtime protocol (openai / codex / anthropic / ...).
	Protocol string
	// BaseURL is the pre-filled upstream base URL.
	BaseURL string
	// ModelsPath is the optional models list endpoint path override.
	ModelsPath string
}

// LoadPresetProviderTemplates extracts every provider entry declared by the
// enabled presets (built-in + system directory + user presets.yaml, filtered
// by hostname match) that carries a usable base URL. Entries are deduplicated
// by provider name (first occurrence wins, in preset load order) and sorted
// by provider name for a stable picker ordering.
func LoadPresetProviderTemplates(host string) []ProviderLoginTemplate {
	presets, err := LoadSystemPresets()
	if err != nil {
		// Preset layers are auxiliary; a broken preset file must not break
		// the login flow — it simply loses its template suggestions.
		return nil
	}
	type entry struct {
		template ProviderLoginTemplate
		preset   string
	}
	byName := make(map[string]entry)
	for _, preset := range presets {
		if !preset.IsEnabled() || len(preset.Config) == 0 || !PresetMatchesHost(preset, host) {
			continue
		}
		cfg, err := preset.DecodedConfig()
		if err != nil || cfg == nil || len(cfg.Providers.Items) == 0 {
			continue
		}
		for name, provider := range cfg.Providers.Items {
			baseURL := strings.TrimSpace(provider.BaseURL)
			if baseURL == "" {
				continue
			}
			if _, seen := byName[name]; seen {
				// First preset in load order wins; skip later duplicates.
				continue
			}
			byName[name] = entry{
				template: ProviderLoginTemplate{
					PresetName: preset.Name,
					Name:       name,
					Protocol:   strings.TrimSpace(provider.GetProtocol()),
					BaseURL:    baseURL,
					ModelsPath: strings.TrimSpace(provider.ModelsPath),
				},
				preset: preset.Name,
			}
		}
	}
	names := make([]string, 0, len(byName))
	for name := range byName {
		names = append(names, name)
	}
	sort.Strings(names)
	templates := make([]ProviderLoginTemplate, 0, len(names))
	for _, name := range names {
		templates = append(templates, byName[name].template)
	}
	return templates
}

// TemplateProvider is a debug/validation helper that decodes the template
// back into a Provider struct. It is not used by the merge path (templates
// are prefill suggestions, never runtime config layers).
func (t ProviderLoginTemplate) Summary() string {
	if t.PresetName == "" {
		return fmt.Sprintf("%s (%s)", t.Name, t.Protocol)
	}
	return fmt.Sprintf("%s (%s, preset: %s)", t.Name, t.Protocol, t.PresetName)
}
