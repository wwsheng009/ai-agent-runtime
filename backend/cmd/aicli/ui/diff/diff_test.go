package diff

import (
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/render"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/style"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/syntax"
)

type failHighlighter struct{}

func (failHighlighter) Highlight(syntax.HighlightRequest) ([]render.Line, syntax.HighlightMeta) {
	panic("highlighter must not run after the diff preview budget is exceeded")
}

func TestParseUnifiedBasic(t *testing.T) {
	src := `diff --git a/foo.go b/foo.go
--- a/foo.go
+++ b/foo.go
@@ -1,3 +1,3 @@
 package foo
-func Old() {}
+func New() {}
 context
`
	files := ParseUnified(src, DefaultParseOptions())
	if len(files) != 1 {
		t.Fatalf("files=%d", len(files))
	}
	if files[0].NewPath != "foo.go" {
		t.Fatalf("path=%q", files[0].NewPath)
	}
	if len(files[0].Hunks) != 1 || len(files[0].Hunks[0].Lines) < 3 {
		t.Fatalf("hunks=%+v", files[0].Hunks)
	}
	var kinds []LineKind
	for _, l := range files[0].Hunks[0].Lines {
		kinds = append(kinds, l.Kind)
	}
	joined := ""
	for _, k := range kinds {
		joined += string(rune('0' + int(k)))
	}
	if !containsKind(files[0].Hunks[0].Lines, LineAdd) || !containsKind(files[0].Hunks[0].Lines, LineDelete) {
		t.Fatalf("missing add/delete: %+v", files[0].Hunks[0].Lines)
	}
}

func containsKind(lines []DiffLine, k LineKind) bool {
	for _, l := range lines {
		if l.Kind == k {
			return true
		}
	}
	return false
}

func TestParseEditedSupplement(t *testing.T) {
	src := "• Edited path/file.go (+1 -1)\n      259 -     oldValue,\n      259 +     newValue,"
	fd, ok := ParseEditedSupplement(src)
	if !ok {
		t.Fatal("parse failed")
	}
	if fd.NewPath != "path/file.go" {
		t.Fatalf("path=%q", fd.NewPath)
	}
	if len(fd.Hunks) != 1 || len(fd.Hunks[0].Lines) != 2 {
		t.Fatalf("hunk=%+v", fd.Hunks)
	}
}

func TestParseSupplementBlocksKeepsFilesAndHunksSeparate(t *testing.T) {
	src := strings.Join([]string{
		"• Edited first.go (+2 -0)",
		"        1 + package first",
		"          ...",
		"       20 + func Added() {}",
		"  ",
		"• Diff second.ts (+1 -1)",
		"        4 - const oldValue = 1",
		"        4 + const newValue = 2",
	}, "\n")

	blocks := ParseSupplementBlocks(src)
	if len(blocks) != 2 {
		t.Fatalf("blocks=%d, want 2: %+v", len(blocks), blocks)
	}
	if blocks[0].Label != "Edited" || blocks[0].Diff.NewPath != "first.go" {
		t.Fatalf("unexpected first block: %+v", blocks[0])
	}
	if len(blocks[0].Diff.Hunks) != 2 {
		t.Fatalf("first file hunks=%d, want 2", len(blocks[0].Diff.Hunks))
	}
	if blocks[1].Label != "Diff" || blocks[1].Diff.NewPath != "second.ts" {
		t.Fatalf("unexpected second block: %+v", blocks[1])
	}
	if got := len(blocks[1].Diff.Hunks[0].Lines); got != 2 {
		t.Fatalf("second file rows=%d, want 2", got)
	}
}

