package commands

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	config "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
)

func autoProbeLoginTestRequest(t *testing.T, baseURL string) providerLoginRequest {
	t.Helper()
	dir := t.TempDir()
	setTestUserProfileDir(t, dir)
	cfg := &config.Config{
		ConfigFilePath: filepath.Join(dir, "config.yaml"),
		AICLI: &config.AICLIConfig{ModelCards: &config.AICLIModelCardsConfig{
			UserPath:      filepath.Join(dir, "model_cards.yaml"),
			WorkspacePath: filepath.Join(dir, "workspace_cards.yaml"),
		}},
	}
	return providerLoginRequest{
		Context: context.Background(), Config: cfg, ProviderName: "sol", LoginProtocol: "auto",
		BaseURL: baseURL, APIKey: "sk-test", AuthStorePath: filepath.Join(dir, "auth.json"),
		DryRun: true, SkipSiteDetect: true, SkipAccount: true, ModelCardsStrict: true,
	}
}

func TestRunProviderLogin_AutoProbesResponsesOnlyModel(t *testing.T) {
	for _, dryRun := range []bool{true, false} {
		name := "persist"
		if dryRun {
			name = "dry_run"
		}
		t.Run(name, func(t *testing.T) {
			var posts atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if r.Method == http.MethodGet && r.URL.Path == "/v1/models" {
					_, _ = w.Write([]byte(`{"data":[{"id":"gpt-6.1-sol"}]}`))
					return
				}
				posts.Add(1)
				if r.Method != http.MethodPost {
					t.Errorf("unexpected method: %s", r.Method)
				}
				var body map[string]interface{}
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body["model"] != "gpt-6.1-sol" {
					t.Errorf("unexpected model/body: %v err=%v", body, err)
				}
				if r.URL.Path == "/v1/responses" {
					_, _ = w.Write([]byte(`{"object":"response","id":"resp_test","output":[]}`))
				} else {
					w.WriteHeader(http.StatusBadRequest)
					_, _ = w.Write([]byte(`{"error":{"message":"model is not supported for this format"}}`))
				}
			}))
			defer server.Close()
			req := autoProbeLoginTestRequest(t, server.URL)
			req.DryRun = dryRun
			if !dryRun {
				if err := os.WriteFile(req.Config.ConfigFilePath, []byte("providers:\n  items: {}\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			result, err := runProviderLogin(req)
			if err != nil {
				t.Fatal(err)
			}
			if result.Protocol != "codex" || result.LoginProtocol != "codex-apikey" || result.DefaultModel != "gpt-6.1-sol" {
				t.Fatalf("expected responses classification: %+v", result)
			}
			if posts.Load() != 3 || len(result.ModelProbes) != 1 || len(result.ModelProbeWarnings) != 0 {
				t.Fatalf("expected protocol evidence: calls=%d result=%+v", posts.Load(), result)
			}
			paths := []string{req.Config.AICLI.ModelCards.UserPath, req.Config.AICLI.ModelCards.WorkspacePath}
			if dryRun {
				paths = append(paths, req.AuthStorePath, req.Config.ConfigFilePath)
			} else {
				provider := req.Config.Providers.Items["sol"]
				if provider.Protocol != "codex" || provider.APIPath != "/v1/responses" || provider.ForwardURL != "/v1/responses" {
					t.Fatalf("wrong persisted protocol: %+v", provider)
				}
				if provider.ModelCapabilities["gpt-6.1-sol"].MaxContextTokens != 1000000 {
					t.Fatal("capability metadata lost after protocol discovery")
				}
				raw, err := os.ReadFile(req.Config.ConfigFilePath)
				if err != nil || strings.Contains(string(raw), "sk-test") {
					t.Fatalf("config must persist a key ref, not a secret: %v", err)
				}
			}
			for _, path := range paths {
				if _, err := os.Stat(path); !os.IsNotExist(err) {
					t.Fatalf("unexpected write to %s: %v", path, err)
				}
			}
		})
	}
}

