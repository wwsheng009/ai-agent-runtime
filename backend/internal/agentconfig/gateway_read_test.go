package agentconfig

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadGatewaySectionExplicitFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	body := `aicli:
  theme: dark
gateway:
  url: " https://aicli.ttai.online "
  token: " agw_from_tui "
  name: " laptop "
  proxy_url: " http://127.0.0.1:10810 "
  auto_connect: true
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	section, ok, err := LoadGatewaySection(path)
	if err != nil {
		t.Fatalf("LoadGatewaySection: %v", err)
	}
	if !ok {
		t.Fatalf("gateway section not detected")
	}
	if section.URL != "https://aicli.ttai.online" || section.Token != "agw_from_tui" {
		t.Fatalf("url/token = %q / %q", section.URL, section.Token)
	}
	if section.Name != "laptop" || section.ProxyURL != "http://127.0.0.1:10810" {
		t.Fatalf("name/proxy = %q / %q", section.Name, section.ProxyURL)
	}
	if section.AutoConnect == nil || !*section.AutoConnect {
		t.Fatalf("auto_connect not decoded: %+v", section.AutoConnect)
	}
}

func TestLoadGatewaySectionWithoutSection(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("aicli:\n  theme: dark\n"), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	if _, ok, err := LoadGatewaySection(path); err != nil || ok {
		t.Fatalf("missing section = ok:%v err:%v, want ok:false", ok, err)
	}
}

func TestLoadGatewaySectionMissingFile(t *testing.T) {
	if _, _, err := LoadGatewaySection(filepath.Join(t.TempDir(), "absent.yaml")); err == nil {
		t.Fatalf("missing explicit config file was accepted")
	}
}
