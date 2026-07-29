package render

import (
	"fmt"
	"strconv"
	"strings"
)

// ColorDepth describes terminal color capability.
type ColorDepth int

const (
	ColorNone ColorDepth = iota
	ColorANSI16
	ColorANSI256
	ColorTrueColor
)

// ColorProfile captures runtime terminal color capability.
// It is pure data; detection lives in the style/terminal packages.
type ColorProfile struct {
	Enabled    bool
	Depth      ColorDepth
	Hyperlinks bool
	// Forced marks an explicit user override (diagnostic).
	Forced bool
}

// NoColorProfile disables all styling.
func NoColorProfile() ColorProfile {
	return ColorProfile{Enabled: false, Depth: ColorNone}
}

// TrueColorProfile enables full RGB output.
func TrueColorProfile() ColorProfile {
	return ColorProfile{Enabled: true, Depth: ColorTrueColor, Hyperlinks: true}
}

// ANSIBackend encodes a Document into safe SGR (and optional OSC 8) sequences.
// Only SGR styles and OSC 8 hyperlinks are emitted. No cursor/title/clear codes.
type ANSIBackend struct {
	Profile ColorProfile
}

// Render encodes the document as a multi-line ANSI string.
func (b ANSIBackend) Render(doc Document) string {
	lines := b.RenderLines(doc)
	return strings.Join(lines, "\n")
}

// RenderLines encodes each visual line independently, always resetting SGR.
func (b ANSIBackend) RenderLines(doc Document) []string {
	out := make([]string, 0, doc.LineCount())
	for _, block := range doc.Blocks {
		for _, line := range block.Lines {
			out = append(out, b.renderLine(line))
		}
	}
	return out
}

func (b ANSIBackend) renderLine(line Line) string {
	if len(line.Spans) == 0 {
		return ""
	}
	if !b.Profile.Enabled || b.Profile.Depth == ColorNone {
		var plain strings.Builder
		for _, span := range line.Spans {
			plain.WriteString(sanitizeSpanText(span.Text))
		}
		return plain.String()
	}

	var buf strings.Builder
	for _, span := range line.Spans {
		if span.Text == "" && span.Link == "" {
			continue
		}
		style := EffectiveSpanStyle(line.Style, span.Style)
		open, close := b.encodeStyle(style)
		linkOpen, linkClose := b.encodeLink(span.Link)
		buf.WriteString(linkOpen)
		buf.WriteString(open)
		buf.WriteString(sanitizeSpanText(span.Text))
		buf.WriteString(close)
		buf.WriteString(linkClose)
	}
	return buf.String()
}

func (b ANSIBackend) encodeLink(link string) (open, close string) {
	if !b.Profile.Hyperlinks || !safeHyperlink(link) {
		return "", ""
	}
	// OSC 8 ;; url ST ... OSC 8 ;; ST
	return "\x1b]8;;" + link + "\x1b\\", "\x1b]8;;\x1b\\"
}

func (b ANSIBackend) encodeStyle(style Style) (open, close string) {
	params := b.styleParams(style)
	if len(params) == 0 {
		return "", ""
	}
	var bld strings.Builder
	bld.WriteString("\x1b[")
	for i, p := range params {
		if i > 0 {
			bld.WriteByte(';')
		}
		bld.WriteString(strconv.Itoa(p))
	}
	bld.WriteByte('m')
	return bld.String(), "\x1b[0m"
}

func (b ANSIBackend) styleParams(style Style) []int {
	var params []int
	if style.Bold {
		params = append(params, 1)
	}
	if style.Dim {
		params = append(params, 2)
	}
	if style.Italic {
		params = append(params, 3)
	}
	if style.Underline {
		params = append(params, 4)
	}
	if style.Reverse {
		params = append(params, 7)
	}
	params = append(params, b.colorParams(38, style.Foreground)...)
	// Background is conservative on ANSI-16: skip custom backgrounds.
	if style.Background.IsSet() {
		switch b.Profile.Depth {
		case ColorTrueColor, ColorANSI256:
			params = append(params, b.colorParams(48, style.Background)...)
		case ColorANSI16:
			// Only allow reverse as background substitute; skip RGB/indexed bg.
		}
	}
	return params
}

