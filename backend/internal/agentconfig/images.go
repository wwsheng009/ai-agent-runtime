package agentconfig

import (
	"errors"
	"fmt"
	"sort"
	"strings"
)

// ErrNoImagesGenerationsProvider is returned when no provider/model pair can
// satisfy the image generations API requirement.
var ErrNoImagesGenerationsProvider = errors.New("no image generations provider configured")

// ErrNoCodexNativeImageGenerationProvider is returned when no provider/model
// pair can expose Codex native image_generation.
var ErrNoCodexNativeImageGenerationProvider = errors.New("no codex native image generation provider configured")

// ImagesGenerationsHint narrows provider selection for image generation calls.
type ImagesGenerationsHint struct {
	ProviderName string
	Model        string
}

// ImagesGenerationsSelection identifies the provider/model chosen for an image
// generation request.
type ImagesGenerationsSelection struct {
	ProviderName string
	Provider     Provider
	Model        string
}

// CodexNativeImageGenerationHint narrows provider selection for Codex native
// image_generation calls.
type CodexNativeImageGenerationHint struct {
	ProviderName string
	Model        string
}

// CodexNativeImageGenerationSelection identifies the provider/model chosen for
// a Codex native image_generation request.
type CodexNativeImageGenerationSelection struct {
	ProviderName string
	Provider     Provider
	Model        string
}

// ResolveModelCapabilitySpec returns the capability configuration for the
// requested model when one is configured.
func ResolveModelCapabilitySpec(model string, modelCapabilities map[string]ModelCapabilitySpec) (ModelCapabilitySpec, bool) {
	model = strings.TrimSpace(model)
	if model == "" || len(modelCapabilities) == 0 {
		return ModelCapabilitySpec{}, false
	}
	if spec, ok := modelCapabilities[model]; ok {
		return spec, true
	}
	for name, spec := range modelCapabilities {
		if strings.EqualFold(strings.TrimSpace(name), model) {
			return spec, true
		}
	}
	return ModelCapabilitySpec{}, false
}

// SelectImagesGenerationsProvider resolves the first provider/model pair that
// advertises native support for the images generations API.
func SelectImagesGenerationsProvider(cfg *Config, hint ImagesGenerationsHint) (*ImagesGenerationsSelection, error) {
	all, err := SelectAllImagesGenerationsProviders(cfg, hint)
	if err != nil {
		return nil, err
	}
	return all[0], nil
}

// SelectAllImagesGenerationsProviders resolves all provider/model pairs that
// advertise native support for the images generations API, ordered by
// preference (exact model/provider match first, then alphabetical).
// The first element is the primary choice; subsequent elements are failover
// candidates.
func SelectAllImagesGenerationsProviders(cfg *Config, hint ImagesGenerationsHint) ([]*ImagesGenerationsSelection, error) {
	if cfg == nil || len(cfg.Providers.Items) == 0 {
		return nil, ErrNoImagesGenerationsProvider
	}

	hint.ProviderName = strings.TrimSpace(hint.ProviderName)
	hint.Model = strings.TrimSpace(hint.Model)

	providerNames := make([]string, 0, len(cfg.Providers.Items))
	if hint.ProviderName != "" {
		providerNames = append(providerNames, hint.ProviderName)
	} else {
		for name := range cfg.Providers.Items {
			providerNames = append(providerNames, name)
		}
		sort.Strings(providerNames)
	}

	var results []*ImagesGenerationsSelection
	for _, providerName := range providerNames {
		provider, ok := cfg.Providers.Items[providerName]
		if !ok || !provider.Enabled {
			continue
		}
		models := imagesGenerationCandidateModels(provider, hint.Model)
		for _, model := range models {
			spec, ok := ResolveModelCapabilitySpec(model, provider.ModelCapabilities)
			if !ok || !spec.NativeTools.ImagesGenerationsAPI {
				continue
			}
			selectedModel := strings.TrimSpace(model)
			if selectedModel == "" {
				continue
			}
			results = append(results, &ImagesGenerationsSelection{
				ProviderName: providerName,
				Provider:     provider,
				Model:        selectedModel,
			})
		}
	}

	if len(results) == 0 {
		return nil, ErrNoImagesGenerationsProvider
	}
	return results, nil
}

