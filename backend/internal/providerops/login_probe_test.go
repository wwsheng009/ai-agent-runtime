package providerops

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	config "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	"github.com/wwsheng009/ai-agent-runtime/internal/modelcard"
)

func loginProbeTestCatalog(t *testing.T) *modelcard.Catalog {
	t.Helper()
	catalog, warnings, err := modelcard.LoadSources([]modelcard.Source{modelcard.BuiltinSource()}, true)
	if err != nil || len(warnings) != 0 {
		t.Fatalf("builtin: err=%v warnings=%+v", err, warnings)
	}
	return catalog
}

func TestLoginProbeDiscoversResponsesWithoutProtocolMetadata(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var body map[string]interface{}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if r.Method != http.MethodPost || body["model"] != "gpt-6.1-sol" {
			t.Errorf("unexpected probe: %s %s body=%v", r.Method, r.URL.Path, body)
		}
		if r.Header.Get("X-Tenant") != "test" {
			t.Error("global provider headers not propagated")
		}
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/v1/responses" {
			if body["stream"] != false || body["max_output_tokens"] != float64(16) {
				t.Errorf("invalid responses probe: %v", body)
			}
			_, _ = w.Write([]byte(`{"object":"response","id":"resp_test","output":[]}`))
			return
		}
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"message":"Model is not supported for this format"}}`))
	}))
	defer server.Close()
	cfg := &config.Config{}
	cfg.Providers.Headers = map[string]string{"X-Tenant": "test"}
	catalog := loginProbeTestCatalog(t)
	before := len(catalog.Cards)
	provider := config.Provider{BaseURL: server.URL, Protocol: "openai", APIKey: "sk-test"}
	result, err := ProbeUnclassifiedLoginModels(context.Background(), LoginProbeRequest{
		Config: cfg, ProviderName: "test", Provider: provider, LoginProtocol: "openai",
		Catalog: catalog, Models: []ModelInfo{{ID: "gpt-6.1-sol"}, {ID: "gpt-5.6-sol"}, {ID: "custom-image-model"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 3 || len(result.Probes) != 1 || len(result.Warnings) != 0 || len(result.Models) != 3 {
		t.Fatalf("unexpected discovery: calls=%d result=%+v", calls.Load(), result)
	}
	if len(catalog.Cards) != before {
		t.Fatal("input catalog was mutated")
	}
	ctx := modelcard.Context{RuntimeProtocol: "openai", ProviderTemplate: "openai.chat"}
	recs := result.Catalog.RecommendedProviderTemplates(ctx, "gpt-6.1-sol")
	if len(recs) != 1 || recs[0].Template.ID != "codex.responses" || recs[0].Applied[0].Fallback {
		t.Fatalf("expected proven codex mapping: %+v", recs)
	}
	spec, _ := result.Catalog.Resolve(modelcard.Context{RuntimeProtocol: "codex"}, "gpt-6.1-sol")
	if spec.MaxContextTokens != 1000000 || spec.DefaultReasoningEffort != "high" {
		t.Fatalf("probe must not replace capability metadata: %+v", spec)
	}
	// Gateway namespaces can route different models: one positive probe must
	// not become fuzzy evidence for an unprobed alias.
	recs = result.Catalog.RecommendedProviderTemplates(ctx, "other/gpt-6.1-sol")
	if len(recs) != 1 || !recs[0].Applied[0].Fallback {
		t.Fatalf("probe leaked to an untested alias: %+v", recs)
	}
}

func TestLoginProbeVerdicts(t *testing.T) {
	for _, tc := range []struct {
		name        string
		status      int
		multi       bool
		wantModels  int
		wantGroups  int
		wantWarning bool
	}{
		{"multiple supported protocols", 200, true, 1, 2, false},
		{"authentication inconclusive", 401, false, 1, 1, true},
		{"rate limit inconclusive", 429, false, 1, 1, true},
		{"HTML endpoint inconclusive", 404, false, 1, 1, true},
		{"explicit rejection never remains supported", 400, false, 0, 0, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if tc.multi {
					switch r.URL.Path {
					case "/v1/responses":
						_, _ = w.Write([]byte(`{"object":"response","id":"resp_test","output":[]}`))
					case "/v1/chat/completions":
						_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"hi"}}]}`))
					default:
						w.WriteHeader(404)
					}
					return
				}
				w.WriteHeader(tc.status)
				if tc.status == 400 {
					_, _ = w.Write([]byte(`{"error":{"message":"model not found"}}`))
				} else if tc.status == 401 {
					_, _ = w.Write([]byte(`{"error":{"message":"invalid api key"}}`))
				}
			}))
			defer server.Close()
			provider := config.Provider{BaseURL: server.URL, Protocol: "openai", APIKey: "sk-test"}
			result, err := ProbeUnclassifiedLoginModels(context.Background(), LoginProbeRequest{
				Provider: provider, LoginProtocol: "openai", Catalog: loginProbeTestCatalog(t),
				Models: []ModelInfo{{ID: "gpt-6.1-sol"}},
			})
			if err != nil {
				t.Fatal(err)
			}
			if len(result.Models) != tc.wantModels || (len(result.Warnings) > 0) != tc.wantWarning {
				t.Fatalf("unexpected result: %+v", result)
			}
			groups := GroupProviderLoginModelsByProviderTemplate("test", "openai", "auto", provider, result.Models, result.Catalog, "apikey")
			if len(groups) != tc.wantGroups {
				t.Fatalf("unexpected groups: %+v", groups)
			}
		})
	}
}

