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

func TestValidateProviderModels_SendsExpectedHeadersAndParsesModels(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer sk-test" {
			t.Fatalf("unexpected auth header: %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"gpt-4.1-mini"}]}`))
	}))
	defer server.Close()

	result, err := validateProviderModels(providerModelsValidationRequest{
		Provider: config.Provider{
			Protocol: "openai",
			BaseURL:  server.URL,
			APIKey:   "sk-test",
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
