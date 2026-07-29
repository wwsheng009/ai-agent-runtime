package render

import "strings"

// PlainBackend projects a document to plain text without any control sequences.
type PlainBackend struct{}

// Render returns visible text only. Styles and links are discarded.
func (PlainBackend) Render(doc Document) string {
	var b strings.Builder
	firstBlock := true
	for _, block := range doc.Blocks {
		if !firstBlock {
			b.WriteByte('\n')
		}
		firstBlock = false
		for i, line := range block.Lines {
			if i > 0 {
				b.WriteByte('\n')
			}
			for _, span := range line.Spans {
				b.WriteString(sanitizeSpanText(span.Text))
			}
		}
	}
	return b.String()
}

// RenderLines returns one plain string per visual line.
func (PlainBackend) RenderLines(doc Document) []string {
	var lines []string
	for _, block := range doc.Blocks {
		for _, line := range block.Lines {
			var b strings.Builder
			for _, span := range line.Spans {
				b.WriteString(sanitizeSpanText(span.Text))
			}
			lines = append(lines, b.String())
		}
	}
	return lines
}
