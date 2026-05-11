package agentconfig

import "testing"

func TestSelectImagesGenerationsProvider_SelectsMatchingModel(t *testing.T) {
	cfg := &Config{
		Providers: ProvidersConfig{
			Items: map[string]Provider{
				"openai_image": {
					Enabled:      true,
					Type:         "openai",
					BaseURL:      "https://api.openai.com",
					APIKey:       "test-key",
					DefaultModel: "gpt-image-2",
					SupportedModels: []string{
						"gpt-image-1.5",
						"gpt-image-2",
					},
					ModelCapabilities: map[string]ModelCapabilitySpec{
						"gpt-image-1.5": {
							NativeTools: NativeToolCapabilities{ImagesGenerationsAPI: true},
						},
						"gpt-image-2": {
							NativeTools: NativeToolCapabilities{ImagesGenerationsAPI: true},
						},
					},
				},
			},
		},
	}

	selection, err := SelectImagesGenerationsProvider(cfg, ImagesGenerationsHint{Model: "gpt-image-1.5"})
	if err != nil {
		t.Fatalf("SelectImagesGenerationsProvider failed: %v", err)
	}
	if selection.ProviderName != "openai_image" {
		t.Fatalf("unexpected provider: %+v", selection)
	}
	if selection.Model != "gpt-image-1.5" {
		t.Fatalf("unexpected model: %+v", selection)
	}
}

func TestSelectImagesGenerationsProvider_FallsBackToAnySupportedModel(t *testing.T) {
	cfg := &Config{
		Providers: ProvidersConfig{
			Items: map[string]Provider{
				"openai_image": {
					Enabled:      true,
					Type:         "openai",
					BaseURL:      "https://api.openai.com",
					APIKey:       "test-key",
					DefaultModel: "",
					SupportedModels: []string{
						"gpt-image-1.5",
					},
					ModelCapabilities: map[string]ModelCapabilitySpec{
						"gpt-image-1.5": {
							NativeTools: NativeToolCapabilities{ImagesGenerationsAPI: true},
						},
					},
				},
			},
		},
	}

	selection, err := SelectImagesGenerationsProvider(cfg, ImagesGenerationsHint{})
	if err != nil {
		t.Fatalf("SelectImagesGenerationsProvider failed: %v", err)
	}
	if selection.Model != "gpt-image-1.5" {
		t.Fatalf("unexpected fallback model: %+v", selection)
	}
}

func TestSelectImagesGenerationsProvider_ReturnsErrorWhenUnavailable(t *testing.T) {
	cfg := &Config{
		Providers: ProvidersConfig{
			Items: map[string]Provider{
				"openai": {
					Enabled: true,
					Type:    "openai",
					BaseURL: "https://api.openai.com",
					APIKey:  "test-key",
					ModelCapabilities: map[string]ModelCapabilitySpec{
						"gpt-4o": {
							NativeTools: NativeToolCapabilities{ImageGeneration: true},
						},
					},
				},
			},
		},
	}

	if _, err := SelectImagesGenerationsProvider(cfg, ImagesGenerationsHint{}); err == nil {
		t.Fatal("expected error when no images_generations_api capability is configured")
	}
}

func TestSelectAllImagesGenerationsProviders_ReturnsMultipleProviders(t *testing.T) {
	cfg := &Config{
		Providers: ProvidersConfig{
			Items: map[string]Provider{
				"SENSENOVA_IMAGE": {
					Enabled:      true,
					Type:         "openai",
					BaseURL:      "https://token.sensenova.cn",
					APIKey:       "sensenova-key",
					DefaultModel: "sensenova-u1-fast",
					SupportedModels: []string{
						"sensenova-u1-fast",
					},
					ModelCapabilities: map[string]ModelCapabilitySpec{
						"sensenova-u1-fast": {
							NativeTools: NativeToolCapabilities{ImagesGenerationsAPI: true},
						},
					},
				},
				"OPENAI_IMAGE": {
					Enabled:      true,
					Type:         "openai",
					BaseURL:      "https://api.openai.com",
					APIKey:       "openai-key",
					DefaultModel: "gpt-image-2",
					SupportedModels: []string{
						"gpt-image-2",
					},
					ModelCapabilities: map[string]ModelCapabilitySpec{
						"gpt-image-2": {
							NativeTools: NativeToolCapabilities{ImagesGenerationsAPI: true},
						},
					},
				},
			},
		},
	}

	results, err := SelectAllImagesGenerationsProviders(cfg, ImagesGenerationsHint{})
	if err != nil {
		t.Fatalf("SelectAllImagesGenerationsProviders failed: %v", err)
	}
	if len(results) < 2 {
		t.Fatalf("expected at least 2 results, got %d", len(results))
	}

	// Providers should be sorted alphabetically: OPENAI_IMAGE before SENSENOVA_IMAGE
	if results[0].ProviderName != "OPENAI_IMAGE" {
		t.Fatalf("expected first provider OPENAI_IMAGE, got %s", results[0].ProviderName)
	}
	if results[1].ProviderName != "SENSENOVA_IMAGE" {
		t.Fatalf("expected second provider SENSENOVA_IMAGE, got %s", results[1].ProviderName)
	}
}

