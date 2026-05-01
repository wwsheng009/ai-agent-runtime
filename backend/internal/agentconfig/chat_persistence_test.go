package agentconfig

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestUpdateAICLIChatPreferences_UpdatesOnlyChatSection(t *testing.T) {
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
custom_section:
  keep: true
`)
	if err := os.WriteFile(path, []byte(raw), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	updated, err := UpdateAICLIChatPreferences(path, AICLIChatPreferenceUpdate{
		DefaultProvider: stringPtr("beta"),
		DefaultModel:    stringPtr("beta-model"),
		ReasoningEffort: stringPtr("medium"),
	})
	if err != nil {
		t.Fatalf("UpdateAICLIChatPreferences: %v", err)
	}
	if updated == nil {
		t.Fatal("expected updated chat config")
	}
	if updated.DefaultProvider != "beta" || updated.DefaultModel != "beta-model" || updated.ReasoningEffort != "medium" {
		t.Fatalf("unexpected updated config: %+v", updated)
	}

	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	text := string(content)
	for _, expected := range []string{
		"providers:",
		"custom_section:",
		"default_provider: beta",
		"default_model: beta-model",
		"reasoning_effort: medium",
	} {
		if !strings.Contains(text, expected) {
			t.Fatalf("expected %q in updated file:\n%s", expected, text)
		}
	}

	loaded, err := InitGlobalConfig(path)
	if err != nil {
		t.Fatalf("InitGlobalConfig: %v", err)
	}
	if loaded.AICLI == nil || loaded.AICLI.Chat == nil {
		t.Fatalf("expected aicli.chat to be loaded, got %+v", loaded.AICLI)
	}
	if loaded.AICLI.Chat.DefaultProvider != "beta" {
		t.Fatalf("expected loaded default_provider beta, got %q", loaded.AICLI.Chat.DefaultProvider)
	}
	if loaded.AICLI.Chat.DefaultModel != "beta-model" {
		t.Fatalf("expected loaded default_model beta-model, got %q", loaded.AICLI.Chat.DefaultModel)
	}
	if loaded.AICLI.Chat.ReasoningEffort != "medium" {
		t.Fatalf("expected loaded reasoning_effort medium, got %q", loaded.AICLI.Chat.ReasoningEffort)
	}
}

func stringPtr(v string) *string {
	v = strings.TrimSpace(v)
	return &v
}

func boolDoublePtr(v bool) **bool {
	inner := v
	innerPtr := &inner
	return &innerPtr
}

func TestUpdateAICLIChatPreferences_PersistsStreamMode(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	raw := strings.TrimSpace(`
providers:
  default_provider: alpha
  items:
    alpha:
      enabled: true
      protocol: openai
aicli:
  chat:
    default_provider: alpha
    default_model: alpha-model
`)
	if err := os.WriteFile(path, []byte(raw), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	updated, err := UpdateAICLIChatPreferences(path, AICLIChatPreferenceUpdate{
		Stream: boolDoublePtr(false),
	})
	if err != nil {
		t.Fatalf("UpdateAICLIChatPreferences: %v", err)
	}
	if updated == nil || updated.Stream == nil || *updated.Stream != false {
		t.Fatalf("expected updated stream=false, got %+v", updated)
	}

	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	if !strings.Contains(string(content), "stream: false") {
		t.Fatalf("expected stream: false in updated file:\n%s", string(content))
	}

	loaded, err := InitGlobalConfig(path)
	if err != nil {
		t.Fatalf("InitGlobalConfig: %v", err)
	}
	if loaded.AICLI == nil || loaded.AICLI.Chat == nil || loaded.AICLI.Chat.Stream == nil {
		t.Fatalf("expected stream to be loaded, got %+v", loaded.AICLI)
	}
	if *loaded.AICLI.Chat.Stream != false {
		t.Fatalf("expected loaded stream=false, got %v", *loaded.AICLI.Chat.Stream)
	}

	// Round-trip: clear stream preference (set inner pointer to nil) and verify it disappears.
	var nilInner *bool
	updated, err = UpdateAICLIChatPreferences(path, AICLIChatPreferenceUpdate{
		Stream: &nilInner,
	})
	if err != nil {
		t.Fatalf("UpdateAICLIChatPreferences clear: %v", err)
	}
	if updated.Stream != nil {
		t.Fatalf("expected cleared stream, got %+v", updated.Stream)
	}
	content, err = os.ReadFile(path)
	if err != nil {
		t.Fatalf("read config after clear: %v", err)
	}
	if strings.Contains(string(content), "stream:") {
		t.Fatalf("expected stream field removed:\n%s", string(content))
	}
}
