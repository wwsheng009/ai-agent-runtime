package style

import "github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/render"

// Variant is the light/dark axis of a palette.
type Variant int

const (
	VariantDark Variant = iota
	VariantLight
)

// PaletteName constants match existing aicli product palettes.
const (
	PaletteFocus    = "focus"
	PaletteClassic  = "classic"
	PaletteContrast = "contrast"
	PaletteMono     = "mono"
)

// Palette is pure semantic style data. It must not contain Sprint/writer
// or environment probes.
type Palette struct {
	Name    string
	Variant Variant
	Styles  map[Role]render.Style
}

// ThemeMode is the user preference for light/dark selection.
type ThemeMode string

const (
	ThemeModeAuto  ThemeMode = "auto"
	ThemeModeLight ThemeMode = "light"
	ThemeModeDark  ThemeMode = "dark"
)

// ThemeSelection is the durable user choice across the three axes.
type ThemeSelection struct {
	PaletteName string
	SyntaxName  string
	Mode        ThemeMode
}

// ThemeContext is injected into renderers. Immutable after resolution.
type ThemeContext struct {
	Palette      Palette
	SyntaxName   string
	Terminal     ColorProfile
	UseHyperlink bool
}

// StyleFor returns the concrete style for a role, falling back to TextPrimary.
func (p Palette) StyleFor(role Role) render.Style {
	if p.Styles == nil {
		return render.Style{Role: string(role)}
	}
	if s, ok := p.Styles[role]; ok {
		out := s
		if out.Role == "" {
			out.Role = string(role)
		}
		return out
	}
	if s, ok := p.Styles[RoleTextPrimary]; ok {
		out := s
		out.Role = string(role)
		return out
	}
	return render.Style{Role: string(role)}
}

// HasAllRequiredRoles reports whether the palette covers RequiredRoles.
func (p Palette) HasAllRequiredRoles() bool {
	if p.Styles == nil {
		return false
	}
	for _, role := range RequiredRoles {
		if _, ok := p.Styles[role]; !ok {
			return false
		}
	}
	return true
}

// BuiltinPaletteNames returns stable product palette identifiers.
func BuiltinPaletteNames() []string {
	return []string{PaletteFocus, PaletteClassic, PaletteContrast, PaletteMono}
}

// NewPalette builds a palette for name+variant. Unknown names fall back to focus.
func NewPalette(name string, variant Variant) Palette {
	name = normalizePaletteName(name)
	switch name {
	case PaletteClassic:
		return classicPalette(variant)
	case PaletteContrast:
		return contrastPalette(variant)
	case PaletteMono:
		return monoPalette(variant)
	default:
		return focusPalette(variant)
	}
}

func normalizePaletteName(name string) string {
	switch stringsToLower(name) {
	case PaletteClassic, "default":
		return PaletteClassic
	case PaletteContrast, "high-contrast", "highcontrast":
		return PaletteContrast
	case PaletteMono, "monochrome":
		return PaletteMono
	case PaletteFocus, "":
		return PaletteFocus
	default:
		return PaletteFocus
	}
}

func stringsToLower(s string) string {
	b := make([]byte, len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 'A' && c <= 'Z' {
			c += 'a' - 'A'
		}
		b[i] = c
	}
	return string(b)
}

