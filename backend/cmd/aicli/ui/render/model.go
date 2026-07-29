// Package render provides a structured intermediate representation for terminal UI.
// Styles stay as data until a backend materializes ANSI or plain text.
package render

// Document is a top-level render tree composed of blocks.
type Document struct {
	Blocks []Block
}

// BlockKind classifies a block for layout policies.
type BlockKind int

const (
	BlockParagraph BlockKind = iota
	BlockCode
	BlockQuote
	BlockList
	BlockTable
	BlockRule
	BlockStatus
	BlockCustom
	// BlockSpacer is vertical whitespace produced by the layout stage.
	// Content renderers never create it directly; ApplyBlockSpacing does.
	BlockSpacer
)

// Block is a vertical unit that contains one or more lines.
type Block struct {
	Kind         BlockKind
	Lines        []Line
	KeepWithNext bool
}

// Line is a single visual row made of styled spans.
// Newlines belong to line boundaries, never inside Span.Text.
type Line struct {
	Spans []Span
	Style Style
}

// Span is an atomic styled text fragment. Text must not contain ESC/OSC/CSI.
type Span struct {
	Text  string
	Style Style
	Link  string
}

// Style is a structured presentation hint. Backends decide encoding.
type Style struct {
	Foreground Color
	Background Color
	Bold       bool
	Dim        bool
	Italic     bool
	Underline  bool
	Reverse    bool
	// Role is an optional semantic tag retained for styled golden tests.
	// Backends ignore Role when encoding; ThemeResolver maps Role -> concrete Style.
	Role string
}

// ColorKind identifies how a color is specified.
type ColorKind int

const (
	// ColorDefault means "use terminal default" (no SGR color).
	ColorDefault ColorKind = iota
	// ColorANSI is a classic 0-15 ANSI index.
	ColorANSI
	// ColorIndexed is an xterm 256-color index (0-255).
	ColorIndexed
	// ColorRGB is a 24-bit truecolor value.
	ColorRGB
)

// Color is a backend-neutral color value.
type Color struct {
	Kind  ColorKind
	Index uint8
	R     uint8
	G     uint8
	B     uint8
}

// Empty reports whether the color is the zero/default value.
func (c Color) Empty() bool {
	return c.Kind == ColorDefault && c.Index == 0 && c.R == 0 && c.G == 0 && c.B == 0
}

// IsSet reports whether an explicit color was provided.
func (c Color) IsSet() bool {
	return c.Kind != ColorDefault
}

// RGB constructs a truecolor value.
func RGB(r, g, b uint8) Color {
	return Color{Kind: ColorRGB, R: r, G: g, B: b}
}

// ANSI constructs a classic 16-color value.
func ANSI(index uint8) Color {
	return Color{Kind: ColorANSI, Index: index}
}

// Indexed constructs an xterm 256-color value.
func Indexed(index uint8) Color {
	return Color{Kind: ColorIndexed, Index: index}
}

// DefaultColor is the terminal default foreground/background.
func DefaultColor() Color {
	return Color{Kind: ColorDefault}
}

// Size is a measured width/height in terminal cells.
type Size struct {
	Width  int
	Height int
}

// Constraints describe the available layout space.
type Constraints struct {
	Width   int
	Height  int
	Compact bool
}

// TextSpan builds an unstyled span.
func TextSpan(text string) Span {
	return Span{Text: text}
}

// StyledSpan builds a span with an explicit style.
func StyledSpan(text string, style Style) Span {
	return Span{Text: text, Style: style}
}

// RoleSpan builds a span tagged with a semantic role name.
func RoleSpan(text, role string) Span {
	return Span{Text: text, Style: Style{Role: role}}
}

// SingleLineDoc builds a one-line document.
func SingleLineDoc(spans ...Span) Document {
	return Document{
		Blocks: []Block{{
			Kind:  BlockParagraph,
			Lines: []Line{{Spans: spans}},
		}},
	}
}

// LinesDoc builds a multi-line paragraph document.
func LinesDoc(lines ...Line) Document {
	return Document{
		Blocks: []Block{{
			Kind:  BlockParagraph,
			Lines: lines,
		}},
	}
}

// PlainText extracts visible text with newlines between lines.
func (d Document) PlainText() string {
	return PlainBackend{}.Render(d)
}

// LineCount returns the number of visual lines in the document.
func (d Document) LineCount() int {
	n := 0
	for _, b := range d.Blocks {
		n += len(b.Lines)
	}
	return n
}

// CloneStyle returns a copy of style.
func CloneStyle(s Style) Style {
	return s
}