func TestRenderDiffReadableAllProfiles(t *testing.T) {
	fd := FileDiff{
		OldPath: "a.go",
		NewPath: "a.go",
		Hunks: []Hunk{{
			Lines: []DiffLine{
				{Kind: LineDelete, OldLineNo: 1, Text: "old"},
				{Kind: LineAdd, NewLineNo: 1, Text: "new"},
				{Kind: LineContext, OldLineNo: 2, NewLineNo: 2, Text: "keep"},
			},
		}},
	}
	profiles := []render.ColorDepth{
		render.ColorNone,
		render.ColorANSI16,
		render.ColorANSI256,
		render.ColorTrueColor,
	}
	for _, depth := range profiles {
		profile := style.ColorProfile{
			ColorProfile: render.ColorProfile{Enabled: depth != render.ColorNone, Depth: depth},
			Background:   style.BackgroundDark,
		}
		theme := style.BuildThemeContext(style.ThemeSelection{
			PaletteName: style.PaletteFocus,
			Mode:        style.ThemeModeDark,
		}, profile)
		opts := DefaultRenderOptions(80, theme)
		opts.SyntaxHighlight = false
		doc := Document(fd, opts)
		plain := render.PlainBackend{}.Render(doc)
		if !strings.Contains(plain, "+") || !strings.Contains(plain, "-") {
			t.Fatalf("depth %v missing signs: %q", depth, plain)
		}
		if !strings.Contains(plain, "old") || !strings.Contains(plain, "new") {
			t.Fatalf("depth %v lost text: %q", depth, plain)
		}
		// Encoded form must not include raw cursor CSI from content.
		out := style.RenderDocument(doc, theme)
		if strings.Contains(out, "\x1b[2J") || strings.Contains(out, "\x1b[H") {
			t.Fatalf("dangerous CSI in output depth=%v: %q", depth, out)
		}
		for _, line := range (render.PlainBackend{}).RenderLines(doc) {
			if render.Width(line) > 80 {
				t.Fatalf("overflow depth=%v line=%q", depth, line)
			}
		}
	}
}

func TestRenderTextUnifiedNoRawCSI(t *testing.T) {
	src := "--- a/x\n+++ b/x\n@@ -1 +1 @@\n-old\x1b[2J\n+new\n"
	theme := style.BuildThemeContext(style.ThemeSelection{
		PaletteName: style.PaletteFocus,
		Mode:        style.ThemeModeDark,
	}, style.ColorProfile{ColorProfile: render.TrueColorProfile()})
	doc := RenderText(src, DefaultRenderOptions(80, theme))
	plain := render.PlainBackend{}.Render(doc)
	if strings.Contains(plain, "\x1b") {
		t.Fatalf("ESC in plain: %q", plain)
	}
}

func TestANSI256DiffUsesIndexedBackground(t *testing.T) {
	theme := style.BuildThemeContext(style.ThemeSelection{
		PaletteName: style.PaletteFocus,
		Mode:        style.ThemeModeDark,
	}, style.ColorProfile{ColorProfile: render.ColorProfile{
		Enabled: true,
		Depth:   render.ColorANSI256,
	}})
	line := renderDiffLine(DiffLine{Kind: LineAdd, Text: "new"}, nil, DefaultRenderOptions(80, theme))
	found := false
	for _, span := range line.Spans {
		if span.Text == "new" && span.Style.Background.Kind == render.ColorIndexed {
			found = true
		}
	}
	if !found {
		t.Fatalf("ANSI-256 add line has no indexed background: %+v", line)
	}
}

