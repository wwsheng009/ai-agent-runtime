package commands

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
	"github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
)

func newThemeCommandSession(t *testing.T) (*ChatSession, string) {
	t.Helper()

	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	raw := strings.TrimSpace(`
providers:
  default_provider: alpha
  items:
    alpha:
      enabled: true
      protocol: openai
      base_url: https://alpha.example.com
      default_model: alpha-model
aicli:
  chat:
    default_provider: alpha
    default_model: alpha-model
`)
	if err := os.WriteFile(cfgPath, []byte(raw), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	cfg, err := agentconfig.InitGlobalConfig(cfgPath)
	if err != nil {
		t.Fatalf("InitGlobalConfig: %v", err)
	}
	if err := ui.ApplyThemeSelection(ui.ThemePresetFocus, ui.ThemeModeAuto); err != nil {
		t.Fatalf("ApplyThemeSelection: %v", err)
	}
	return &ChatSession{
		ProviderName: "alpha",
		Provider:     cfg.Providers.Items["alpha"],
		Model:        "alpha-model",
		Config:       cfg,
	}, cfgPath
}

func loadThemePreference(t *testing.T, cfgPath string) (name string, mode string) {
	t.Helper()
	loaded, err := agentconfig.InitGlobalConfig(cfgPath)
	if err != nil {
		t.Fatalf("reload config: %v", err)
	}
	if loaded.AICLI == nil || loaded.AICLI.Theme == nil {
		return "", ""
	}
	return strings.TrimSpace(loaded.AICLI.Theme.Name), strings.TrimSpace(loaded.AICLI.Theme.Mode)
}

func TestParseThemeCommandRequest(t *testing.T) {
	cases := []struct {
		input   string
		want    themeCommandAction
		palette string
		mode    string
		wantErr bool
	}{
		{"/theme", themeCommandSelect, "", "", false},
		{"/theme select", themeCommandSelect, "", "", false},
		{"/theme status", themeCommandStatus, "", "", false},
		{"/theme list", themeCommandList, "", "", false},
		{"/theme preview", themeCommandPreview, "", "", false},
		{"/theme sample", themeCommandPreview, "", "", false},
		{"/theme contrast", themeCommandSet, ui.ThemePresetContrast, "", false},
		{"/theme high-contrast", themeCommandSet, ui.ThemePresetContrast, "", false},
		{"/theme mono", themeCommandSet, ui.ThemePresetMono, "", false},
		{"/theme minimal", themeCommandSet, ui.ThemePresetMono, "", false},
		{"/theme default", themeCommandSet, ui.ThemePresetFocus, "", false},
		{"/theme dark", themeCommandSet, "", ui.ThemeModeDark, false},
		{"/theme light", themeCommandSet, "", ui.ThemeModeLight, false},
		{"/theme auto", themeCommandSet, "", ui.ThemeModeAuto, false},
		{"/theme light contrast", themeCommandSet, ui.ThemePresetContrast, ui.ThemeModeLight, false},
		{"/theme contrast dark", themeCommandSet, ui.ThemePresetContrast, ui.ThemeModeDark, false},
		{"/theme rainbow", 0, "", "", true},
		{"/theme contrast mono", 0, "", "", true},
		{"/theme dark light", 0, "", "", true},
	}
	for _, tc := range cases {
		got, err := parseThemeCommandRequest(tc.input)
		if tc.wantErr {
			if err == nil {
				t.Fatalf("%s: expected error, got %+v", tc.input, got)
			}
			continue
		}
		if err != nil {
			t.Fatalf("%s: unexpected error %v", tc.input, err)
		}
		if got.Action != tc.want {
			t.Fatalf("%s: action=%d want=%d", tc.input, got.Action, tc.want)
		}
		if got.Action == themeCommandSet {
			if got.Palette != tc.palette {
				t.Fatalf("%s: palette=%q want=%q", tc.input, got.Palette, tc.palette)
			}
			if got.Mode != tc.mode {
				t.Fatalf("%s: mode=%q want=%q", tc.input, got.Mode, tc.mode)
			}
		}
	}
}

func TestHandleThemeCommand_SetPersistsPreference(t *testing.T) {
	session, cfgPath := newThemeCommandSession(t)
	t.Cleanup(func() {
		_ = ui.ApplyThemeSelection(ui.ThemePresetFocus, ui.ThemeModeAuto)
	})

	if quit := handleThemeCommand(session, "/theme contrast", true); quit {
		t.Fatal("expected /theme command not to exit")
	}
	if ui.CurrentThemeName() != ui.ThemePresetContrast {
		t.Fatalf("expected current theme contrast, got %q", ui.CurrentThemeName())
	}
	if session.Config.AICLI == nil || session.Config.AICLI.Theme == nil || session.Config.AICLI.Theme.Name != ui.ThemePresetContrast {
		t.Fatalf("expected in-memory theme preference contrast, got %+v", session.Config.AICLI)
	}
	name, mode := loadThemePreference(t, cfgPath)
	if name != ui.ThemePresetContrast {
		t.Fatalf("expected persisted theme contrast, got %q", name)
	}
	if mode != ui.ThemeModeAuto {
		t.Fatalf("expected persisted mode auto, got %q", mode)
	}
}

func TestHandleThemeCommand_SetModePersistsPreference(t *testing.T) {
	session, cfgPath := newThemeCommandSession(t)
	t.Cleanup(func() {
		_ = ui.ApplyThemeSelection(ui.ThemePresetFocus, ui.ThemeModeAuto)
	})

	if quit := handleThemeCommand(session, "/theme light", true); quit {
		t.Fatal("expected /theme light not to exit")
	}
	if ui.CurrentThemeModeName() != ui.ThemeModeLight {
		t.Fatalf("expected current mode light, got %q", ui.CurrentThemeModeName())
	}
	if ui.CurrentThemeName() != ui.ThemePresetFocus {
		t.Fatalf("expected palette to remain focus, got %q", ui.CurrentThemeName())
	}
	name, mode := loadThemePreference(t, cfgPath)
	if mode != ui.ThemeModeLight {
		t.Fatalf("expected persisted mode light, got %q", mode)
	}
	if name != ui.ThemePresetFocus {
		t.Fatalf("expected persisted palette focus, got %q", name)
	}
}

func TestHandleThemeCommand_SetModeAndPalette(t *testing.T) {
	session, cfgPath := newThemeCommandSession(t)
	t.Cleanup(func() {
		_ = ui.ApplyThemeSelection(ui.ThemePresetFocus, ui.ThemeModeAuto)
	})

	if quit := handleThemeCommand(session, "/theme dark mono", true); quit {
		t.Fatal("expected /theme dark mono not to exit")
	}
	if ui.CurrentThemeModeName() != ui.ThemeModeDark {
		t.Fatalf("expected mode dark, got %q", ui.CurrentThemeModeName())
	}
	if ui.CurrentThemeName() != ui.ThemePresetMono {
		t.Fatalf("expected palette mono, got %q", ui.CurrentThemeName())
	}
	name, mode := loadThemePreference(t, cfgPath)
	if name != ui.ThemePresetMono || mode != ui.ThemeModeDark {
		t.Fatalf("expected persisted mono+dark, got name=%q mode=%q", name, mode)
	}
}

func TestHandleThemeCommand_StatusDoesNotMutateOrPersist(t *testing.T) {
	session, cfgPath := newThemeCommandSession(t)
	t.Cleanup(func() {
		_ = ui.ApplyThemeSelection(ui.ThemePresetFocus, ui.ThemeModeAuto)
	})

	if quit := handleThemeCommand(session, "/theme status", true); quit {
		t.Fatal("expected /theme status not to exit")
	}
	if ui.CurrentThemeName() != ui.ThemePresetFocus {
		t.Fatalf("expected theme to remain focus, got %q", ui.CurrentThemeName())
	}
	name, mode := loadThemePreference(t, cfgPath)
	if name != "" || mode != "" {
		t.Fatalf("expected no persisted theme after status, got name=%q mode=%q", name, mode)
	}
}

func TestHandleThemeCommand_NoOpWhenAlreadyMatching(t *testing.T) {
	session, cfgPath := newThemeCommandSession(t)
	t.Cleanup(func() {
		_ = ui.ApplyThemeSelection(ui.ThemePresetFocus, ui.ThemeModeAuto)
	})

	if quit := handleThemeCommand(session, "/theme focus", true); quit {
		t.Fatal("expected /theme focus not to exit")
	}
	name, mode := loadThemePreference(t, cfgPath)
	if name != "" || mode != "" {
		t.Fatalf("expected no persisted theme when value unchanged, got name=%q mode=%q", name, mode)
	}
}

func TestHandleThemeCommand_NonInteractiveBareShowsStatus(t *testing.T) {
	session, cfgPath := newThemeCommandSession(t)
	t.Cleanup(func() {
		_ = ui.ApplyThemeSelection(ui.ThemePresetFocus, ui.ThemeModeAuto)
	})

	if quit := handleThemeCommand(session, "/theme", true); quit {
		t.Fatal("expected /theme not to exit")
	}
	if ui.CurrentThemeName() != ui.ThemePresetFocus {
		t.Fatalf("expected theme to remain focus, got %q", ui.CurrentThemeName())
	}
	name, mode := loadThemePreference(t, cfgPath)
	if name != "" || mode != "" {
		t.Fatalf("expected no persisted theme after non-interactive bare /theme, got name=%q mode=%q", name, mode)
	}
}

func TestThemeSlashArgumentCandidatesIncludePresetsAndModes(t *testing.T) {
	candidates := themeSlashArgumentCandidates()
	found := map[string]bool{}
	for _, candidate := range candidates {
		found[candidate.Command] = true
	}
	for _, name := range []string{"auto", "dark", "light", "classic", "focus", "contrast", "mono", "list", "status", "preview", "select"} {
		if !found[name] {
			t.Fatalf("expected theme candidate %q, got %#v", name, candidates)
		}
	}
}

func TestHandleThemeCommand_PreviewDoesNotMutateOrPersist(t *testing.T) {
	session, cfgPath := newThemeCommandSession(t)
	t.Cleanup(func() {
		_ = ui.ApplyThemeSelection(ui.ThemePresetFocus, ui.ThemeModeAuto)
	})

	if quit := handleThemeCommand(session, "/theme preview", true); quit {
		t.Fatal("expected /theme preview not to exit")
	}
	if ui.CurrentThemeName() != ui.ThemePresetFocus {
		t.Fatalf("expected theme to remain focus, got %q", ui.CurrentThemeName())
	}
	if ui.CurrentThemeModeName() != ui.ThemeModeAuto {
		t.Fatalf("expected mode to remain auto, got %q", ui.CurrentThemeModeName())
	}
	name, mode := loadThemePreference(t, cfgPath)
	if name != "" || mode != "" {
		t.Fatalf("expected no persisted theme after preview, got name=%q mode=%q", name, mode)
	}
}