// colorParams emits SGR color parameters for fg (38) or bg (48).
func (b ANSIBackend) colorParams(lead int, c Color) []int {
	if !c.IsSet() {
		return nil
	}
	switch b.Profile.Depth {
	case ColorNone:
		return nil
	case ColorANSI16:
		return ansi16Params(lead, c)
	case ColorANSI256:
		idx := colorTo256(c)
		return []int{lead, 5, int(idx)}
	default: // TrueColor
		switch c.Kind {
		case ColorRGB:
			return []int{lead, 2, int(c.R), int(c.G), int(c.B)}
		case ColorIndexed:
			return []int{lead, 5, int(c.Index)}
		case ColorANSI:
			return ansi16Params(lead, c)
		default:
			return nil
		}
	}
}

func ansi16Params(lead int, c Color) []int {
	idx := colorToANSI16(c)
	// Map lead 38/48 to classic set/set-bg codes.
	if lead == 38 {
		if idx >= 8 {
			return []int{90 + int(idx-8)}
		}
		return []int{30 + int(idx)}
	}
	if lead == 48 {
		if idx >= 8 {
			return []int{100 + int(idx-8)}
		}
		return []int{40 + int(idx)}
	}
	return nil
}

// colorTo256 quantizes any color to an xterm 256 index.
func colorTo256(c Color) uint8 {
	switch c.Kind {
	case ColorIndexed:
		return c.Index
	case ColorANSI:
		return c.Index % 16
	case ColorRGB:
		return rgbToANSI256(c.R, c.G, c.B)
	default:
		return 0
	}
}

// colorToANSI16 maps to the classic 16-color palette.
func colorToANSI16(c Color) uint8 {
	switch c.Kind {
	case ColorANSI:
		return c.Index % 16
	case ColorIndexed:
		if c.Index < 16 {
			return c.Index
		}
		// Approximate via RGB of the 256-color cube entry.
		r, g, b := ansi256ToRGB(c.Index)
		return rgbToANSI16(r, g, b)
	case ColorRGB:
		return rgbToANSI16(c.R, c.G, c.B)
	default:
		return 7
	}
}

// rgbToANSI256 uses the standard 6x6x6 cube + grayscale ramp.
func rgbToANSI256(r, g, b uint8) uint8 {
	// Prefer grayscale ramp when channel variance is low.
	if max3(r, g, b)-min3(r, g, b) < 8 {
		gray := (int(r) + int(g) + int(b)) / 3
		if gray < 8 {
			return 16
		}
		if gray > 238 {
			return 231
		}
		return uint8(232 + (gray-8)/10)
	}
	return uint8(16 + 36*toCube(r) + 6*toCube(g) + toCube(b))
}

// RGBToANSI256 exposes the backend quantizer so producers that must emit a
// concrete xterm index in the IR (for example diff backgrounds on 256-color
// terminals) stay consistent with what the ANSI backend would encode.
func RGBToANSI256(r, g, b uint8) uint8 {
	return rgbToANSI256(r, g, b)
}

func toCube(v uint8) int {
	if v < 48 {
		return 0
	}
	if v < 115 {
		return 1
	}
	return int((v - 35) / 40)
}

func rgbToANSI16(r, g, b uint8) uint8 {
	// Map to the brightest of the classic 16 by simple distance.
	palette := [][3]uint8{
		{0, 0, 0},       // 0 black
		{128, 0, 0},     // 1 red
		{0, 128, 0},     // 2 green
		{128, 128, 0},   // 3 yellow
		{0, 0, 128},     // 4 blue
		{128, 0, 128},   // 5 magenta
		{0, 128, 128},   // 6 cyan
		{192, 192, 192}, // 7 white
		{128, 128, 128}, // 8 bright black
		{255, 0, 0},     // 9 bright red
		{0, 255, 0},     // 10 bright green
		{255, 255, 0},   // 11 bright yellow
		{0, 0, 255},     // 12 bright blue
		{255, 0, 255},   // 13 bright magenta
		{0, 255, 255},   // 14 bright cyan
		{255, 255, 255}, // 15 bright white
	}
	best := uint8(7)
	bestDist := int(^uint(0) >> 1)
	for i, p := range palette {
		dr := int(r) - int(p[0])
		dg := int(g) - int(p[1])
		db := int(b) - int(p[2])
		dist := dr*dr + dg*dg + db*db
		if dist < bestDist {
			bestDist = dist
			best = uint8(i)
		}
	}
	return best
}