func TestLargeDiffSkipsSyntaxHighlightingButKeepsDiffRoles(t *testing.T) {
	large := strings.Repeat("value := render(item) ", DefaultHighlightMaxBytes/10)
	fd := FileDiff{
		NewPath: "large.go",
		Hunks: []Hunk{{Lines: []DiffLine{
			{Kind: LineDelete, OldLineNo: 1, Text: large},
			{Kind: LineAdd, NewLineNo: 1, Text: large},
		}}},
	}
	theme := style.BuildThemeContext(style.ThemeSelection{
		PaletteName: style.PaletteFocus,
		Mode:        style.ThemeModeDark,
	}, style.ColorProfile{ColorProfile: render.TrueColorProfile()})
	opts := DefaultRenderOptions(80, theme)
	opts.Highlighter = failHighlighter{}
	doc := Document(fd, opts)

	plain := render.PlainBackend{}.Render(doc)
	if !strings.Contains(plain, "+ ") || !strings.Contains(plain, "- ") {
		t.Fatalf("large diff lost add/delete markers: %q", plain)
	}
	// Large content wraps into many rows; scan the whole document instead of
	// assuming a fixed line index for the first add row.
	foundAddRole := false
	foundDeleteRole := false
	for _, line := range doc.Blocks[0].Lines {
		for _, span := range line.Spans {
			switch span.Style.Role {
			case string(style.RoleSuccess):
				foundAddRole = true
			case string(style.RoleError):
				foundDeleteRole = true
			}
		}
	}
	if !foundAddRole {
		t.Fatalf("large diff lost semantic add style across %d lines", doc.LineCount())
	}
	if !foundDeleteRole {
		t.Fatalf("large diff lost semantic delete style across %d lines", doc.LineCount())
	}
}

func TestRenderTextFallbackUsesSafeStructuredLines(t *testing.T) {
	theme := style.BuildThemeContext(style.ThemeSelection{
		PaletteName: style.PaletteFocus,
		Mode:        style.ThemeModeDark,
	}, style.ColorProfile{ColorProfile: render.NoColorProfile()})
	doc := RenderText("first\nsecond\x1b[2J", DefaultRenderOptions(8, theme))
	if doc.LineCount() != 2 {
		t.Fatalf("fallback lines=%d, want 2: %+v", doc.LineCount(), doc)
	}
	if got := (render.PlainBackend{}).Render(doc); got != "first\nsecond" {
		t.Fatalf("fallback plain=%q", got)
	}
	for _, line := range doc.Blocks[0].Lines {
		if render.LineWidth(line) > 8 {
			t.Fatalf("fallback overflow: %+v", line)
		}
		for _, span := range line.Spans {
			if strings.ContainsAny(span.Text, "\r\n\x1b") {
				t.Fatalf("control/newline crossed span boundary: %q", span.Text)
			}
		}
	}
}

// TestGutterRendersSingleLineNumberColumn locks the gutter to one column.
// Context rows carry the same number on both sides, and a two-column gutter
// printed it twice ("269  269") on every unchanged row.
func TestGutterRendersSingleLineNumberColumn(t *testing.T) {
	fd := FileDiff{
		OldPath: "a.go",
		NewPath: "a.go",
		Hunks: []Hunk{{
			Lines: []DiffLine{
				{Kind: LineContext, OldLineNo: 269, NewLineNo: 269, Text: "keep"},
				{Kind: LineDelete, OldLineNo: 270, Text: "old"},
				{Kind: LineAdd, NewLineNo: 270, Text: "new"},
				{Kind: LineContext, OldLineNo: 271, NewLineNo: 271, Text: "tail"},
			},
		}},
	}
	opts := DefaultRenderOptions(80, plainThemeContext())
	opts.SyntaxHighlight = false
	rows := (render.PlainBackend{}).RenderLines(Document(fd, opts))
	if len(rows) != 5 {
		t.Fatalf("rows=%d: %q", len(rows), rows)
	}
	want := []string{
		" 269   keep",
		" 270 - old",
		" 270 + new",
		" 271   tail",
	}
	for i, expected := range want {
		if rows[i+1] != expected {
			t.Fatalf("row %d = %q, want %q (all rows: %q)", i+1, rows[i+1], expected, rows)
		}
	}
}

// TestGutterWidensForWideLineNumbers guards against the clipping fallback in
// gutter(), which would print "2345" for line 12345.
func TestGutterWidensForWideLineNumbers(t *testing.T) {
	fd := FileDiff{
		NewPath: "a.go",
		Hunks: []Hunk{{
			Lines: []DiffLine{
				{Kind: LineContext, OldLineNo: 12345, NewLineNo: 12345, Text: "keep"},
			},
		}},
	}
	opts := DefaultRenderOptions(80, plainThemeContext())
	opts.SyntaxHighlight = false
	rows := (render.PlainBackend{}).RenderLines(Document(fd, opts))
	if len(rows) != 2 || rows[1] != "12345   keep" {
		t.Fatalf("rows=%q", rows)
	}
}