func focusPalette(v Variant) Palette {
	if v == VariantLight {
		return Palette{
			Name:    PaletteFocus,
			Variant: v,
			Styles: map[Role]render.Style{
				RoleTextPrimary:   {Role: string(RoleTextPrimary)},
				RoleTextSecondary: {Foreground: render.ANSI(0), Role: string(RoleTextSecondary)},
				RoleTextMuted:     {Foreground: render.ANSI(8), Dim: true, Role: string(RoleTextMuted)},
				RoleAccent:        {Foreground: render.ANSI(4), Bold: true, Role: string(RoleAccent)},
				RoleUser:          {Foreground: render.ANSI(4), Bold: true, Role: string(RoleUser)},
				RoleAssistant:     {Foreground: render.ANSI(0), Role: string(RoleAssistant)},
				RoleSystem:        {Foreground: render.ANSI(3), Role: string(RoleSystem)},
				RoleTool:          {Foreground: render.ANSI(4), Bold: true, Role: string(RoleTool)},
				RoleReasoning:     {Foreground: render.ANSI(3), Role: string(RoleReasoning)},
				RoleApproval:      {Foreground: render.ANSI(5), Bold: true, Role: string(RoleApproval)},
				RoleInfo:          {Foreground: render.ANSI(4), Role: string(RoleInfo)},
				RoleSuccess:       {Foreground: render.ANSI(2), Bold: true, Role: string(RoleSuccess)},
				RoleWarning:       {Foreground: render.ANSI(3), Bold: true, Role: string(RoleWarning)},
				RoleError:         {Foreground: render.ANSI(1), Bold: true, Role: string(RoleError)},
				RoleLink:          {Foreground: render.ANSI(6), Underline: true, Role: string(RoleLink)},
				RoleBorder:        {Foreground: render.ANSI(8), Role: string(RoleBorder)},
				RoleSelection:     {Reverse: true, Bold: true, Role: string(RoleSelection)},
				RoleCodeInline:    {Foreground: render.ANSI(5), Bold: true, Role: string(RoleCodeInline)},
				RoleCommand:       {Foreground: render.ANSI(5), Role: string(RoleCommand)},
				RoleMetaLabel:     {Foreground: render.ANSI(8), Role: string(RoleMetaLabel)},
				RoleTimeline:      {Foreground: render.ANSI(8), Role: string(RoleTimeline)},
				RoleProgress:      {Foreground: render.ANSI(2), Role: string(RoleProgress)},
			},
		}
	}
	return Palette{
		Name:    PaletteFocus,
		Variant: v,
		Styles: map[Role]render.Style{
			RoleTextPrimary:   {Role: string(RoleTextPrimary)},
			RoleTextSecondary: {Foreground: render.ANSI(7), Role: string(RoleTextSecondary)},
			RoleTextMuted:     {Foreground: render.ANSI(8), Dim: true, Role: string(RoleTextMuted)},
			RoleAccent:        {Foreground: render.ANSI(14), Bold: true, Role: string(RoleAccent)},
			RoleUser:          {Foreground: render.ANSI(14), Bold: true, Role: string(RoleUser)},
			// Assistant body uses default terminal foreground (not solid green).
			RoleAssistant:  {Role: string(RoleAssistant)},
			RoleSystem:     {Foreground: render.ANSI(11), Role: string(RoleSystem)},
			RoleTool:       {Foreground: render.ANSI(14), Bold: true, Role: string(RoleTool)},
			RoleReasoning:  {Foreground: render.ANSI(11), Role: string(RoleReasoning)},
			RoleApproval:   {Foreground: render.ANSI(13), Bold: true, Role: string(RoleApproval)},
			RoleInfo:       {Foreground: render.ANSI(12), Role: string(RoleInfo)},
			RoleSuccess:    {Foreground: render.ANSI(10), Bold: true, Role: string(RoleSuccess)},
			RoleWarning:    {Foreground: render.ANSI(11), Bold: true, Role: string(RoleWarning)},
			RoleError:      {Foreground: render.ANSI(9), Bold: true, Role: string(RoleError)},
			RoleLink:       {Foreground: render.ANSI(14), Underline: true, Role: string(RoleLink)},
			RoleBorder:     {Foreground: render.ANSI(8), Role: string(RoleBorder)},
			RoleSelection:  {Reverse: true, Bold: true, Role: string(RoleSelection)},
			RoleCodeInline: {Foreground: render.ANSI(13), Role: string(RoleCodeInline)},
			RoleCommand:    {Foreground: render.ANSI(13), Role: string(RoleCommand)},
			RoleMetaLabel:  {Foreground: render.ANSI(8), Role: string(RoleMetaLabel)},
			RoleTimeline:   {Foreground: render.ANSI(8), Role: string(RoleTimeline)},
			RoleProgress:   {Foreground: render.ANSI(10), Role: string(RoleProgress)},
		},
	}
}

