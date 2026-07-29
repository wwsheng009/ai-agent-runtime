package markdown

import (
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/render"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/style"
	extast "github.com/yuin/goldmark/extension/ast"
	"github.com/yuin/goldmark/ast"
)

func (r *renderer) renderTable(n *extast.Table) render.Block {
	headers, rows := extractTable(n, r.src)
	if len(headers) == 0 && len(rows) == 0 {
		return render.Block{
			Kind: render.BlockTable,
			Lines: []render.Line{{
				Spans: []render.Span{{Text: "(empty table)", Style: render.Style{Role: string(style.RoleTextMuted)}}},
			}},
		}
	}

	mode := r.opts.TableMode
	if mode == TableAuto {
		mode = chooseTableMode(r.opts.Width, headers, rows)
	}

	switch mode {
	case TableRecords:
		return render.Block{Kind: render.BlockTable, Lines: renderTableRecords(headers, rows, r.opts.Width)}
	case TablePlain:
		return render.Block{Kind: render.BlockTable, Lines: renderTablePlain(headers, rows, r.opts.Width)}
	default:
		return render.Block{Kind: render.BlockTable, Lines: renderTableGrid(headers, rows, r.opts.Width)}
	}
}

func extractTable(n *extast.Table, src []byte) (headers []string, rows [][]string) {
	for c := n.FirstChild(); c != nil; c = c.NextSibling() {
		switch section := c.(type) {
		case *extast.TableHeader:
			for cell := section.FirstChild(); cell != nil; cell = cell.NextSibling() {
				if tc, ok := cell.(*extast.TableCell); ok {
					headers = append(headers, tableCellText(tc, src))
				}
			}
		case *extast.TableRow:
			var row []string
			for cell := section.FirstChild(); cell != nil; cell = cell.NextSibling() {
				if tc, ok := cell.(*extast.TableCell); ok {
					row = append(row, tableCellText(tc, src))
				}
			}
			rows = append(rows, row)
		}
	}
	// Some GFM parses put header row as first TableRow under TableHeader only;
	// also handle header cells living as TableRow inside TableHeader (already covered).
	return headers, rows
}

func tableCellText(cell *extast.TableCell, src []byte) string {
	var b strings.Builder
	ast.Walk(cell, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		if t, ok := n.(*ast.Text); ok {
			seg := t.Segment
			b.Write(seg.Value(src))
		}
		if t, ok := n.(*ast.String); ok {
			b.Write(t.Value)
		}
		if _, ok := n.(*ast.CodeSpan); ok {
			// include code span children via Text nodes
		}
		return ast.WalkContinue, nil
	})
	return strings.TrimSpace(strings.ReplaceAll(b.String(), "\n", " "))
}

func chooseTableMode(width int, headers []string, rows [][]string) TableMode {
	cols := len(headers)
	for _, row := range rows {
		if len(row) > cols {
			cols = len(row)
		}
	}
	if cols <= 1 {
		return TableGrid
	}
	if width > 0 && width < 60 {
		return TableRecords
	}
	// Estimate ideal width: sum of max cell widths + separators.
	ideal := 0
	for i := 0; i < cols; i++ {
		max := 0
		if i < len(headers) {
			max = render.Width(headers[i])
		}
		for _, row := range rows {
			if i < len(row) {
				if w := render.Width(row[i]); w > max {
					max = w
				}
			}
		}
		if max < 3 {
			max = 3
		}
		ideal += max
	}
	ideal += 3 * (cols - 1) // " | " separators
	if width > 0 && ideal > width && width < 100 {
		return TableRecords
	}
	return TableGrid
}

func renderTableGrid(headers []string, rows [][]string, width int) []render.Line {
	cols := len(headers)
	for _, row := range rows {
		if len(row) > cols {
			cols = len(row)
		}
	}
	if cols == 0 {
		return nil
	}

	// Compute ideal column widths.
	colWidths := make([]int, cols)
	for i := 0; i < cols; i++ {
		if i < len(headers) {
			colWidths[i] = render.Width(headers[i])
		}
		for _, row := range rows {
			if i < len(row) {
				if w := render.Width(row[i]); w > colWidths[i] {
					colWidths[i] = w
				}
			}
		}
		if colWidths[i] < 1 {
			colWidths[i] = 1
		}
	}

	// Fit into viewport: shrink columns proportionally if needed.
	sepW := 3 // " │ "
	totalSep := sepW * (cols - 1)
	if width > 0 {
		budget := width - totalSep
		if budget < cols {
			budget = cols
		}
		total := 0
		for _, w := range colWidths {
			total += w
		}
		if total > budget {
			// Give each column a minimum of 1, distribute rest by weight.
			remaining := budget - cols
			if remaining < 0 {
				remaining = 0
			}
			weights := make([]int, cols)
			weightSum := 0
			for i, w := range colWidths {
				weights[i] = w
				weightSum += w
			}
			for i := range colWidths {
				extra := 0
				if weightSum > 0 {
					extra = remaining * weights[i] / weightSum
				}
				colWidths[i] = 1 + extra
			}
			// Fix rounding drift on last column.
			sum := 0
			for _, w := range colWidths {
				sum += w
			}
			if sum < budget {
				colWidths[cols-1] += budget - sum
			}
		}
	}

	var lines []render.Line
	if len(headers) > 0 {
		lines = append(lines, gridRowLine(padRow(headers, cols), colWidths, true))
		lines = append(lines, gridSeparatorLine(colWidths))
	}
	for _, row := range rows {
		// Cell content may need multi-line wrap within column width.
		cellLines := wrapRowCells(padRow(row, cols), colWidths)
		for _, cl := range cellLines {
			lines = append(lines, gridRowLine(cl, colWidths, false))
		}
	}
	// Final safety: truncate any line still over width.
	if width > 0 {
		for i := range lines {
			if render.LineWidth(lines[i]) > width {
				lines[i] = render.Truncate(lines[i], width, "…")
			}
		}
	}
	return lines
}

