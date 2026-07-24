package agentconfig

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestUpdateAICLIThemePreferences_UpdatesOnlyThemeSection(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	raw := strings.TrimSpace(`
providers:
  default_provider: alpha
  items:
    alpha:
      enabled: true
      protocol: openai
      base_url: https://alpha.example.com
aicli:
  chat:
    default_provider: alpha
    default_model: alpha-model
custom_section:
  keep: true
`)
	if err := os.WriteFile(path, []byte(raw), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	name := "contrast"
	mode := "light"
	updated, err := UpdateAICLIThemePreferences(path, AICLIThemePreferenceUpdate{
		Name: &name,
		Mode: &mode,
	})
	if err != nil {
		t.Fatalf("UpdateAICLIThemePreferences: %v", err)
	}
	if updated == nil || updated.Name != "contrast" || updated.Mode != "light" {
		t.Fatalf("unexpected updated theme: %+v", updated)
	}

	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	text := string(content)
	for _, expected := range []string{
		"providers:",
		"custom_section:",
		"default_provider: alpha",
		"default_model: alpha-model",
		"name: contrast",
		"mode: light",
	} {
		if !strings.Contains(text, expected) {
			t.Fatalf("expected %q in updated file:\n%s", expected, text)
		}
	}

	loaded, err := InitGlobalConfig(path)
	if err != nil {
		t.Fatalf("InitGlobalConfig: %v", err)
	}
	if loaded.AICLI == nil || loaded.AICLI.Theme == nil {
		t.Fatalf("expected aicli.theme to be loaded, got %+v", loaded.AICLI)
	}
	if loaded.AICLI.Theme.Name != "contrast" {
		t.Fatalf("expected loaded theme contrast, got %q", loaded.AICLI.Theme.Name)
	}
	if loaded.AICLI.Theme.Mode != "light" {
		t.Fatalf("expected loaded mode light, got %q", loaded.AICLI.Theme.Mode)
	}
	if loaded.AICLI.Chat == nil || loaded.AICLI.Chat.DefaultProvider != "alpha" {
		t.Fatalf("expected chat preferences preserved, got %+v", loaded.AICLI.Chat)
	}
}
