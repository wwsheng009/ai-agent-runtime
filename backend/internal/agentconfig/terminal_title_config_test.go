package agentconfig

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestInitGlobalConfigLoadsTerminalTitlePreferences(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	configYAML := `
aicli:
  chat:
    terminal_title:
      enabled: false
      animations: false
      items:
        - activity
        - state
        - project
`
	if err := os.WriteFile(configPath, []byte(configYAML), 0o644); err != nil {
		t.Fatalf("write config yaml: %v", err)
	}

	cfg, err := InitGlobalConfig(configPath)
	if err != nil {
		t.Fatalf("InitGlobalConfig: %v", err)
	}
	if cfg.AICLI == nil || cfg.AICLI.Chat == nil || cfg.AICLI.Chat.TerminalTitle == nil {
		t.Fatalf("terminal title config not loaded: %+v", cfg.AICLI)
	}
	title := cfg.AICLI.Chat.TerminalTitle
	if title.Enabled == nil || *title.Enabled {
		t.Fatalf("enabled = %v, want explicit false", title.Enabled)
	}
	if title.Animations == nil || *title.Animations {
		t.Fatalf("animations = %v, want explicit false", title.Animations)
	}
	if want := []string{"activity", "state", "project"}; !reflect.DeepEqual(title.Items, want) {
		t.Fatalf("items = %#v, want %#v", title.Items, want)
	}
}