func TestRunProviderLogin_ProbeOptOutAndKnownModels(t *testing.T) {
	for _, tc := range []struct {
		name, protocol, model, wantProtocol string
		skip                                bool
	}{
		{"explicit protocol", "openai", "gpt-6.1-sol", "openai", false},
		{"opt out", "auto", "gpt-6.1-sol", "openai", true},
		{"known card", "auto", "gpt-5.6-sol", "codex", false},
		{"image heuristic", "auto", "custom-image-model", "openai_image", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var posts atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodPost {
					posts.Add(1)
				}
				_ = json.NewEncoder(w).Encode(map[string]interface{}{"data": []map[string]string{{"id": tc.model}}})
			}))
			defer server.Close()
			req := autoProbeLoginTestRequest(t, server.URL)
			req.LoginProtocol, req.SkipModelProbe = tc.protocol, tc.skip
			result, err := runProviderLogin(req)
			if err != nil {
				t.Fatal(err)
			}
			if posts.Load() != 0 || len(result.ModelProbes) != 0 || result.Protocol != tc.wantProtocol {
				t.Fatalf("unexpected probe/classification: calls=%d result=%+v", posts.Load(), result)
			}
		})
	}
}

func TestRunProviderLogin_ProbeUnknownAndRejection(t *testing.T) {
	for _, status := range []int{http.StatusUnauthorized, http.StatusBadRequest} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodGet {
					_, _ = w.Write([]byte(`{"data":[{"id":"gpt-6.1-sol"}]}`))
					return
				}
				w.WriteHeader(status)
				if status == http.StatusUnauthorized {
					_, _ = w.Write([]byte(`{"error":{"message":"invalid api key"}}`))
				} else {
					_, _ = w.Write([]byte(`{"error":{"message":"model not found"}}`))
				}
			}))
			defer server.Close()
			req := autoProbeLoginTestRequest(t, server.URL)
			result, err := runProviderLogin(req)
			if status == http.StatusBadRequest {
				if err == nil || !strings.Contains(err.Error(), "no models remain after auto protocol probes") {
					t.Fatalf("explicit rejection must not persist an unusable fallback: result=%+v err=%v", result, err)
				}
			} else {
				if err != nil || result.Protocol != "openai" || len(result.ModelProbeWarnings) != 1 {
					t.Fatalf("unknown should retain fallback with warning: result=%+v err=%v", result, err)
				}
				encoded, err := json.Marshal(result)
				if err != nil || !strings.Contains(string(encoded), `"model_probes"`) || !strings.Contains(string(encoded), `"model_probe_warnings"`) {
					t.Fatalf("probe evidence missing from JSON: %s err=%v", encoded, err)
				}
			}
			for _, path := range []string{req.Config.ConfigFilePath, req.AuthStorePath, req.Config.AICLI.ModelCards.UserPath} {
				if _, err := os.Stat(path); !os.IsNotExist(err) {
					t.Fatalf("unexpected write: %s %v", path, err)
				}
			}
		})
	}
}

func TestLoginSkipModelProbeFlag(t *testing.T) {
	cmd := NewLoginCommand(nil)
	flag := cmd.Flags().Lookup("skip-model-probe")
	if flag == nil || flag.DefValue != "false" {
		t.Fatalf("skip-model-probe must be an explicit opt-out: %+v", flag)
	}
	parsed, err := parseChatLoginCommandRequest("/login --protocol auto --skip-model-probe --dry-run")
	if err != nil || !parsed.SkipModelProbe || !parsed.DryRun || parsed.Protocol != "auto" {
		t.Fatalf("chat flag parity: %+v err=%v", parsed, err)
	}
}

func TestRunProviderLogin_CancelledDoesNotProbeOrWrite(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
	}))
	defer server.Close()
	req := autoProbeLoginTestRequest(t, server.URL)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	req.Context, req.DryRun = ctx, false
	if _, err := runProviderLogin(req); err != context.Canceled {
		t.Fatalf("expected cancellation: %v", err)
	}
	if calls.Load() != 0 {
		t.Fatalf("cancelled login made %d requests", calls.Load())
	}
	for _, path := range []string{req.Config.ConfigFilePath, req.AuthStorePath} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("cancelled login wrote %s: %v", path, err)
		}
	}
}