func TestLoginProbeLimitAndPreferredModel(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer server.Close()
	models := make([]ModelInfo, 0, loginProbeMaxModels+2)
	for i := 0; i < loginProbeMaxModels+2; i++ {
		models = append(models, ModelInfo{ID: fmt.Sprintf("unmapped-%d", i)})
	}
	preferred := models[len(models)-1].ID
	result, err := ProbeUnclassifiedLoginModels(context.Background(), LoginProbeRequest{
		Provider: config.Provider{BaseURL: server.URL, APIKey: "sk-test"}, LoginProtocol: "openai",
		Models: models, Catalog: loginProbeTestCatalog(t), DefaultModel: preferred,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Probes) != loginProbeMaxModels || result.Probes[0].Model != preferred || calls.Load() != int32(loginProbeMaxModels*3) {
		t.Fatalf("probe cap/default priority: calls=%d probes=%+v", calls.Load(), result.Probes)
	}
	if len(result.Models) != len(models) || len(result.Warnings) != loginProbeMaxModels+1 || !strings.Contains(result.Warnings[0], "未验证") {
		t.Fatalf("inconclusive/untested models must remain explicit: %+v", result)
	}
}

func TestLoginProbeBudgetAndCancellation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(time.Second):
		}
	}))
	defer server.Close()
	req := LoginProbeRequest{
		Provider: config.Provider{BaseURL: server.URL, APIKey: "sk-test"}, LoginProtocol: "openai",
		Models: []ModelInfo{{ID: "gpt-6.1-sol"}}, Catalog: loginProbeTestCatalog(t), Timeout: 30 * time.Millisecond,
	}
	start := time.Now()
	result, err := ProbeUnclassifiedLoginModels(context.Background(), req)
	if err != nil || len(result.Models) != 1 || len(result.Warnings) != 1 || time.Since(start) > 2*time.Second {
		t.Fatalf("discovery budget: result=%+v err=%v elapsed=%s", result, err, time.Since(start))
	}
	for _, probe := range result.Probes[0].Probes {
		if probe.Verdict != string(probeVerdictUnknown) {
			t.Fatalf("timeout is not protocol rejection: %+v", probe)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := ProbeUnclassifiedLoginModels(ctx, req); !errors.Is(err, context.Canceled) {
		t.Fatalf("caller cancellation must abort login: %v", err)
	}
}

func TestLoginProbeWithCapabilitiesDisabled(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/responses" {
			_, _ = w.Write([]byte(`{"object":"response","output":[]}`))
		} else {
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	result, err := ProbeUnclassifiedLoginModels(nil, LoginProbeRequest{
		Provider: config.Provider{BaseURL: server.URL, APIKey: "sk-test"}, LoginProtocol: "openai",
		Models: []ModelInfo{{ID: "gpt-6.1-sol"}},
	})
	if err != nil || len(result.Probes) != 1 {
		t.Fatalf("protocol discovery without capability cards: result=%+v err=%v", result, err)
	}
	spec, _ := result.Catalog.Resolve(modelcard.Context{RuntimeProtocol: "codex"}, "gpt-6.1-sol")
	if spec.MaxContextTokens != 0 || spec.ReasoningModel {
		t.Fatalf("builtin capability cards silently re-enabled: %+v", spec)
	}
}

func TestLoginProbeSkipsKnownAndImageModels(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()
	catalog := loginProbeTestCatalog(t)
	result, err := ProbeUnclassifiedLoginModels(nil, LoginProbeRequest{
		Provider: config.Provider{BaseURL: server.URL}, LoginProtocol: "openai", Catalog: catalog,
		Models: []ModelInfo{{ID: "gpt-5.6-sol"}, {ID: "gpt-6-astra"}, {ID: "gpt-image-2"}, {ID: "custom-image-model"}},
	})
	if err != nil || calls.Load() != 0 || result.Catalog != catalog || len(result.Probes) != 0 {
		t.Fatalf("known/image models must not be probed: result=%+v err=%v calls=%d", result, err, calls.Load())
	}
}

func TestResponsesProbeRequiresProtocolEvidence(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		want       providerModelProbeVerdict
	}{
		{"id alone", `{"id":"gateway-123"}`, probeVerdictUnknown},
		{"chat response", `{"id":"chatcmpl-1","object":"chat.completion","choices":[{}]}`, probeVerdictUnknown},
		{"message response", `{"id":"msg-1","type":"message","content":[]}`, probeVerdictUnknown},
		{"failed response", `{"object":"response","id":"resp-1","status":"failed","output":[]}`, probeVerdictUnknown},
		{"queued response", `{"object":"response","id":"resp-1","status":"queued"}`, probeVerdictUnknown},
		{"responses output", `{"object":"response","id":"resp-1","output":[]}`, probeVerdictSupported},
		{"compatible output", `{"output":[]}`, probeVerdictSupported},
		{"budget exhausted", `{"object":"response","status":"incomplete","incomplete_details":{"reason":"max_output_tokens"},"output":[]}`, probeVerdictSupported},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, detail := classifyProviderModelProbeResponse(http.StatusOK, []byte(tc.body), "codex")
			if got != tc.want {
				t.Fatalf("got %s want %s: %s", got, tc.want, detail)
			}
		})
	}
}
