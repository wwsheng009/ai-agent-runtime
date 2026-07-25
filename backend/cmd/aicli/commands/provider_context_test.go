package commands

import (
	"strings"
	"testing"

	config "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	"github.com/wwsheng009/ai-agent-runtime/internal/buildinfo"
	"github.com/wwsheng009/ai-agent-runtime/internal/llm/adapter"
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

func TestEffectiveChatProviderHeaders_UserAgentOverridesDefaultInBuildHeaders(t *testing.T) {
	cfg := &config.Config{
		Providers: config.ProvidersConfig{
			Headers: map[string]string{
				"User-Agent": "global-config-ua/1.0",
			},
			Items: map[string]config.Provider{
				"alpha": {
					Enabled:  true,
					Protocol: "openai",
					Headers: map[string]string{
						"user-agent": "provider-config-ua/2.0",
					},
				},
			},
		},
	}

	session := &ChatSession{
		Config:   cfg,
		Provider: cfg.Providers.Items["alpha"],
	}
	effective := effectiveChatProviderHeaders(session)
	if got := effective["User-Agent"]; got != "provider-config-ua/2.0" {
		// EffectiveProviderHeaders canonicalizes keys, so either form is fine.
		if got == "" {
			for k, v := range effective {
				if strings.EqualFold(k, "User-Agent") {
					got = v
					break
				}
			}
		}
		if got != "provider-config-ua/2.0" {
			t.Fatalf("effective User-Agent = %q, want provider-config-ua/2.0 (headers=%+v)", got, effective)
		}
	}

	built := adapter.GetAdapterOrDefault("openai").BuildHeaders(adapter.AdapterConfig{
		APIKey:  "k",
		Headers: effective,
	})
	got := ""
	for k, v := range built {
		if strings.EqualFold(k, "User-Agent") {
			got = v
			break
		}
	}
	if got != "provider-config-ua/2.0" {
		t.Fatalf("BuildHeaders User-Agent = %q, want provider-config-ua/2.0 (default would be %q)", got, buildinfo.UserAgent())
	}
}

func TestEffectiveChatProviderHeaders_MissingUserAgentFallsBackToDefault(t *testing.T) {
	cfg := &config.Config{
		Providers: config.ProvidersConfig{
			Headers: map[string]string{
				"X-Global": "only-this",
			},
			Items: map[string]config.Provider{
				"alpha": {
					Enabled:  true,
					Protocol: "openai",
				},
			},
		},
	}
	session := &ChatSession{
		Config:   cfg,
		Provider: cfg.Providers.Items["alpha"],
	}
	built := adapter.GetAdapterOrDefault("openai").BuildHeaders(adapter.AdapterConfig{
		APIKey:  "k",
		Headers: effectiveChatProviderHeaders(session),
	})
	got := ""
	for k, v := range built {
		if strings.EqualFold(k, "User-Agent") {
			got = v
			break
		}
	}
	if got != buildinfo.UserAgent() {
		t.Fatalf("User-Agent = %q, want default %q", got, buildinfo.UserAgent())
	}
}