func TestRunProviderLogin_AutoProbePreservesSavedCustomResponsesPath(t *testing.T) {
	var responses, chat atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/v1/models" {
			_, _ = w.Write([]byte(`{"data":[{"id":"gpt-6.1-sol"}]}`))
			return
		}
		var body map[string]interface{}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if r.URL.Path == "/custom/responses" {
			responses.Add(1)
			if body["input"] == nil || body["messages"] != nil {
				t.Errorf("custom Responses path received wrong protocol: %+v", body)
			}
			_, _ = w.Write([]byte(`{"object":"response","output":[]}`))
			return
		}
		if r.URL.Path == "/v1/chat/completions" {
			chat.Add(1)
		}
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"message":"model is not supported for this format"}}`))
	}))
	defer server.Close()
	req := autoProbeLoginTestRequest(t, server.URL)
	req.Config.Providers.Items = map[string]config.Provider{
		"sol": {Protocol: "codex", BaseURL: server.URL, APIPath: "/custom/responses", APIKey: "sk-test"},
	}
	result, err := runProviderLogin(req)
	if err != nil || result.Protocol != "codex" || responses.Load() != 1 || chat.Load() != 1 {
		t.Fatalf("saved path should be probed using codex only: result=%+v err=%v responses=%d chat=%d", result, err, responses.Load(), chat.Load())
	}
}

func TestRunProviderLogin_AutoProbeCreatesMultipleProtocolGroups(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/models":
			_, _ = w.Write([]byte(`{"data":[{"id":"gpt-6.1-sol"}]}`))
		case "/v1/responses":
			_, _ = w.Write([]byte(`{"object":"response","output":[]}`))
		case "/v1/chat/completions":
			_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"hi"}}]}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	result, err := runProviderLogin(autoProbeLoginTestRequest(t, server.URL))
	if err != nil || len(result.ProviderConfigs) != 2 {
		t.Fatalf("multiple protocols should produce two groups: result=%+v err=%v", result, err)
	}
	protocols := map[string]bool{}
	for _, provider := range result.ProviderConfigs {
		protocols[provider.Protocol] = true
		if len(provider.SupportedModels) != 1 || provider.SupportedModels[0] != "gpt-6.1-sol" {
			t.Fatalf("wrong grouped models: %+v", provider)
		}
	}
	if !protocols["openai"] || !protocols["codex"] {
		t.Fatalf("wrong protocols: %v", protocols)
	}
}

func TestRunProviderLogin_AutoProbeHonorsDefaultOutsideLargestGroup(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			_, _ = w.Write([]byte(`{"data":[{"id":"gpt-6.1-sol"},{"id":"claude-sonnet-4-6"},{"id":"claude-opus-4-6"}]}`))
			return
		}
		if r.URL.Path == "/v1/responses" {
			_, _ = w.Write([]byte(`{"object":"response","output":[]}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()
	req := autoProbeLoginTestRequest(t, server.URL)
	req.DefaultModel = "gpt-6.1-sol"
	result, err := runProviderLogin(req)
	if err != nil || result.Protocol != "codex" || result.DefaultModel != req.DefaultModel || len(result.ProviderConfigs) != 2 {
		t.Fatalf("explicit default should select its discovered group: result=%+v err=%v", result, err)
	}
}

func TestRunProviderLogin_CancelDuringProbeDoesNotWrite(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			_, _ = w.Write([]byte(`{"data":[{"id":"gpt-6.1-sol"}]}`))
			return
		}
		cancel()
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer server.Close()
	req := autoProbeLoginTestRequest(t, server.URL)
	req.Context, req.DryRun = ctx, false
	if _, err := runProviderLogin(req); err == nil || !strings.Contains(err.Error(), context.Canceled.Error()) {
		t.Fatalf("cancel during probes should abort login: %v", err)
	}
	for _, path := range []string{req.Config.ConfigFilePath, req.AuthStorePath, req.Config.AICLI.ModelCards.UserPath} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("cancelled login wrote %s: %v", path, err)
		}
	}
}

func TestValidateProviderLoginModelsAutoSharesOneTimeoutAcrossAttempts(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		select {
		case <-r.Context().Done():
			return
		case <-time.After(200 * time.Millisecond):
			w.WriteHeader(http.StatusUnauthorized)
		}
	}))
	defer server.Close()

	_, _, _, err := validateProviderLoginModels(
		context.Background(),
		nil,
		"slow",
		config.Provider{BaseURL: server.URL, APIKey: "sk-test"},
		"auto",
		"",
		350*time.Millisecond,
	)
	if err == nil {
		t.Fatal("expected the shared auto-validation deadline to expire")
	}
	if calls.Load() > 2 {
		t.Fatalf("auto validation made %d requests with a 350ms total timeout; want at most 2", calls.Load())
	}
}
