package ui

import (
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/render"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/style"
	"strings"
	"testing"
)

func TestSetThemePreset_ChangesCurrentThemeName(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	defer func() {
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
	t.Setenv("NO_COLOR", "1")
	defer func() {
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
	t.Setenv("NO_COLOR", "1")
	defer func() {
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
	t.Setenv("NO_COLOR", "1")
	defer func() {
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

func TestFormatAssistantSupplementBlock_RendersMultiFileTokenColors(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	t.Setenv("FORCE_COLOR", "1")
	t.Setenv("AICLI_COLOR_DEPTH", "truecolor")
	previousSyntax := CurrentSyntaxThemeName()
	defer func() { _ = SetSyntaxTheme(previousSyntax) }()
	if err := SetSyntaxTheme("monokai"); err != nil {
		t.Fatalf("SetSyntaxTheme: %v", err)
	}

	raw := strings.Join([]string{
		"• Edited first.go (+1 -0)",
		"        1 + value := render(item)",
		"• Diff second.ts (+1 -0)",
		"        1 + const value = 1",
	}, "\n")
	got := FormatAssistantSupplementBlock(raw)
	plain := render.ANSIToPlain(got)
	for _, want := range []string{"• Edited first.go", "value := render(item)", "• Diff second.ts", "const value = 1"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("rendered diff lost %q: %q", want, plain)
		}
	}
	for _, sgr := range []string{"38;2;166;226;46", "38;2;249;38;114"} {
		if !strings.Contains(got, sgr) {
			t.Fatalf("expected Chroma token color %q in %q", sgr, got)
		}
	}
}

func TestFormatAssistantSupplementBlock_SingleLineTimelineUsesDocument(t *testing.T) {
	t.Setenv("NO_COLOR", "1")

	for _, line := range []string{
		"[tool] view path",
		"[tool done] shell exit 0",
		"[reasoning] planning next step",
		"[task] started worker-1",
	} {
		if got := FormatAssistantSupplementBlock(line); got != line {
			t.Fatalf("single-line timeline should stay layout-identical, line=%q got=%q", line, got)
		}
	}

	// Bullet notices keep marker via Document; unknown tags stay legacy.
	bullet := "• Edited file.go"
	if got := FormatAssistantSupplementBlock(bullet); got != bullet {
		t.Fatalf("bullet notice should stay layout-identical, got %q", got)
	}
	bulletTag := "• [task] started worker"
	if got := FormatAssistantSupplementBlock(bulletTag); got != bulletTag {
		t.Fatalf("bullet+tag should stay layout-identical, got %q", got)
	}
	unknown := "[prompt] layers=system"
	if got := FormatAssistantSupplementBlock(unknown); got != unknown {
		t.Fatalf("unknown tag should stay plain, got %q", got)
	}
}

func TestFormatAssistantSupplementBlock_MultiLineKnownTimelineUsesDocument(t *testing.T) {
	t.Setenv("NO_COLOR", "1")

	// First line is a known layout-identical tag; indented body stays legacy.
	raw := "[tool done] ls path=docs\n  目录: docs\n  统计: 0 个文件"
	if got := FormatAssistantSupplementBlock(raw); got != raw {
		t.Fatalf("multi-line known timeline should stay layout-identical, got %q", got)
	}
}

func TestStyleAssistantSupplementLine_KnownTimelineUsesDocumentRoles(t *testing.T) {
	defer func() {
		_ = SetThemePreset(ThemePresetFocus)
	}()
	t.Setenv("NO_COLOR", "")
	t.Setenv("FORCE_COLOR", "1")
	t.Setenv("AICLI_COLOR_DEPTH", "ansi16")

	theme := createTheme(ThemeDark)
	line := "[tool] view path"
	got := theme.StyleAssistantSupplementLine(line)
	if plain := render.ANSIToPlain(got); plain != line {
		t.Fatalf("known timeline changed plain text, got %q (plain %q)", got, plain)
	}
	if !strings.ContainsRune(got, '\x1b') {
		t.Fatalf("known timeline should use negotiated role colors, got %q", got)
	}
	if strings.Contains(got, "\x1b[38;2;") || strings.Contains(got, "\x1b[38;5;") {
		t.Fatalf("ANSI-16 timeline contains higher-depth color: %q", got)
	}

	// Bullet notices use Document without rewriting to [notice].
	bullet := "• Edited file.go"
	gotBullet := theme.StyleAssistantSupplementLine(bullet)
	if plain := render.ANSIToPlain(gotBullet); plain != bullet {
		t.Fatalf("bullet notice must keep marker, got %q (plain %q)", gotBullet, plain)
	}
	if strings.Contains(gotBullet, "[notice]") {
		t.Fatalf("bullet notice must not rewrite to [notice], got %q", gotBullet)
	}
	if !strings.ContainsRune(gotBullet, '\x1b') {
		t.Fatalf("bullet notice should use negotiated timeline role, got %q", gotBullet)
	}
}

func TestFormatAssistantSupplementBlock_CollapsesRedundantBlankLines(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	defer func() {
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
	t.Setenv("NO_COLOR", "")
	t.Setenv("FORCE_COLOR", "1")
	t.Setenv("AICLI_COLOR_DEPTH", "ansi16")
	defer func() {
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
	if strings.Contains(addLine, "38;5;") || strings.Contains(addLine, "38;2;") ||
		strings.Contains(delLine, "38;5;") || strings.Contains(delLine, "38;2;") {
		t.Fatalf("ANSI-16 rendering leaked high-depth SGR: add=%q del=%q", addLine, delLine)
	}
	if got := render.ANSIToPlain(addLine); got != "      "+addBody {
		t.Fatalf("added line plain projection changed: %q", got)
	}
	if got := render.ANSIToPlain(delLine); got != "      "+delBody {
		t.Fatalf("deleted line plain projection changed: %q", got)
	}
}

func TestThemePresets_AllSemanticRolesExistForEveryPaletteAndMode(t *testing.T) {
	oldName := currentThemeName
	oldMode := currentThemeMode
	oldTheme := currentTheme
	defer func() {
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

			ctx := ThemeContextForTheme(theme, style.ColorProfile{ColorProfile: render.ColorProfile{
				Enabled: true,
				Depth:   render.ColorANSI16,
			}})
			if !ctx.Palette.HasAllRequiredRoles() {
				t.Fatalf("%s/%v: semantic palette is missing required roles", palette, mode)
			}
			for _, role := range style.RequiredRoles {
				if got := ctx.Palette.StyleFor(role).Role; got != string(role) {
					t.Fatalf("%s/%v: role %q resolved as %q", palette, mode, role, got)
				}
			}
		}
	}
}

func TestThemePresets_MonoDoesNotUseSemanticChromaticStatusColors(t *testing.T) {
	for _, variant := range []style.Variant{style.VariantDark, style.VariantLight} {
		mono := style.NewPalette(ThemePresetMono, variant)
		focus := style.NewPalette(ThemePresetFocus, variant)
		for _, role := range []style.Role{style.RoleError, style.RoleSuccess, style.RoleWarning, style.RoleProgress} {
			monoStyle := mono.StyleFor(role)
			if monoStyle.Foreground.IsSet() || monoStyle.Background.IsSet() {
				t.Fatalf("mono/%v role %q leaked chromatic color: %+v", variant, role, monoStyle)
			}
			if !focus.StyleFor(role).Foreground.IsSet() {
				t.Fatalf("focus/%v role %q unexpectedly lacks chromatic baseline", variant, role)
			}
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
	oldName := currentThemeName
	oldMode := currentThemeMode
	oldTheme := currentTheme
	defer func() {
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
	classic := style.NewPalette(ThemePresetClassic, style.VariantLight)
	focus := style.NewPalette(ThemePresetFocus, style.VariantLight)

	// Classic keeps its signature magenta emphasis in the semantic palette,
	// rather than relying on removed Theme color objects.
	allSame := true
	for _, role := range []style.Role{style.RoleMetaLabel, style.RoleTool, style.RoleInfo} {
		if classic.StyleFor(role) != focus.StyleFor(role) {
			allSame = false
		}
	}
	if allSame {
		t.Fatal("classic light appears identical to base for meta/tool/info; palette overlay incomplete")
	}
	if !classic.HasAllRequiredRoles() {
		t.Fatal("classic light missing required semantic roles")
	}
}
