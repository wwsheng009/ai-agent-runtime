package style

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/render"
	"gopkg.in/yaml.v3"
)

// CustomPaletteFile is the on-disk YAML schema for user palettes.
//
// Example:
//
//	name: ocean
//	variant: dark
//	roles:
//	  Tool: { fg: "#00bcd4", bold: true }
//	  Error: { fg: "#ff5252" }
type CustomPaletteFile struct {
	Name    string                     `yaml:"name"`
	Variant string                     `yaml:"variant"` // dark|light
	Roles   map[string]CustomRoleStyle `yaml:"roles"`
}

// CustomRoleStyle maps a role to color/modifiers.
type CustomRoleStyle struct {
	FG     string `yaml:"fg"`
	BG     string `yaml:"bg"`
	Bold   bool   `yaml:"bold"`
	Dim    bool   `yaml:"dim"`
	Italic bool   `yaml:"italic"`
}

const maxCustomThemeFileBytes = 64 * 1024

// LoadCustomPalettesFromDir scans dir for *.yaml / *.yml palette files.
// Invalid files are skipped with diagnostics returned separately.
func LoadCustomPalettesFromDir(dir string) (palettes []Palette, diags []string) {
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return nil, nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, []string{fmt.Sprintf("read themes dir: %v", err)}
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		lower := strings.ToLower(name)
		if strings.HasSuffix(lower, ".yaml") || strings.HasSuffix(lower, ".yml") {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	seen := map[string]struct{}{}
	for _, name := range names {
		path := filepath.Join(dir, name)
		p, err := LoadCustomPaletteFile(path)
		if err != nil {
			diags = append(diags, fmt.Sprintf("%s: %v", name, err))
			continue
		}
		key := strings.ToLower(p.Name)
		if _, ok := seen[key]; ok {
			diags = append(diags, fmt.Sprintf("%s: duplicate palette name %q", name, p.Name))
			continue
		}
		// Clash with builtins is allowed but renamed with custom- prefix warning.
		if isBuiltinPaletteName(p.Name) {
			diags = append(diags, fmt.Sprintf("%s: name %q collides with builtin; loaded as custom-%s", name, p.Name, p.Name))
			p.Name = "custom-" + p.Name
			key = strings.ToLower(p.Name)
		}
		seen[key] = struct{}{}
		palettes = append(palettes, p)
	}
	return palettes, diags
}

// LoadCustomPaletteFile parses one YAML palette file.
func LoadCustomPaletteFile(path string) (Palette, error) {
	info, err := os.Stat(path)
	if err != nil {
		return Palette{}, err
	}
	if info.Size() > maxCustomThemeFileBytes {
		return Palette{}, fmt.Errorf("file exceeds %d bytes", maxCustomThemeFileBytes)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return Palette{}, err
	}
	var file CustomPaletteFile
	if err := yaml.Unmarshal(raw, &file); err != nil {
		return Palette{}, fmt.Errorf("yaml: %w", err)
	}
	name := strings.TrimSpace(file.Name)
	if name == "" {
		base := filepath.Base(path)
		name = strings.TrimSuffix(strings.TrimSuffix(base, filepath.Ext(base)), "")
		name = strings.TrimSpace(name)
	}
	if name == "" {
		return Palette{}, fmt.Errorf("missing palette name")
	}
	variant := VariantDark
	switch strings.ToLower(strings.TrimSpace(file.Variant)) {
	case "light", "day":
		variant = VariantLight
	case "", "dark", "night":
		variant = VariantDark
	default:
		return Palette{}, fmt.Errorf("unknown variant %q", file.Variant)
	}

	// Start from focus base so missing roles still resolve.
	base := NewPalette(PaletteFocus, variant)
	styles := make(map[Role]render.Style, len(base.Styles)+len(file.Roles))
	for k, v := range base.Styles {
		styles[k] = v
	}
	for roleName, rs := range file.Roles {
		role := Role(strings.TrimSpace(roleName))
		if role == "" {
			continue
		}
		st, err := customRoleToStyle(role, rs)
		if err != nil {
			return Palette{}, fmt.Errorf("role %s: %w", roleName, err)
		}
		styles[role] = st
	}
	// Ensure required roles exist.
	for _, req := range RequiredRoles {
		if _, ok := styles[req]; !ok {
			styles[req] = base.StyleFor(req)
		}
	}
	return Palette{Name: name, Variant: variant, Styles: styles}, nil
}

func customRoleToStyle(role Role, rs CustomRoleStyle) (render.Style, error) {
	st := render.Style{Role: string(role), Bold: rs.Bold, Dim: rs.Dim, Italic: rs.Italic}
	if fg := strings.TrimSpace(rs.FG); fg != "" {
		c, err := parseHexColor(fg)
		if err != nil {
			return render.Style{}, err
		}
		st.Foreground = c
	}
	if bg := strings.TrimSpace(rs.BG); bg != "" {
		c, err := parseHexColor(bg)
		if err != nil {
			return render.Style{}, err
		}
		st.Background = c
	}
	return st, nil
}

func parseHexColor(s string) (render.Color, error) {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "#")
	if len(s) != 6 {
		return render.Color{}, fmt.Errorf("color %q must be #RRGGBB", s)
	}
	var r, g, b int
	if _, err := fmt.Sscanf(s, "%02x%02x%02x", &r, &g, &b); err != nil {
		if _, err2 := fmt.Sscanf(s, "%02X%02X%02X", &r, &g, &b); err2 != nil {
			return render.Color{}, fmt.Errorf("invalid hex color %q", s)
		}
	}
	return render.RGB(uint8(r), uint8(g), uint8(b)), nil
}

func isBuiltinPaletteName(name string) bool {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case PaletteFocus, PaletteClassic, PaletteContrast, PaletteMono:
		return true
	default:
		return false
	}
}