// SelectAllCodexNativeImageGenerationProviders resolves all provider/model
// pairs that can expose the Codex Responses image_generation native tool.
func SelectAllCodexNativeImageGenerationProviders(cfg *Config, hint CodexNativeImageGenerationHint) ([]*CodexNativeImageGenerationSelection, error) {
	if cfg == nil || len(cfg.Providers.Items) == 0 {
		return nil, ErrNoCodexNativeImageGenerationProvider
	}

	hint.ProviderName = strings.TrimSpace(hint.ProviderName)
	hint.Model = strings.TrimSpace(hint.Model)

	providerNames := make([]string, 0, len(cfg.Providers.Items))
	if hint.ProviderName != "" {
		providerNames = append(providerNames, hint.ProviderName)
	} else {
		for name := range cfg.Providers.Items {
			providerNames = append(providerNames, name)
		}
		sort.Strings(providerNames)
	}

	var results []*CodexNativeImageGenerationSelection
	for _, providerName := range providerNames {
		provider, ok := cfg.Providers.Items[providerName]
		if !ok || !provider.Enabled || !strings.EqualFold(provider.GetProtocol(), "codex") {
			continue
		}
		models := imagesGenerationCandidateModels(provider, hint.Model)
		for _, model := range models {
			if strings.TrimSpace(model) == "*" {
				continue
			}
			if !ProviderHasCodexNativeImageGeneration(provider, model) {
				continue
			}
			selectedModel := strings.TrimSpace(model)
			if selectedModel == "" {
				continue
			}
			results = append(results, &CodexNativeImageGenerationSelection{
				ProviderName: providerName,
				Provider:     provider,
				Model:        selectedModel,
			})
		}
	}

	if len(results) == 0 {
		return nil, ErrNoCodexNativeImageGenerationProvider
	}
	return results, nil
}

func imagesGenerationCandidateModels(provider Provider, requestedModel string) []string {
	requestedModel = strings.TrimSpace(requestedModel)
	if requestedModel != "" {
		return []string{requestedModel}
	}

	candidates := make([]string, 0, 1+len(provider.SupportedModels)+len(provider.ModelCapabilities))
	add := func(model string, seen map[string]struct{}) {
		model = strings.TrimSpace(model)
		if model == "" {
			return
		}
		key := strings.ToLower(model)
		if _, exists := seen[key]; exists {
			return
		}
		seen[key] = struct{}{}
		candidates = append(candidates, model)
	}

	seen := make(map[string]struct{})
	add(provider.DefaultModel, seen)

	supported := append([]string(nil), provider.SupportedModels...)
	sort.Strings(supported)
	for _, model := range supported {
		add(model, seen)
	}

	keys := make([]string, 0, len(provider.ModelCapabilities))
	for model := range provider.ModelCapabilities {
		keys = append(keys, model)
	}
	sort.Strings(keys)
	for _, model := range keys {
		add(model, seen)
	}

	return candidates
}

// ProviderHasImagesGenerationsAPI reports whether the provider/model pair
// advertises support for the images generations endpoint.
func ProviderHasImagesGenerationsAPI(provider Provider, model string) bool {
	spec, ok := ResolveModelCapabilitySpec(model, provider.ModelCapabilities)
	return ok && spec.NativeTools.ImagesGenerationsAPI
}

// ProviderHasCodexNativeImageGeneration reports whether the provider/model pair
// can expose Codex native image_generation.
func ProviderHasCodexNativeImageGeneration(provider Provider, model string) bool {
	if !strings.EqualFold(provider.GetProtocol(), "codex") {
		return false
	}
	spec, ok := ResolveModelCapabilitySpec(model, provider.ModelCapabilities)
	if !ok && strings.TrimSpace(model) != "*" {
		spec, ok = provider.ModelCapabilities["*"]
	}
	return ok && ModelCapabilityHasTextImageNativeGeneration(spec)
}

// ModelCapabilityHasTextImageNativeGeneration reports whether a capability
// enables native image_generation and accepts both text and image modalities.
func ModelCapabilityHasTextImageNativeGeneration(spec ModelCapabilitySpec) bool {
	if !spec.NativeTools.ImageGeneration {
		return false
	}
	modalities := make(map[string]struct{}, len(spec.InputModalities))
	for _, modality := range spec.InputModalities {
		trimmed := strings.ToLower(strings.TrimSpace(modality))
		if trimmed != "" {
			modalities[trimmed] = struct{}{}
		}
	}
	_, hasText := modalities["text"]
	_, hasImage := modalities["image"]
	return hasText && hasImage
}

// ImagesGenerationsProviderSummary returns a short human-readable summary of a
// selected provider/model pair.
func ImagesGenerationsProviderSummary(selection *ImagesGenerationsSelection) string {
	if selection == nil {
		return ""
	}
	parts := make([]string, 0, 2)
	if name := strings.TrimSpace(selection.ProviderName); name != "" {
		parts = append(parts, fmt.Sprintf("provider=%s", name))
	}
	if model := strings.TrimSpace(selection.Model); model != "" {
		parts = append(parts, fmt.Sprintf("model=%s", model))
	}
	return strings.Join(parts, " ")
}