func ansi256ToRGB(index uint8) (uint8, uint8, uint8) {
	if index < 16 {
		palette := [][3]uint8{
			{0, 0, 0}, {128, 0, 0}, {0, 128, 0}, {128, 128, 0},
			{0, 0, 128}, {128, 0, 128}, {0, 128, 128}, {192, 192, 192},
			{128, 128, 128}, {255, 0, 0}, {0, 255, 0}, {255, 255, 0},
			{0, 0, 255}, {255, 0, 255}, {0, 255, 255}, {255, 255, 255},
		}
		p := palette[index]
		return p[0], p[1], p[2]
	}
	if index >= 232 {
		v := uint8(8 + (int(index)-232)*10)
		return v, v, v
	}
	i := int(index) - 16
	r := cubeLevel(i / 36)
	g := cubeLevel((i % 36) / 6)
	b := cubeLevel(i % 6)
	return r, g, b
}

func cubeLevel(v int) uint8 {
	if v <= 0 {
		return 0
	}
	return uint8(55 + v*40)
}

func min3(a, b, c uint8) uint8 {
	if a <= b && a <= c {
		return a
	}
	if b <= c {
		return b
	}
	return c
}

func max3(a, b, c uint8) uint8 {
	if a >= b && a >= c {
		return a
	}
	if b >= c {
		return b
	}
	return c
}

// StyledGolden formats a document as a human-reviewable styled dump.
// Example:
//
//	line 0:
//	  [Success bold] "Ready"
//	  [TextMuted] " · model"
func StyledGolden(doc Document) string {
	var b strings.Builder
	lineNo := 0
	for _, block := range doc.Blocks {
		for _, line := range block.Lines {
			fmt.Fprintf(&b, "line %d:\n", lineNo)
			if len(line.Spans) == 0 {
				b.WriteString("  (empty)\n")
			}
			for _, span := range line.Spans {
				style := EffectiveSpanStyle(line.Style, span.Style)
				b.WriteString("  [")
				b.WriteString(styleLabel(style))
				b.WriteString(`] "`)
				b.WriteString(span.Text)
				b.WriteString(`"`)
				if span.Link != "" {
					b.WriteString(" link=")
					b.WriteString(span.Link)
				}
				b.WriteByte('\n')
			}
			lineNo++
		}
	}
	return b.String()
}

func styleLabel(s Style) string {
	parts := make([]string, 0, 4)
	if s.Role != "" {
		parts = append(parts, s.Role)
	}
	if s.Bold {
		parts = append(parts, "bold")
	}
	if s.Dim {
		parts = append(parts, "dim")
	}
	if s.Italic {
		parts = append(parts, "italic")
	}
	if s.Underline {
		parts = append(parts, "underline")
	}
	if s.Reverse {
		parts = append(parts, "reverse")
	}
	if s.Foreground.IsSet() && s.Role == "" {
		parts = append(parts, fmt.Sprintf("fg=%s", colorLabel(s.Foreground)))
	}
	if s.Background.IsSet() {
		parts = append(parts, fmt.Sprintf("bg=%s", colorLabel(s.Background)))
	}
	if len(parts) == 0 {
		return "Plain"
	}
	return strings.Join(parts, " ")
}

func colorLabel(c Color) string {
	switch c.Kind {
	case ColorANSI:
		return fmt.Sprintf("ansi(%d)", c.Index)
	case ColorIndexed:
		return fmt.Sprintf("idx(%d)", c.Index)
	case ColorRGB:
		return fmt.Sprintf("#%02x%02x%02x", c.R, c.G, c.B)
	default:
		return "default"
	}
}