func classicPalette(v Variant) Palette {
	p := focusPalette(v)
	p.Name = PaletteClassic
	if v == VariantDark {
		p.Styles[RoleAssistant] = render.Style{Role: string(RoleAssistant)}
		p.Styles[RoleUser] = render.Style{Foreground: render.ANSI(14), Bold: true, Role: string(RoleUser)}
		p.Styles[RoleSystem] = render.Style{Foreground: render.ANSI(11), Role: string(RoleSystem)}
		p.Styles[RoleCommand] = render.Style{Foreground: render.ANSI(5), Role: string(RoleCommand)}
		p.Styles[RoleMetaLabel] = render.Style{Foreground: render.ANSI(5), Role: string(RoleMetaLabel)}
		p.Styles[RoleTool] = render.Style{Foreground: render.ANSI(5), Role: string(RoleTool)}
		p.Styles[RoleInfo] = render.Style{Foreground: render.ANSI(5), Role: string(RoleInfo)}
		p.Styles[RoleApproval] = render.Style{Foreground: render.ANSI(11), Bold: true, Role: string(RoleApproval)}
	} else {
		p.Styles[RoleAssistant] = render.Style{Role: string(RoleAssistant)}
		p.Styles[RoleUser] = render.Style{Foreground: render.ANSI(4), Bold: true, Role: string(RoleUser)}
		p.Styles[RoleSystem] = render.Style{Foreground: render.ANSI(3), Role: string(RoleSystem)}
		p.Styles[RoleCommand] = render.Style{Foreground: render.ANSI(5), Role: string(RoleCommand)}
		p.Styles[RoleMetaLabel] = render.Style{Foreground: render.ANSI(5), Role: string(RoleMetaLabel)}
		p.Styles[RoleTool] = render.Style{Foreground: render.ANSI(5), Role: string(RoleTool)}
		p.Styles[RoleInfo] = render.Style{Foreground: render.ANSI(5), Role: string(RoleInfo)}
		p.Styles[RoleApproval] = render.Style{Foreground: render.ANSI(3), Bold: true, Role: string(RoleApproval)}
	}
	return p
}

func contrastPalette(v Variant) Palette {
	p := focusPalette(v)
	p.Name = PaletteContrast
	// High-contrast: force bold on key statuses, prefer bright ANSI.
	for _, role := range []Role{RoleSuccess, RoleError, RoleWarning, RoleApproval, RoleTool, RoleUser, RoleSystem} {
		s := p.Styles[role]
		s.Bold = true
		p.Styles[role] = s
	}
	p.Styles[RoleTextMuted] = render.Style{Foreground: render.ANSI(7), Role: string(RoleTextMuted)}
	return p
}

func monoPalette(v Variant) Palette {
	// Mono avoids chromatic hues; rely on weight/dim/reverse.
	styles := map[Role]render.Style{
		RoleTextPrimary:   {Role: string(RoleTextPrimary)},
		RoleTextSecondary: {Role: string(RoleTextSecondary)},
		RoleTextMuted:     {Dim: true, Role: string(RoleTextMuted)},
		RoleAccent:        {Bold: true, Role: string(RoleAccent)},
		RoleUser:          {Bold: true, Role: string(RoleUser)},
		RoleAssistant:     {Role: string(RoleAssistant)},
		RoleSystem:        {Role: string(RoleSystem)},
		RoleTool:          {Bold: true, Role: string(RoleTool)},
		RoleReasoning:     {Dim: true, Role: string(RoleReasoning)},
		RoleApproval:      {Bold: true, Role: string(RoleApproval)},
		RoleInfo:          {Role: string(RoleInfo)},
		RoleSuccess:       {Bold: true, Role: string(RoleSuccess)},
		RoleWarning:       {Bold: true, Role: string(RoleWarning)},
		RoleError:         {Bold: true, Reverse: true, Role: string(RoleError)},
		RoleLink:          {Underline: true, Role: string(RoleLink)},
		RoleBorder:        {Dim: true, Role: string(RoleBorder)},
		RoleSelection:     {Reverse: true, Bold: true, Role: string(RoleSelection)},
		RoleCodeInline:    {Reverse: true, Role: string(RoleCodeInline)},
		RoleCommand:       {Bold: true, Role: string(RoleCommand)},
		RoleMetaLabel:     {Dim: true, Role: string(RoleMetaLabel)},
		RoleTimeline:      {Dim: true, Role: string(RoleTimeline)},
		RoleProgress:      {Bold: true, Role: string(RoleProgress)},
	}
	_ = v
	return Palette{Name: PaletteMono, Variant: v, Styles: styles}
}
