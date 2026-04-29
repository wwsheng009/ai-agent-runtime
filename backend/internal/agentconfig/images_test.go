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
