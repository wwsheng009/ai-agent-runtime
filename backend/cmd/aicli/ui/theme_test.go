package ui

import (
	"strings"
	"testing"

	"github.com/fatih/color"
)

func TestSetThemePreset_ChangesCurrentThemeName(t *testing.T) {
	oldNoColor := color.NoColor
	color.NoColor = true
	defer func() {
		color.NoColor = oldNoColor
		_ = SetThemePreset(ThemePresetFocus)
	}()

	if err := SetThemePreset("contrast"); err != nil {
		t.Fatalf("SetThemePreset: %v", err)
	}
	if got := CurrentThemeName(); got != ThemePresetContrast {
		t.Fatalf("expected current theme %q, got %q", ThemePresetContrast, got)
	}
	if theme := GetTheme(ThemeAuto); theme == nil || theme.Name != ThemePresetContrast {
		t.Fatalf("expected loaded theme preset %q, got %+v", ThemePresetContrast, theme)
	}
}

func TestSetThemePreset_RejectsUnknownTheme(t *testing.T) {
	if err := SetThemePreset("unknown-theme"); err == nil {
		t.Fatal("expected unknown theme to fail")
	}
}

func TestSetThemeMode_ChangesLightDarkAxis(t *testing.T) {
	oldNoColor := color.NoColor
	color.NoColor = true
	defer func() {
		color.NoColor = oldNoColor
		_ = ApplyThemeSelection(ThemePresetFocus, ThemeModeAuto)
	}()

	if err := SetThemeMode("light"); err != nil {
		t.Fatalf("SetThemeMode: %v", err)
	}
	if got := CurrentThemeModeName(); got != ThemeModeLight {
		t.Fatalf("expected mode light, got %q", got)
	}
	if theme := GetTheme(ThemeAuto); theme == nil || theme.Type != ThemeLight {
		t.Fatalf("expected resolved light theme, got %+v", theme)
	}

	if err := SetThemeMode("dark"); err != nil {
		t.Fatalf("SetThemeMode dark: %v", err)
	}
	if theme := GetTheme(ThemeAuto); theme == nil || theme.Type != ThemeDark {
		t.Fatalf("expected resolved dark theme, got %+v", theme)
	}
}

func TestApplyThemeSelection_SetsBothAxes(t *testing.T) {
	oldNoColor := color.NoColor
	color.NoColor = true
	defer func() {
		color.NoColor = oldNoColor
		_ = ApplyThemeSelection(ThemePresetFocus, ThemeModeAuto)
	}()

	if err := ApplyThemeSelection(ThemePresetMono, ThemeModeLight); err != nil {
		t.Fatalf("ApplyThemeSelection: %v", err)
	}
	if CurrentThemeName() != ThemePresetMono {
		t.Fatalf("expected palette mono, got %q", CurrentThemeName())
	}
	if CurrentThemeModeName() != ThemeModeLight {
		t.Fatalf("expected mode light, got %q", CurrentThemeModeName())
	}
	theme := GetTheme(ThemeAuto)
	if theme == nil || theme.Name != ThemePresetMono || theme.Type != ThemeLight {
		t.Fatalf("expected mono+light theme, got %+v", theme)
	}
}

func TestNormalizeThemeModeName(t *testing.T) {
	cases := map[string]string{
		"":       ThemeModeAuto,
		"auto":   ThemeModeAuto,
		"system": ThemeModeAuto,
		"dark":   ThemeModeDark,
		"night":  ThemeModeDark,
		"light":  ThemeModeLight,
		"white":  ThemeModeLight,
		"nope":   "",
	}
	for raw, want := range cases {
		if got := NormalizeThemeModeName(raw); got != want {
			t.Fatalf("NormalizeThemeModeName(%q)=%q want %q", raw, got, want)
		}
	}
}

func TestFormatAssistantSupplementBlock_PreservesPlainLayoutWithoutColor(t *testing.T) {
	oldNoColor := color.NoColor
	color.NoColor = true
	defer func() {
		color.NoColor = oldNoColor
		_ = SetThemePreset(ThemePresetFocus)
	}()

	if err := SetThemePreset(ThemePresetContrast); err != nil {
		t.Fatalf("SetThemePreset: %v", err)
	}

	raw := "[tool done] execute_shell_command command=git status\n  failed: exit status 1"
	if got := FormatAssistantSupplementBlock(raw); got != raw {
		t.Fatalf("expected plain layout to be preserved, got %q", got)
	}

	diff := "• Edited internal\\service\\shop\\endpoint\\security.go (+1 -1)\n      259 -     oldValue,\n      259 +     newValue,"
	if got := FormatAssistantSupplementBlock(diff); got != diff {
		t.Fatalf("expected plain diff layout to be preserved, got %q", got)
	}
}