func TestSelectAllImagesGenerationsProvider_HintByProvider(t *testing.T) {
	cfg := &Config{
		Providers: ProvidersConfig{
			Items: map[string]Provider{
				"SENSENOVA_IMAGE": {
					Enabled:         true,
					BaseURL:         "https://token.sensenova.cn",
					APIKey:          "sensenova-key",
					DefaultModel:    "sensenova-u1-fast",
					SupportedModels: []string{"sensenova-u1-fast"},
					ModelCapabilities: map[string]ModelCapabilitySpec{
						"sensenova-u1-fast": {
							NativeTools: NativeToolCapabilities{ImagesGenerationsAPI: true},
						},
					},
				},
				"OPENAI_IMAGE": {
					Enabled:         true,
					BaseURL:         "https://api.openai.com",
					APIKey:          "openai-key",
					DefaultModel:    "gpt-image-2",
					SupportedModels: []string{"gpt-image-2"},
					ModelCapabilities: map[string]ModelCapabilitySpec{
						"gpt-image-2": {
							NativeTools: NativeToolCapabilities{ImagesGenerationsAPI: true},
						},
					},
				},
			},
		},
	}

	results, err := SelectAllImagesGenerationsProviders(cfg, ImagesGenerationsHint{ProviderName: "SENSENOVA_IMAGE"})
	if err != nil {
		t.Fatalf("SelectAllImagesGenerationsProviders with provider hint failed: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result for provider hint, got %d", len(results))
	}
	if results[0].ProviderName != "SENSENOVA_IMAGE" {
		t.Fatalf("expected SENSENOVA_IMAGE, got %s", results[0].ProviderName)
	}
}

func TestSelectAllImagesGenerationsProvider_HintByModel(t *testing.T) {
	cfg := &Config{
		Providers: ProvidersConfig{
			Items: map[string]Provider{
				"SENSENOVA_IMAGE": {
					Enabled:         true,
					BaseURL:         "https://token.sensenova.cn",
					APIKey:          "sensenova-key",
					DefaultModel:    "sensenova-u1-fast",
					SupportedModels: []string{"sensenova-u1-fast"},
					ModelCapabilities: map[string]ModelCapabilitySpec{
						"sensenova-u1-fast": {
							NativeTools: NativeToolCapabilities{ImagesGenerationsAPI: true},
						},
					},
				},
				"OPENAI_IMAGE": {
					Enabled:         true,
					BaseURL:         "https://api.openai.com",
					APIKey:          "openai-key",
					DefaultModel:    "gpt-image-2",
					SupportedModels: []string{"gpt-image-2"},
					ModelCapabilities: map[string]ModelCapabilitySpec{
						"gpt-image-2": {
							NativeTools: NativeToolCapabilities{ImagesGenerationsAPI: true},
						},
					},
				},
			},
		},
	}

	results, err := SelectAllImagesGenerationsProviders(cfg, ImagesGenerationsHint{Model: "sensenova-u1-fast"})
	if err != nil {
		t.Fatalf("SelectAllImagesGenerationsProviders with model hint failed: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result for model hint, got %d", len(results))
	}
	if results[0].Model != "sensenova-u1-fast" {
		t.Fatalf("expected sensenova-u1-fast, got %s", results[0].Model)
	}
}

func TestSelectAllCodexNativeImageGenerationProviders_SelectsTextImageCodexModel(t *testing.T) {
	cfg := &Config{
		Providers: ProvidersConfig{
			Items: map[string]Provider{
				"CODEX_NATIVE": {
					Enabled:         true,
					Protocol:        "codex",
					DefaultModel:    "gpt-5.4",
					SupportedModels: []string{"gpt-5.4"},
					ModelCapabilities: map[string]ModelCapabilitySpec{
						"gpt-5.4": {
							InputModalities: []string{"text", "image"},
							NativeTools: NativeToolCapabilities{
								ImageGeneration: true,
							},
						},
					},
				},
				"OPENAI_IMAGE": {
					Enabled:      true,
					Protocol:     "openai",
					DefaultModel: "gpt-image-2",
					ModelCapabilities: map[string]ModelCapabilitySpec{
						"gpt-image-2": {
							NativeTools: NativeToolCapabilities{ImagesGenerationsAPI: true},
						},
					},
				},
			},
		},
	}

	results, err := SelectAllCodexNativeImageGenerationProviders(cfg, CodexNativeImageGenerationHint{})
	if err != nil {
		t.Fatalf("SelectAllCodexNativeImageGenerationProviders failed: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 native result, got %d", len(results))
	}
	if results[0].ProviderName != "CODEX_NATIVE" || results[0].Model != "gpt-5.4" {
		t.Fatalf("unexpected native selection: %+v", results[0])
	}
}

func TestSelectAllCodexNativeImageGenerationProviders_RejectsTextOnlyModel(t *testing.T) {
	cfg := &Config{
		Providers: ProvidersConfig{
			Items: map[string]Provider{
				"CODEX_TEXT": {
					Enabled:      true,
					Protocol:     "codex",
					DefaultModel: "gpt-5.4",
					ModelCapabilities: map[string]ModelCapabilitySpec{
						"gpt-5.4": {
							InputModalities: []string{"text"},
							NativeTools: NativeToolCapabilities{
								ImageGeneration: true,
							},
						},
					},
				},
			},
		},
	}

	if _, err := SelectAllCodexNativeImageGenerationProviders(cfg, CodexNativeImageGenerationHint{}); err == nil {
		t.Fatal("expected error for text-only native image model")
	}
}