func padRow(row []string, cols int) []string {
	out := make([]string, cols)
	for i := 0; i < cols; i++ {
		if i < len(row) {
			out[i] = row[i]
		}
	}
	return out
}

func wrapRowCells(cells []string, colWidths []int) [][]string {
	// Wrap each cell independently; return row-slices for each visual line.
	wrapped := make([][]string, len(cells))
	maxLines := 1
	for i, cell := range cells {
		w := colWidths[i]
		if w < 1 {
			w = 1
		}
		parts := wrapCell(cell, w)
		wrapped[i] = parts
		if len(parts) > maxLines {
			maxLines = len(parts)
		}
	}
	rows := make([][]string, maxLines)
	for li := 0; li < maxLines; li++ {
		row := make([]string, len(cells))
		for ci := range cells {
			if li < len(wrapped[ci]) {
				row[ci] = wrapped[ci][li]
			}
		}
		rows[li] = row
	}
	return rows
}

func wrapCell(text string, width int) []string {
	if width <= 0 || render.Width(text) <= width {
		return []string{text}
	}
	line := render.Line{Spans: []render.Span{{Text: text}}}
	wrapped := render.Wrap(line, width, render.WrapOptions{BreakWord: true})
	out := make([]string, 0, len(wrapped))
	for _, l := range wrapped {
		var b strings.Builder
		for _, sp := range l.Spans {
			b.WriteString(sp.Text)
		}
		out = append(out, b.String())
	}
	if len(out) == 0 {
		return []string{""}
	}
	return out
}

func gridRowLine(cells []string, colWidths []int, header bool) render.Line {
	var spans []render.Span
	role := style.RoleTextPrimary
	if header {
		role = style.RoleAccent
	}
	for i, cell := range cells {
		if i > 0 {
			spans = append(spans, render.Span{
				Text:  " │ ",
				Style: render.Style{Role: string(style.RoleBorder), Dim: true},
			})
		}
		w := colWidths[i]
		text := cell
		if render.Width(text) > w {
			text = render.TruncateText(text, w, "…")
		} else if render.Width(text) < w {
			text = text + strings.Repeat(" ", w-render.Width(text))
		}
		st := render.Style{Role: string(role)}
		if header {
			st.Bold = true
		}
		spans = append(spans, render.Span{Text: text, Style: st})
	}
	return render.Line{Spans: spans}
}

func gridSeparatorLine(colWidths []int) render.Line {
	var spans []render.Span
	for i, w := range colWidths {
		if i > 0 {
			spans = append(spans, render.Span{
				Text:  "─┼─",
				Style: render.Style{Role: string(style.RoleBorder), Dim: true},
			})
		}
		spans = append(spans, render.Span{
			Text:  strings.Repeat("─", w),
			Style: render.Style{Role: string(style.RoleBorder), Dim: true},
		})
	}
	return render.Line{Spans: spans}
}

func renderTableRecords(headers []string, rows [][]string, width int) []render.Line {
	var lines []render.Line
	for ri, row := range rows {
		if ri > 0 {
			lines = append(lines, render.Line{Spans: []render.Span{{
				Text:  strings.Repeat("─", min(width, 40)),
				Style: render.Style{Role: string(style.RoleBorder), Dim: true},
			}}})
		}
		cols := len(row)
		if len(headers) > cols {
			cols = len(headers)
		}
		for ci := 0; ci < cols; ci++ {
			key := ""
			if ci < len(headers) {
				key = headers[ci]
			} else {
				key = "col"
			}
			val := ""
			if ci < len(row) {
				val = row[ci]
			}
			keySpan := render.Span{
				Text:  key + ": ",
				Style: render.Style{Role: string(style.RoleAccent), Bold: true},
			}
			valSpan := render.Span{
				Text:  val,
				Style: render.Style{Role: string(style.RoleTextPrimary)},
			}
			line := render.Line{Spans: []render.Span{keySpan, valSpan}}
			if width > 0 && render.LineWidth(line) > width {
				for _, wl := range render.Wrap(line, width, render.WrapOptions{BreakWord: true}) {
					lines = append(lines, wl)
				}
			} else {
				lines = append(lines, line)
			}
		}
	}
	// Header-only table
	if len(rows) == 0 && len(headers) > 0 {
		for _, h := range headers {
			lines = append(lines, render.Line{Spans: []render.Span{{
				Text:  h,
				Style: render.Style{Role: string(style.RoleAccent), Bold: true},
			}}})
		}
	}
	return lines
}

func renderTablePlain(headers []string, rows [][]string, width int) []render.Line {
	join := func(cells []string) string {
		return strings.Join(cells, " | ")
	}
	var lines []render.Line
	if len(headers) > 0 {
		text := join(headers)
		if width > 0 {
			text = render.TruncateText(text, width, "…")
		}
		lines = append(lines, render.Line{Spans: []render.Span{{
			Text: text, Style: render.Style{Role: string(style.RoleAccent), Bold: true},
		}}})
	}
	for _, row := range rows {
		text := join(row)
		if width > 0 {
			text = render.TruncateText(text, width, "…")
		}
		lines = append(lines, render.Line{Spans: []render.Span{{
			Text: text, Style: render.Style{Role: string(style.RoleTextPrimary)},
		}}})
	}
	return lines
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
