package commands

import (
	"net/http"
	"net/http/httptest"
	"testing"

	config "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
)

func TestParseProviderModelsResponse_OpenAICompatible(t *testing.T) {
	models, err := parseProviderModelsResponse([]byte(`{"object":"list","data":[{"id":"gpt-4.1"},{"id":"gpt-5"}]}`), "openai")
	if err != nil {
		t.Fatalf("parseProviderModelsResponse: %v", err)
	}
	ids := providerModelIDs(models)
	if len(ids) != 2 || ids[0] != "gpt-4.1" || ids[1] != "gpt-5" {
		t.Fatalf("unexpected ids: %v", ids)
	}
}

func TestProviderModelsAPIKey_PrefersInlineKeyOverAuthRef(t *testing.T) {
	// login 交互中输入的新 key 写入内联 APIKey 字段，而 api_key_ref 仍指向
	// auth store 中的旧凭据；校验必须用新输入值，否则会以旧 key 请求返回 401。
	ghostRef := "models-api-key-test-no-such-ref"
	got := providerModelsAPIKey(config.Provider{
		APIKey:    "  agw_live_new_secret  ",
		APIKeyRef: ghostRef,
	})
	if got != "agw_live_new_secret" {
		t.Fatalf("expected inline APIKey to win over auth ref, got %q", got)
	}
}

func TestProviderModelsAPIKey_FallsBackToGetAPIKeyWhenInlineEmpty(t *testing.T) {
	// 非 login 路径 APIKey 字段为空：保持 GetAPIKey 语义（ref 不存在 → 空，
	// 不 panic，也不 fallback 到不存在的 auth store 值）。
	ghostRef := "models-api-key-test-no-such-ref"
	if got := providerModelsAPIKey(config.Provider{APIKeyRef: ghostRef}); got != "" {
		t.Fatalf("expected empty key for missing auth ref, got %q", got)
	}
}

func TestParseProviderModelsResponse_GeminiStripsModelsPrefix(t *testing.T) {
	models, err := parseProviderModelsResponse([]byte(`{"models":[{"name":"models/gemini-2.5-pro"},{"name":"models/gemini-2.5-flash"}]}`), "gemini")
	if err != nil {
		t.Fatalf("parseProviderModelsResponse: %v", err)
	}
	ids := providerModelIDs(models)
	if len(ids) != 2 || ids[0] != "gemini-2.5-pro" || ids[1] != "gemini-2.5-flash" {
		t.Fatalf("unexpected ids: %v", ids)
	}
}

func TestParseProviderModelsResponse_CodexSlugAndCapabilities(t *testing.T) {
	models, err := parseProviderModelsResponse([]byte(`{"models":[{"slug":"gpt-5.4","reasoningEfforts":["low","medium"],"inputModalities":["text","image"],"maxContextTokens":256000}]}`), "codex-apikey")
	if err != nil {
		t.Fatalf("parseProviderModelsResponse: %v", err)
	}
	if len(models) != 1 || models[0].ID != "gpt-5.4" {
		t.Fatalf("unexpected models: %+v", models)
	}
	if models[0].MaxContextTokens != 256000 || len(models[0].ReasoningEfforts) != 2 || len(models[0].InputModalities) != 2 {
		t.Fatalf("expected codex metadata, got %+v", models[0])
	}
}

func TestParseProviderModelsResponse_UsesThinkingEffortsAlias(t *testing.T) {
	models, err := parseProviderModelsResponse([]byte(`{"models":[{"slug":"gpt-5.4","thinkingEfforts":["low","medium"],"supported_thinking_efforts":["HIGH"]}]}`), "codex-apikey")
	if err != nil {
		t.Fatalf("parseProviderModelsResponse: %v", err)
	}
	if len(models) != 1 || models[0].ID != "gpt-5.4" {
		t.Fatalf("unexpected models: %+v", models)
	}
	if got := models[0].ReasoningEfforts; len(got) != 3 || got[0] != "low" || got[1] != "medium" || got[2] != "HIGH" {
		t.Fatalf("unexpected efforts: %+v", got)
	}
}

func TestValidateProviderModels_SendsExpectedHeadersAndParsesModels(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer sk-test" {
			t.Fatalf("unexpected auth header: %q", got)
		}
		if got := r.Header.Get("X-Global"); got != "global-value" {
			t.Fatalf("unexpected global header: %q", got)
		}
		if got := r.Header.Get("X-Shared"); got != "provider-value" {
			t.Fatalf("unexpected shared header: %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"gpt-4.1-mini"}]}`))
	}))
	defer server.Close()

	result, err := validateProviderModels(providerModelsValidationRequest{
		Config: &config.Config{Providers: config.ProvidersConfig{Headers: map[string]string{
			"X-Global": "global-value",
			"X-Shared": "global-value",
		}}},
		Provider: config.Provider{
			Protocol: "openai",
			BaseURL:  server.URL,
			APIKey:   "sk-test",
			Headers: map[string]string{
				"x-shared": "provider-value",
			},
		},
		LoginProtocol: "openai",
	})
	if err != nil {
		t.Fatalf("validateProviderModels: %v", err)
	}
	if len(result.Models) != 1 || result.Models[0].ID != "gpt-4.1-mini" {
		t.Fatalf("unexpected result: %+v", result)
	}
}

func TestValidateProviderModels_BaseURLWithV1DoesNotDuplicateModelsPath(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"gpt-5-codex"}]}`))
	}))
	defer server.Close()

	result, err := validateProviderModels(providerModelsValidationRequest{
		Provider: config.Provider{
			Protocol: "codex",
			BaseURL:  server.URL + "/v1",
			APIKey:   "sk-test",
		},
		LoginProtocol: "codex-apikey",
	})
	if err != nil {
		t.Fatalf("validateProviderModels: %v", err)
	}
	wantEndpoint := server.URL + "/v1/models"
	if result.Endpoint != wantEndpoint {
		t.Fatalf("endpoint = %q, want %q", result.Endpoint, wantEndpoint)
	}
	if len(result.Models) != 1 || result.Models[0].ID != "gpt-5-codex" {
		t.Fatalf("unexpected result: %+v", result)
	}
}

func TestValidateProviderModels_FailsOnEmptyModels(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":[]}`))
	}))
	defer server.Close()

	_, err := validateProviderModels(providerModelsValidationRequest{
		Provider: config.Provider{
			Protocol: "openai",
			BaseURL:  server.URL,
			APIKey:   "sk-test",
		},
		LoginProtocol: "openai",
	})
	if err == nil {
		t.Fatal("expected empty models error")
	}
}
