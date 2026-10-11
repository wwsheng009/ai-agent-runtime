package agentconfig

import (
	"os"
	"path/filepath"
	"testing"
)

// TestUpdateGatewayConfigCreatesSection 验证首次写入会在空配置上创建 gateway 段。
func TestUpdateGatewayConfigCreatesSection(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("providers:\n  items: {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	url := "wss://gw.example.com"
	token := "agw_abcdef123456"
	enabled := true
	cfg, err := UpdateGatewayConfig(path, GatewayPreferenceUpdate{URL: &url, Token: &token, Enabled: &enabled})
	if err != nil {
		t.Fatalf("UpdateGatewayConfig: %v", err)
	}
	if !cfg.Enabled {
		t.Fatal("explicit enabled=true must persist")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	for _, want := range []string{"gateway:", "url: wss://gw.example.com", "token: agw_abcdef123456"} {
		if !contains(text, want) {
			t.Fatalf("config file missing %q:\n%s", want, text)
		}
	}
	// 不相关段必须保留。
	if !contains(text, "providers:") {
		t.Fatalf("unrelated section dropped:\n%s", text)
	}
}

// TestUpdateGatewayConfigPreservesUnrelatedSections 验证二次写入不破坏其它段与注释密度。
func TestUpdateGatewayConfigPreservesUnrelatedSections(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	original := "# top comment\nproviders:\n  items:\n    openai:\n      api_key: keep\n"
	if err := os.WriteFile(path, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}
	name := "laptop"
	autoConnect := false
	if _, err := UpdateGatewayConfig(path, GatewayPreferenceUpdate{Name: &name, AutoConnect: &autoConnect}); err != nil {
		t.Fatalf("UpdateGatewayConfig: %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	for _, want := range []string{"providers:", "api_key: keep", "gateway:", "name: laptop", "auto_connect: false"} {
		if !contains(text, want) {
			t.Fatalf("config file missing %q:\n%s", want, text)
		}
	}
	if contains(text, "auto_connect: true") {
		t.Fatalf("auto_connect must be explicitly false:\n%s", text)
	}
}

// TestUpdateGatewayConfigClearsToken 验证空串清除语义（disable 路径使用）。
func TestUpdateGatewayConfigClearsToken(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	url := "wss://gw.example.com"
	token := "agw_secret"
	if _, err := UpdateGatewayConfig(path, GatewayPreferenceUpdate{URL: &url, Token: &token}); err != nil {
		t.Fatal(err)
	}
	empty := ""
	cfg, err := UpdateGatewayConfig(path, GatewayPreferenceUpdate{Token: &empty, Enabled: boolPtr(false)})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Token != "" || cfg.Enabled {
		t.Fatalf("token/enabled not cleared: %+v", cfg)
	}
}

// TestUpdateGatewayConfigRequiresPath 验证空路径报错。
func TestUpdateGatewayConfigRequiresPath(t *testing.T) {
	if _, err := UpdateGatewayConfig("  ", GatewayPreferenceUpdate{}); err == nil {
		t.Fatal("empty path must fail")
	}
}

func contains(haystack, needle string) bool {
	return len(haystack) >= len(needle) && (haystack == needle || indexOf(haystack, needle) >= 0)
}

func indexOf(haystack, needle string) int {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return i
		}
	}
	return -1
}
