package style

import (
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/render"
)

// SeparatorKind selects the fill character family.
type SeparatorKind int

const (
	SeparatorRegular SeparatorKind = iota
	SeparatorThin
	SeparatorThick
	SeparatorDashed
	SeparatorDouble
)

// SeparatorModel is structured input for a horizontal rule.
type SeparatorModel struct {
	Kind    SeparatorKind
	Width   int
	Title   string
	Padding int
	// Fill overrides the default glyph when non-empty.
	Fill string
}

// SeparatorDocument builds a single-line muted separator.
func SeparatorDocument(model SeparatorModel) render.Document {
	width := model.Width
	if width <= 0 {
		width = 80
	}
	fill := model.Fill
	if fill == "" {
		switch model.Kind {
		case SeparatorThick:
			fill = "═"
		case SeparatorDashed:
			fill = "-"
		case SeparatorDouble:
			fill = "═"
		default:
			fill = "─"
		}
	}

	var text string
	title := model.Title
	if title == "" {
		text = strings.Repeat(fill, width)
	} else {
		// Use cell width for title, not byte length.
		titleWidth := render.Width(title)
		pad := model.Padding
		if titleWidth+2*pad >= width {
			text = title
		} else {
			left := (width - titleWidth - 2*pad) / 2
			right := width - titleWidth - 2*pad - left
			text = strings.Repeat(fill, left) +
				strings.Repeat(" ", pad) +
				title +
				strings.Repeat(" ", pad) +
				strings.Repeat(fill, right)
		}
	}
	return render.SingleLineDoc(render.Span{
		Text:  text,
		Style: render.Style{Role: string(RoleBorder)},
	})
}