func plainThemeContext() style.ThemeContext {
	return style.BuildThemeContext(style.ThemeSelection{
		PaletteName: style.PaletteFocus,
		Mode:        style.ThemeModeDark,
	}, style.ColorProfile{ColorProfile: render.NoColorProfile()})
}

// TestDiffRowBackgroundCoversFullWidth locks the add/delete tint to the whole
// terminal row: gutter, sign, code, and the empty tail after the last column.
func TestDiffRowBackgroundCoversFullWidth(t *testing.T) {
	fd := FileDiff{
		NewPath: "a.go",
		Hunks: []Hunk{{
			Lines: []DiffLine{
				{Kind: LineDelete, OldLineNo: 1, Text: "old"},
				{Kind: LineAdd, NewLineNo: 1, Text: "new"},
				{Kind: LineAdd, NewLineNo: 2, Text: ""},
				{Kind: LineContext, OldLineNo: 3, NewLineNo: 3, Text: "keep"},
			},
		}},
	}
	const width = 40
	opts := DefaultRenderOptions(width, trueColorThemeContext())
	opts.SyntaxHighlight = false
	rows := Document(fd, opts).Blocks[0].Lines
	if len(rows) != 5 {
		t.Fatalf("rows=%d", len(rows))
	}
	for i, row := range rows[1:4] {
		if got := render.LineWidth(row); got != width {
			t.Fatalf("tinted row %d width=%d, want %d", i, got, width)
		}
		if !row.Style.Background.IsSet() {
			t.Fatalf("tinted row %d has no row background: %+v", i, row.Style)
		}
		for j, span := range row.Spans {
			if !span.Style.Background.IsSet() {
				t.Fatalf("row %d span %d (%q) misses the row tint", i, j, span.Text)
			}
		}
	}
	context := rows[4]
	if context.Style.Background.IsSet() {
		t.Fatalf("context row must stay untinted: %+v", context.Style)
	}
	if got := render.LineWidth(context); got == width {
		t.Fatalf("context row padded to full width: %q", (render.PlainBackend{}).Render(render.LinesDoc(context)))
	}
}

// TestWrappedDiffRowBackgroundCoversFullWidth keeps the tint on every visual
// row of a wrapped change, including the continuation prefix and its tail.
func TestWrappedDiffRowBackgroundCoversFullWidth(t *testing.T) {
	fd := FileDiff{
		NewPath: "a.go",
		Hunks: []Hunk{{
			Lines: []DiffLine{
				{Kind: LineAdd, NewLineNo: 1, Text: strings.Repeat("value ", 20)},
			},
		}},
	}
	const width = 40
	opts := DefaultRenderOptions(width, trueColorThemeContext())
	opts.SyntaxHighlight = false
	rows := Document(fd, opts).Blocks[0].Lines
	if len(rows) < 4 {
		t.Fatalf("expected wrapped rows, got %d", len(rows))
	}
	for i, row := range rows[1:] {
		if got := render.LineWidth(row); got != width {
			t.Fatalf("wrapped row %d width=%d, want %d", i, got, width)
		}
		last := row.Spans[len(row.Spans)-1]
		if !last.Style.Background.IsSet() {
			t.Fatalf("wrapped row %d tail misses the tint: %+v", i, last)
		}
	}
}

func trueColorThemeContext() style.ThemeContext {
	return style.BuildThemeContext(style.ThemeSelection{
		PaletteName: style.PaletteFocus,
		Mode:        style.ThemeModeDark,
	}, style.ColorProfile{ColorProfile: render.TrueColorProfile()})
}
