package commands

import (
	"testing"

	config "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
)

func TestResolveProviderExecutionContext(t *testing.T) {
	cfg := &config.Config{
		Providers: config.ProvidersConfig{
			DefaultProvider: "alpha",
			Headers: map[string]string{
				"X-Global": "global-value",
				"X-Shared": "global-value",
			},
			Items: map[string]config.Provider{
				"alpha": {
					Enabled:      true,
					Protocol:     "openai",
					DefaultModel: "gpt-4.1",
					ModelMappings: map[string]string{
						"gpt-5-mini": "gpt-5.2-mini",
					},
					Headers: map[string]string{
						"x-shared": "provider-value",
					},
				},
				"beta": {
					Enabled:      false,
					Protocol:     "codex",
					DefaultModel: "gpt-5",
				},
			},
		},
	}

	resolved, details, err := resolveProviderExecutionContext(cfg, "", "")
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if resolved.ProviderName != "alpha" || resolved.Model != "gpt-4.1" || resolved.Adapter == nil {
		t.Fatalf("unexpected resolved context: %+v", resolved)
	}
	if details["provider"] != "alpha" || details["model"] != "gpt-4.1" {
		t.Fatalf("unexpected details: %+v", details)
	}
	if resolved.Provider.Headers["X-Global"] != "global-value" || resolved.Provider.Headers["X-Shared"] != "provider-value" {
		t.Fatalf("unexpected effective headers: %+v", resolved.Provider.Headers)
	}
	chatHeaders := adapterAdapterConfig(&ChatSession{Config: cfg, Provider: cfg.Providers.Items["alpha"]}).Headers
	if chatHeaders["X-Global"] != "global-value" || chatHeaders["X-Shared"] != "provider-value" {
		t.Fatalf("unexpected chat request headers: %+v", chatHeaders)
	}

	resolved, details, err = resolveProviderExecutionContext(cfg, "alpha", "gpt-5-mini")
	if err != nil {
		t.Fatalf("expected mapped model to resolve, got %v", err)
	}
	if resolved.Model != "gpt-5.2-mini" || !resolved.ModelMapped || resolved.RequestedModel != "gpt-5-mini" {
		t.Fatalf("unexpected mapped context: %+v", resolved)
	}
	if details["mapped_model"] != "gpt-5.2-mini" {
		t.Fatalf("unexpected mapping details: %+v", details)
	}

	_, details, err = resolveProviderExecutionContext(cfg, "missing", "")
	if err == nil {
		t.Fatal("expected missing provider error")
	}
	if details["provider"] != "missing" {
		t.Fatalf("unexpected missing provider details: %+v", details)
	}

	_, _, err = resolveProviderExecutionContext(cfg, "beta", "")
	if err == nil {
		t.Fatal("expected disabled provider error")
	}
}