func TestFormatAssistantSupplementBlock_CollapsesRedundantBlankLines(t *testing.T) {
	oldNoColor := color.NoColor
	color.NoColor = true
	defer func() {
		color.NoColor = oldNoColor
		_ = SetThemePreset(ThemePresetFocus)
	}()

	if err := SetThemePreset(ThemePresetContrast); err != nil {
		t.Fatalf("SetThemePreset: %v", err)
	}

	raw := "\n\n[prompt] layers=unknown/system\n\n\n(instruction 471 / total 2490 tokens)\n\n\n"
	want := "[prompt] layers=unknown/system\n\n(instruction 471 / total 2490 tokens)"
	if got := FormatAssistantSupplementBlock(raw); got != want {
		t.Fatalf("expected redundant blank lines to collapse, got %q", got)
	}
}

func TestStyleAssistantSupplementLine_ColorsEditedDiffLinesByTheme(t *testing.T) {
	oldNoColor := color.NoColor
	color.NoColor = false
	defer func() {
		color.NoColor = oldNoColor
		_ = SetThemePreset(ThemePresetFocus)
	}()

	theme := createTheme(ThemeDark)
	addBody := "259 +     \"updated_at\": now,"
	delBody := "260 -     \"last_audit_id\": audit.ID.String(),"
	addLine := theme.StyleAssistantSupplementLine("      " + addBody)
	delLine := theme.StyleAssistantSupplementLine("      " + delBody)
	if addLine == "      "+addBody || delLine == "      "+delBody {
		t.Fatal("expected edited diff lines to be colorized")
	}
	if !strings.Contains(addLine, "\x1b[") || !strings.Contains(delLine, "\x1b[") {
		t.Fatalf("expected ANSI color sequences in diff lines, got add=%q del=%q", addLine, delLine)
	}
	if want := "      " + theme.SuccessColor.Sprint(addBody); addLine != want {
		t.Fatalf("expected added line to use theme success color, got %q want %q", addLine, want)
	}
	if want := "      " + theme.ErrorColor.Sprint(delBody); delLine != want {
		t.Fatalf("expected deleted line to use theme error color, got %q want %q", delLine, want)
	}
}

func TestThemePresets_AllRolesNonNilForEveryPaletteAndMode(t *testing.T) {
	oldNoColor := color.NoColor
	oldName := currentThemeName
	oldMode := currentThemeMode
	oldTheme := currentTheme
	color.NoColor = false
	defer func() {
		color.NoColor = oldNoColor
		themeMutex.Lock()
		currentThemeName = oldName
		currentThemeMode = oldMode
		currentTheme = oldTheme
		themeMutex.Unlock()
	}()

	palettes := []string{ThemePresetClassic, ThemePresetFocus, ThemePresetContrast, ThemePresetMono}
	modes := []ThemeType{ThemeDark, ThemeLight}

	for _, palette := range palettes {
		for _, mode := range modes {
			themeMutex.Lock()
			currentThemeName = palette
			currentThemeMode = mode
			currentTheme = nil
			themeMutex.Unlock()

			theme := createTheme(mode)
			if theme == nil {
				t.Fatalf("createTheme(%s/%v) returned nil", palette, mode)
			}
			if theme.Name != palette {
				t.Fatalf("theme.Name=%q want %q (mode=%v)", theme.Name, palette, mode)
			}
			if theme.Type != mode {
				t.Fatalf("theme.Type=%v want %v (palette=%s)", theme.Type, mode, palette)
			}

			roles := map[string]*color.Color{
				"UserColor":       theme.UserColor,
				"AssistantColor":  theme.AssistantColor,
				"SystemColor":     theme.SystemColor,
				"CommandColor":    theme.CommandColor,
				"OutputColor":     theme.OutputColor,
				"SecondaryColor":  theme.SecondaryColor,
				"MutedColor":      theme.MutedColor,
				"MetaLabelColor":  theme.MetaLabelColor,
				"TimelineColor":   theme.TimelineColor,
				"ToolColor":       theme.ToolColor,
				"ReasoningColor":  theme.ReasoningColor,
				"ApprovalColor":   theme.ApprovalColor,
				"ErrorColor":      theme.ErrorColor,
				"WarningColor":    theme.WarningColor,
				"SuccessColor":    theme.SuccessColor,
				"InfoColor":       theme.InfoColor,
				"SeparatorColor":  theme.SeparatorColor,
				"ProgressColor":   theme.ProgressColor,
			}
			for name, c := range roles {
				if c == nil {
					t.Fatalf("%s/%v: %s is nil", palette, mode, name)
				}
			}
		}
	}
}

