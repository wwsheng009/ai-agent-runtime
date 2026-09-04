package agentconfig

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeProviderProxyTestConfig(t *testing.T, raw string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(strings.TrimSpace(raw)+"\n"), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return path
}

func readProviderProxyConfig(t *testing.T, path string) *Config {
	t.Helper()
	cfg, err := InitGlobalConfig(path)
	if err != nil {
		t.Fatalf("InitGlobalConfig: %v", err)
	}
	return cfg
}

func TestSetProviderProxyConfig_AddsNewProxy(t *testing.T) {
	path := writeProviderProxyTestConfig(t, `
providers:
  default_provider: alpha
  items:
    alpha:
      enabled: true
      protocol: openai
      base_url: https://alpha.example.com
`)

	httpURL := "http://127.0.0.1:7890"
	result, err := SetProviderProxyConfig(path, "alpha", ProviderProxyUpdate{HTTP: &httpURL})
	if err != nil {
		t.Fatalf("SetProviderProxyConfig: %v", err)
	}
	if result.Removed || result.Name != "alpha" {
		t.Fatalf("unexpected result: %+v", result)
	}
	if result.Proxy == nil || result.Proxy.HTTP != httpURL || !result.Proxy.Enabled {
		t.Fatalf("unexpected proxy: %+v", result.Proxy)
	}

	cfg := readProviderProxyConfig(t, path)
	got := cfg.Providers.Items["alpha"].Proxy
	if got == nil || got.HTTP != httpURL || !got.Enabled {
		t.Fatalf("proxy not persisted: %+v", got)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	if !strings.Contains(string(raw), "proxy:") || !strings.Contains(string(raw), httpURL) {
		t.Fatalf("proxy missing in yaml:\n%s", raw)
	}
}

func TestSetProviderProxyConfig_MergesAndDisables(t *testing.T) {
	path := writeProviderProxyTestConfig(t, `
providers:
  items:
    alpha:
      enabled: true
      protocol: openai
      proxy:
        http: http://127.0.0.1:7890
        enabled: true
`)

	// Update https only: http must be preserved.
	httpsURL := "http://127.0.0.1:7891"
	result, err := SetProviderProxyConfig(path, "alpha", ProviderProxyUpdate{HTTPS: &httpsURL})
	if err != nil {
		t.Fatalf("SetProviderProxyConfig: %v", err)
	}
	if result.Proxy.HTTP != "http://127.0.0.1:7890" || result.Proxy.HTTPS != httpsURL {
		t.Fatalf("merge failed: %+v", result.Proxy)
	}
	if !result.Proxy.Enabled {
		t.Fatalf("enabled state should be preserved: %+v", result.Proxy)
	}

	// Disable while keeping the URLs.
	disabled := false
	result, err = SetProviderProxyConfig(path, "alpha", ProviderProxyUpdate{Enabled: &disabled})
	if err != nil {
		t.Fatalf("SetProviderProxyConfig: %v", err)
	}
	if result.Proxy.Enabled || result.Proxy.HTTP != "http://127.0.0.1:7890" {
		t.Fatalf("disable failed: %+v", result.Proxy)
	}

	cfg := readProviderProxyConfig(t, path)
	got := cfg.Providers.Items["alpha"].Proxy
	if got == nil || got.Enabled || got.HTTPS != httpsURL || got.HTTP != "http://127.0.0.1:7890" {
		t.Fatalf("merged proxy not persisted: %+v", got)
	}
}

func TestSetProviderProxyConfig_MatchesProviderCaseInsensitively(t *testing.T) {
	path := writeProviderProxyTestConfig(t, `
providers:
  items:
    Alpha:
      enabled: true
      protocol: openai
`)
	httpURL := "http://127.0.0.1:7890"
	result, err := SetProviderProxyConfig(path, "alpha", ProviderProxyUpdate{HTTP: &httpURL})
	if err != nil {
		t.Fatalf("SetProviderProxyConfig: %v", err)
	}
	if result.Name != "Alpha" {
		t.Fatalf("expected canonical name Alpha, got %q", result.Name)
	}
	cfg := readProviderProxyConfig(t, path)
	if cfg.Providers.Items["Alpha"].Proxy == nil {
		t.Fatal("proxy not persisted under Alpha")
	}
}

func TestSetProviderProxyConfig_RejectsInvalidURL(t *testing.T) {
	path := writeProviderProxyTestConfig(t, `
providers:
  items:
    alpha:
      enabled: true
      protocol: openai
`)
	for _, raw := range []string{"ftp://proxy.example.com:21", "not-a-url", "http://", "socks4://127.0.0.1:1080"} {
		value := raw
		if _, err := SetProviderProxyConfig(path, "alpha", ProviderProxyUpdate{HTTP: &value}); err == nil {
			t.Fatalf("expected error for proxy url %q", raw)
		}
	}
}

func TestSetProviderProxyConfig_UnknownProviderFails(t *testing.T) {
	path := writeProviderProxyTestConfig(t, `
providers:
  items:
    alpha:
      enabled: true
      protocol: openai
`)
	httpURL := "http://127.0.0.1:7890"
	if _, err := SetProviderProxyConfig(path, "missing", ProviderProxyUpdate{HTTP: &httpURL}); err == nil {
		t.Fatal("expected error for unknown provider")
	}
}

func TestRemoveProviderProxyConfig(t *testing.T) {
	path := writeProviderProxyTestConfig(t, `
providers:
  items:
    alpha:
      enabled: true
      protocol: openai
      proxy:
        http: http://127.0.0.1:7890
        https: http://127.0.0.1:7891
        enabled: true
    beta:
      enabled: true
      protocol: anthropic
`)

	result, err := RemoveProviderProxyConfig(path, "alpha")
	if err != nil {
		t.Fatalf("RemoveProviderProxyConfig: %v", err)
	}
	if !result.Removed || result.Name != "alpha" {
		t.Fatalf("unexpected result: %+v", result)
	}

	cfg := readProviderProxyConfig(t, path)
	if cfg.Providers.Items["alpha"].Proxy != nil {
		t.Fatalf("proxy still present: %+v", cfg.Providers.Items["alpha"].Proxy)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	if strings.Contains(string(raw), "proxy:") {
		t.Fatalf("proxy node still in yaml:\n%s", raw)
	}

	// Removing again must fail.
	if _, err := RemoveProviderProxyConfig(path, "alpha"); err == nil {
		t.Fatal("expected error when removing a missing proxy")
	}
	// Removing a provider without proxy must fail.
	if _, err := RemoveProviderProxyConfig(path, "beta"); err == nil {
		t.Fatal("expected error when removing proxy from provider without one")
	}
}
func TestSetGlobalProxyConfig_AddsNewProxy(t *testing.T) {
	path := writeProviderProxyTestConfig(t, `
providers:
  items:
    alpha:
      enabled: true
      base_url: https://alpha.example.com
`)
	httpURL := "http://127.0.0.1:7890"
	result, err := SetGlobalProxyConfig(path, GlobalProxyUpdate{HTTP: &httpURL})
	if err != nil {
		t.Fatalf("SetGlobalProxyConfig: %v", err)
	}
	if result.Removed || result.Proxy == nil || result.Proxy.HTTP != httpURL || !result.Proxy.Enabled {
		t.Fatalf("unexpected result: %+v", result)
	}
	cfg := readProviderProxyConfig(t, path)
	if cfg.Providers.Proxy.HTTP != httpURL || !cfg.Providers.Proxy.Enabled {
		t.Fatalf("global proxy not persisted: %+v", cfg.Providers.Proxy)
	}
	if _, ok := cfg.Providers.Items["alpha"]; !ok {
		t.Fatal("existing providers items must be untouched")
	}
}

func TestSetGlobalProxyConfig_CreatesProvidersSection(t *testing.T) {
	path := writeProviderProxyTestConfig(t, `
server:
  name: test
`)
	httpURL := "http://127.0.0.1:7890"
	if _, err := SetGlobalProxyConfig(path, GlobalProxyUpdate{HTTP: &httpURL}); err != nil {
		t.Fatalf("SetGlobalProxyConfig without providers section: %v", err)
	}
	cfg := readProviderProxyConfig(t, path)
	if cfg.Providers.Proxy.HTTP != httpURL {
		t.Fatalf("global proxy not persisted: %+v", cfg.Providers.Proxy)
	}
}

func TestSetGlobalProxyConfig_MergesAndDisables(t *testing.T) {
	path := writeProviderProxyTestConfig(t, `
providers:
  proxy:
    http: http://127.0.0.1:7890
    enabled: true
`)
	httpsURL := "http://127.0.0.1:7891"
	result, err := SetGlobalProxyConfig(path, GlobalProxyUpdate{HTTPS: &httpsURL})
	if err != nil {
		t.Fatalf("SetGlobalProxyConfig (https): %v", err)
	}
	if result.Proxy.HTTP != "http://127.0.0.1:7890" || result.Proxy.HTTPS != httpsURL || !result.Proxy.Enabled {
		t.Fatalf("merge failed: %+v", result.Proxy)
	}

	disabled := false
	result, err = SetGlobalProxyConfig(path, GlobalProxyUpdate{Enabled: &disabled})
	if err != nil {
		t.Fatalf("SetGlobalProxyConfig (disable): %v", err)
	}
	if result.Proxy.Enabled || result.Proxy.HTTP != "http://127.0.0.1:7890" {
		t.Fatalf("disable failed: %+v", result.Proxy)
	}

	cfg := readProviderProxyConfig(t, path)
	got := cfg.Providers.Proxy
	if got.Enabled || got.HTTP != "http://127.0.0.1:7890" || got.HTTPS != httpsURL {
		t.Fatalf("merged global proxy not persisted: %+v", got)
	}
}

func TestSetGlobalProxyConfig_ClearsFieldsAndRejectsBadURL(t *testing.T) {
	path := writeProviderProxyTestConfig(t, `
providers:
  proxy:
    http: http://127.0.0.1:7890
    https: http://127.0.0.1:7891
    enabled: true
`)
	// clear clears the http field, preserving https.
	clear := ""
	if _, err := SetGlobalProxyConfig(path, GlobalProxyUpdate{HTTP: &clear}); err != nil {
		t.Fatalf("SetGlobalProxyConfig (clear): %v", err)
	}
	cfg := readProviderProxyConfig(t, path)
	got := cfg.Providers.Proxy
	if got.HTTP != "" || got.HTTPS != "http://127.0.0.1:7891" || !got.Enabled {
		t.Fatalf("clear failed: %+v", got)
	}

	// Invalid scheme must be rejected.
	bad := "ftp://proxy.example.com:21"
	if _, err := SetGlobalProxyConfig(path, GlobalProxyUpdate{HTTP: &bad}); err == nil {
		t.Fatal("expected error for unsupported proxy scheme")
	}
}

func TestRemoveGlobalProxyConfig(t *testing.T) {
	path := writeProviderProxyTestConfig(t, `
providers:
  proxy:
    http: http://127.0.0.1:7890
  items:
    alpha:
      enabled: true
      base_url: https://alpha.example.com
`)
	result, err := RemoveGlobalProxyConfig(path)
	if err != nil {
		t.Fatalf("RemoveGlobalProxyConfig: %v", err)
	}
	if !result.Removed {
		t.Fatalf("unexpected result: %+v", result)
	}
	cfg := readProviderProxyConfig(t, path)
	if !cfg.Providers.Proxy.IsEmpty() {
		t.Fatalf("global proxy still present: %+v", cfg.Providers.Proxy)
	}
	if _, ok := cfg.Providers.Items["alpha"]; !ok {
		t.Fatal("providers items must be untouched")
	}
	if _, err := RemoveGlobalProxyConfig(path); err == nil {
		t.Fatal("expected error when removing a missing global proxy")
	}
}