func TestThemePresets_MonoDoesNotUseSemanticChromaticStatusColors(t *testing.T) {
	oldNoColor := color.NoColor
	oldName := currentThemeName
	oldMode := currentThemeMode
	oldTheme := currentTheme
	color.NoColor = false
	defer func() {
		color.NoColor = oldNoColor
		themeMutex.Lock()
		currentThemeName = oldName
		currentThemeMode = oldMode
		currentTheme = oldTheme
		themeMutex.Unlock()
	}()

	// Compare mono status colors against base chromatic red/green/yellow by Sprint.
	// Mono should render only bold/default/muted, not the base Error/Success/Warning styles.
	for _, mode := range []ThemeType{ThemeDark, ThemeLight} {
		themeMutex.Lock()
		currentThemeName = ThemePresetMono
		currentThemeMode = mode
		currentTheme = nil
		themeMutex.Unlock()
		mono := createTheme(mode)

		themeMutex.Lock()
		currentThemeName = ThemePresetFocus
		currentTheme = nil
		themeMutex.Unlock()
		// Build base-like status colors via a temporary classic/focus path is hard;
		// instead, sample known chromatic base attributes.
		base := baseTheme(mode)

		sample := "status"
		if mono.ErrorColor.Sprint(sample) == base.ErrorColor.Sprint(sample) {
			t.Fatalf("mono/%v ErrorColor still matches chromatic base", mode)
		}
		if mono.SuccessColor.Sprint(sample) == base.SuccessColor.Sprint(sample) {
			t.Fatalf("mono/%v SuccessColor still matches chromatic base", mode)
		}
		if mono.WarningColor.Sprint(sample) == base.WarningColor.Sprint(sample) {
			t.Fatalf("mono/%v WarningColor still matches chromatic base", mode)
		}
		if mono.ProgressColor.Sprint(sample) == base.ProgressColor.Sprint(sample) {
			t.Fatalf("mono/%v ProgressColor still matches chromatic base", mode)
		}
	}
}

func TestThemeDescriptions_CoverAllModesAndPalettes(t *testing.T) {
	for _, mode := range SupportedThemeModeNames() {
		if ThemeModeDescription(mode) == "" {
			t.Fatalf("missing ThemeModeDescription for %q", mode)
		}
	}
	for _, palette := range SupportedThemePresetNames() {
		if ThemePresetDescription(palette) == "" {
			t.Fatalf("missing ThemePresetDescription for %q", palette)
		}
	}
}

func TestBuildThemePreview_DoesNotMutateGlobalSelection(t *testing.T) {
	oldNoColor := color.NoColor
	oldName := currentThemeName
	oldMode := currentThemeMode
	oldTheme := currentTheme
	color.NoColor = false
	defer func() {
		color.NoColor = oldNoColor
		themeMutex.Lock()
		currentThemeName = oldName
		currentThemeMode = oldMode
		currentTheme = oldTheme
		themeMutex.Unlock()
	}()

	if err := ApplyThemeSelection(ThemePresetFocus, ThemeModeDark); err != nil {
		t.Fatalf("ApplyThemeSelection: %v", err)
	}

	preview := BuildThemePreview(ThemePresetMono, ThemeModeLight)
	if preview == nil {
		t.Fatal("BuildThemePreview returned nil")
	}
	if preview.Name != ThemePresetMono {
		t.Fatalf("preview.Name=%q want mono", preview.Name)
	}
	if preview.Type != ThemeLight {
		t.Fatalf("preview.Type=%v want light", preview.Type)
	}
	if CurrentThemeName() != ThemePresetFocus {
		t.Fatalf("global palette mutated: %q", CurrentThemeName())
	}
	if CurrentThemeModeName() != ThemeModeDark {
		t.Fatalf("global mode mutated: %q", CurrentThemeModeName())
	}

	sample := FormatThemePreviewSample(preview)
	if sample == "" {
		t.Fatal("expected non-empty preview sample")
	}
	if !strings.Contains(sample, "user") || !strings.Contains(sample, "tool") {
		t.Fatalf("expected sample tokens in %q", sample)
	}
}

func TestThemePresets_ClassicLightCoversCoreUIRoles(t *testing.T) {
	oldNoColor := color.NoColor
	oldName := currentThemeName
	oldMode := currentThemeMode
	oldTheme := currentTheme
	color.NoColor = false
	defer func() {
		color.NoColor = oldNoColor
		themeMutex.Lock()
		currentThemeName = oldName
		currentThemeMode = oldMode
		currentTheme = oldTheme
		themeMutex.Unlock()
	}()

	themeMutex.Lock()
	currentThemeName = ThemePresetClassic
	currentThemeMode = ThemeLight
	currentTheme = nil
	themeMutex.Unlock()

	classic := createTheme(ThemeLight)
	base := baseTheme(ThemeLight)

	// Classic light must diverge from bare base on its signature accent roles
	// (meta/tool/info), proving the palette actually layers, not just inherits.
	sample := "role"
	if classic.MetaLabelColor.Sprint(sample) == base.MetaLabelColor.Sprint(sample) &&
		classic.ToolColor.Sprint(sample) == base.ToolColor.Sprint(sample) &&
		classic.InfoColor.Sprint(sample) == base.InfoColor.Sprint(sample) {
		t.Fatal("classic light appears identical to base for meta/tool/info; palette overlay incomplete")
	}
	if classic.UserColor == nil || classic.AssistantColor == nil || classic.SeparatorColor == nil {
		t.Fatal("classic light missing core role colors")
	}
}
