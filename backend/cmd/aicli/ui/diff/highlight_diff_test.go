package diff

import (
	"strings"
	"testing"

	"github.com/alecthomas/chroma/v2"
	"github.com/alecthomas/chroma/v2/styles"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/render"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/style"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/syntax"
)

// mismatchHighlighter reports success but returns the wrong number of rows,
// simulating a lexer whose line count drifts from the hunk.
type mismatchHighlighter struct{}

func (mismatchHighlighter) Highlight(syntax.HighlightRequest) ([]render.Line, syntax.HighlightMeta) {
	return []render.Line{{Spans: []render.Span{{
		Text:  "only-one-row",
		Style: render.Style{Foreground: render.RGB(1, 2, 3)},
	}}}}, syntax.HighlightMeta{Highlighted: true}
}

type recordingHighlighter struct {
	languages []string
}

func (h *recordingHighlighter) Highlight(req syntax.HighlightRequest) ([]render.Line, syntax.HighlightMeta) {
	h.languages = append(h.languages, req.Language)
	parts := strings.Split(strings.TrimSuffix(req.Code, "\n"), "\n")
	lines := make([]render.Line, 0, len(parts))
	for _, part := range parts {
		lines = append(lines, render.Line{Spans: []render.Span{{
			Text:  part,
			Style: render.Style{Role: "Probe." + req.Language, Foreground: render.RGB(1, 2, 3)},
		}}})
	}
	return lines, syntax.HighlightMeta{Language: req.Language, Highlighted: true}
}

func testThemeContext(depth render.ColorDepth, syntaxName string) style.ThemeContext {
	return style.BuildThemeContext(style.ThemeSelection{
		PaletteName: style.PaletteFocus,
		SyntaxName:  syntaxName,
		Mode:        style.ThemeModeDark,
	}, style.ColorProfile{
		ColorProfile: render.ColorProfile{Enabled: depth != render.ColorNone, Depth: depth},
		Background:   style.BackgroundDark,
	})
}

func TestHighlightHunkRejectsRowCountMismatch(t *testing.T) {
	hunk := Hunk{Lines: []DiffLine{
		{Kind: LineDelete, OldLineNo: 1, Text: `fmt.Println("a")`},
		{Kind: LineAdd, NewLineNo: 1, Text: `fmt.Println("b")`},
		{Kind: LineContext, OldLineNo: 2, NewLineNo: 2, Text: "return nil"},
	}}
	opts := DefaultRenderOptions(80, testThemeContext(render.ColorTrueColor, "monokai"))
	opts.Highlighter = mismatchHighlighter{}

	tokens := highlightHunk(hunk, "go", opts)
	if len(tokens) != len(hunk.Lines) {
		t.Fatalf("token rows=%d, want %d", len(tokens), len(hunk.Lines))
	}
	for i, spans := range tokens {
		if len(spans) != 1 {
			t.Fatalf("row %d not a single plain span: %+v", i, spans)
		}
		if spans[0].Text != hunk.Lines[i].Text {
			t.Fatalf("row %d text=%q, want %q", i, spans[0].Text, hunk.Lines[i].Text)
		}
		if spans[0].Style.Foreground.IsSet() {
			t.Fatalf("row %d kept token color after mismatch: %+v", i, spans[0])
		}
	}
}

func TestHighlightHunkKeepsTokenColorsBeforeTrailingBlankRow(t *testing.T) {
	hunk := Hunk{Lines: []DiffLine{
		{Kind: LineAdd, NewLineNo: 1, Text: "package sample"},
		{Kind: LineAdd, NewLineNo: 2, Text: ""},
	}}
	opts := DefaultRenderOptions(80, testThemeContext(render.ColorTrueColor, "monokai"))
	tokens := highlightHunk(hunk, "go", opts)

	if len(tokens) != 2 {
		t.Fatalf("token rows=%d, want 2", len(tokens))
	}
	foundColor := false
	for _, span := range tokens[0] {
		if span.Style.Foreground.IsSet() {
			foundColor = true
			break
		}
	}
	if !foundColor {
		t.Fatalf("trailing blank row disabled preceding token colors: %+v", tokens)
	}
	if got := (render.PlainBackend{}).Render(render.LinesDoc(
		render.Line{Spans: tokens[0]},
		render.Line{Spans: tokens[1]},
	)); got != "package sample\n" {
		t.Fatalf("unexpected highlighted projection: %q", got)
	}
}

func TestSupplementDocumentHighlightsEachFileWithItsOwnLanguage(t *testing.T) {
	blocks := ParseSupplementBlocks(strings.Join([]string{
		"• Edited first.go (+1 -0)",
		"        1 + package first",
		"• Edited second.ts (+1 -0)",
		"        1 + const value = 1",
	}, "\n"))
	recorder := &recordingHighlighter{}
	opts := DefaultRenderOptions(80, testThemeContext(render.ColorTrueColor, "monokai"))
	opts.Highlighter = recorder
	// Each file is below this budget, while their combined source is not.
	// A lossy multi-file adapter would skip both highlighter calls.
	opts.HighlightLimits = syntax.Limits{MaxBytes: 20, MaxLines: 2}
	doc := SupplementDocument(blocks, opts)

	if got := strings.Join(recorder.languages, ","); got != "go,typescript" {
		t.Fatalf("highlight languages=%q, want go,typescript", got)
	}
	plain := render.PlainBackend{}.Render(doc)
	for _, want := range []string{"• Edited first.go", "package first", "• Edited second.ts", "const value = 1"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("multi-file projection missing %q: %q", want, plain)
		}
	}
}

func TestWrapKeepsTokenStylesAndGutterAlignment(t *testing.T) {
	const width = 40
	source := `value := renderMessage(surface, errorDocument("失败"), fallbackStyle)`
	fd := FileDiff{
		NewPath: "wrap.go",
		Hunks: []Hunk{{Lines: []DiffLine{
			{Kind: LineAdd, NewLineNo: 12, Text: source},
		}}},
	}
	doc := Document(fd, DefaultRenderOptions(width, testThemeContext(render.ColorTrueColor, "monokai")))

	lines := doc.Blocks[0].Lines
	// Header + at least two wrapped content rows.
	if len(lines) < 3 {
		t.Fatalf("expected wrapped rows, got %d lines: %+v", len(lines), lines)
	}
	for i, line := range lines {
		if got := render.LineWidth(line); got > width {
			t.Fatalf("line %d width=%d exceeds %d", i, got, width)
		}
	}

	content := lines[1:]
	prefixWidth := render.SpanWidth(content[0].Spans[0]) + render.SpanWidth(content[0].Spans[1])
	styledContinuations := 0
	var rebuilt strings.Builder
	for rowIndex, line := range content {
		skip := 1
		if rowIndex == 0 {
			skip = 2
		}
		if got := render.SpanWidth(line.Spans[0]); got != prefixWidth && rowIndex > 0 {
			t.Fatalf("continuation row %d prefix width=%d, want %d", rowIndex, got, prefixWidth)
		}
		for _, span := range line.Spans[skip:] {
			rebuilt.WriteString(span.Text)
			if rowIndex > 0 && span.Style.Foreground.IsSet() {
				styledContinuations++
			}
		}
	}
	if styledContinuations == 0 {
		t.Fatal("wrapped continuation rows lost all token colors")
	}
	stripSpaces := func(s string) string { return strings.ReplaceAll(s, " ", "") }
	if got, want := stripSpaces(rebuilt.String()), stripSpaces(source); got != want {
		t.Fatalf("wrapped content lost characters:\n got %q\nwant %q", got, want)
	}
}

func TestThemeScopeBackgroundsOverridePalette(t *testing.T) {
	const themeName = "aicli-diff-scope-test"
	styles.Register(chroma.MustNewStyle(themeName, chroma.StyleEntries{
		chroma.Background:      "#f8f8f2 bg:#272822",
		chroma.GenericInserted: "bg:#112233",
		chroma.GenericDeleted:  "bg:#445566",
	}))

	trueColor := DefaultRenderOptions(80, testThemeContext(render.ColorTrueColor, themeName))
	got := resolveDiffBackgrounds(trueColor)
	if want := render.RGB(0x11, 0x22, 0x33); got.add != want {
		t.Fatalf("truecolor add bg=%+v, want %+v", got.add, want)
	}
	if want := render.RGB(0x44, 0x55, 0x66); got.del != want {
		t.Fatalf("truecolor del bg=%+v, want %+v", got.del, want)
	}

	indexed := DefaultRenderOptions(80, testThemeContext(render.ColorANSI256, themeName))
	got = resolveDiffBackgrounds(indexed)
	if want := render.Indexed(render.RGBToANSI256(0x11, 0x22, 0x33)); got.add != want {
		t.Fatalf("ansi256 add bg=%+v, want %+v", got.add, want)
	}

	ansi16 := DefaultRenderOptions(80, testThemeContext(render.ColorANSI16, themeName))
	if got = resolveDiffBackgrounds(ansi16); got.add.IsSet() || got.del.IsSet() {
		t.Fatalf("ansi16 must stay background-free: %+v", got)
	}
}

func TestThemeWithoutDiffScopesKeepsPaletteTint(t *testing.T) {
	opts := DefaultRenderOptions(80, testThemeContext(render.ColorTrueColor, "monokai"))
	got := resolveDiffBackgrounds(opts)
	if want := addBackground(opts.Theme); got.add != want {
		t.Fatalf("add bg=%+v, want palette %+v", got.add, want)
	}
	if want := delBackground(opts.Theme); got.del != want {
		t.Fatalf("del bg=%+v, want palette %+v", got.del, want)
	}
}

func TestParseEditedSupplementKeepsContextAndSplitsSeparatorHunk(t *testing.T) {
	src := strings.Join([]string{
		`• Edited internal/service.go (+1 -1)`,
		`       10   unchanged`,
		`       11 - old`,
		`       11 + new`,
		`          ...`,
		`       30   -1 offset`,
	}, "\n")

	fd, ok := ParseEditedSupplement(src)
	if !ok {
		t.Fatal("parse failed")
	}
	if len(fd.Hunks) != 2 {
		t.Fatalf("hunks=%d, want 2: %+v", len(fd.Hunks), fd.Hunks)
	}
	lines := append(append([]DiffLine{}, fd.Hunks[0].Lines...), fd.Hunks[1].Lines...)
	if len(lines) != 5 {
		t.Fatalf("rows=%d, want 5: %+v", len(lines), lines)
	}

	want := []struct {
		kind LineKind
		text string
	}{
		{LineContext, "unchanged"},
		{LineDelete, "old"},
		{LineAdd, "new"},
		{LineMeta, hunkSeparatorText},
		// Content starting with '-' must stay context, not become a deletion.
		{LineContext, "-1 offset"},
	}
	for i, expect := range want {
		if lines[i].Kind != expect.kind {
			t.Fatalf("row %d kind=%v, want %v", i, lines[i].Kind, expect.kind)
		}
		if lines[i].Text != expect.text {
			t.Fatalf("row %d text=%q, want %q", i, lines[i].Text, expect.text)
		}
	}
	if lines[0].OldLineNo != 10 || lines[0].NewLineNo != 10 {
		t.Fatalf("context row numbers=%d/%d, want 10/10", lines[0].OldLineNo, lines[0].NewLineNo)
	}
	if lines[1].OldLineNo != 11 || lines[1].NewLineNo != 0 {
		t.Fatalf("delete row numbers=%d/%d, want 11/0", lines[1].OldLineNo, lines[1].NewLineNo)
	}
	if lines[2].OldLineNo != 0 || lines[2].NewLineNo != 11 {
		t.Fatalf("add row numbers=%d/%d, want 0/11", lines[2].OldLineNo, lines[2].NewLineNo)
	}
}

func TestDiffContentExpandsTabs(t *testing.T) {
	fd := FileDiff{
		NewPath: "tabs.txt",
		Hunks: []Hunk{{Lines: []DiffLine{
			{Kind: LineContext, OldLineNo: 1, NewLineNo: 1, Text: "a\tb"},
		}}},
	}
	opts := DefaultRenderOptions(80, testThemeContext(render.ColorTrueColor, "monokai"))
	opts.SyntaxHighlight = false
	plain := render.PlainBackend{}.Render(Document(fd, opts))
	if strings.ContainsRune(plain, '\t') {
		t.Fatalf("tab survived into rendered output: %q", plain)
	}
	if !strings.Contains(plain, "a   b") {
		t.Fatalf("tab not expanded to the next stop: %q", plain)
	}
}

// TestEveryRowFitsFrameWidth locks the invariant that made the document-wide
// width clamp removable: appendDiffRow and clipDiffLine are the only places a
// row is measured, so every emitted row must already fit.
func TestEveryRowFitsFrameWidth(t *testing.T) {
	fd := FileDiff{
		OldPath: "internal/very/deeply/nested/legacy_renderer.go",
		NewPath: "internal/very/deeply/nested/structured_renderer.go",
		Hunks: []Hunk{{
			Header: "@@ -18,4 +18,4 @@ func renderMessage(surface Surface) error",
			Lines: []DiffLine{
				{Kind: LineDelete, OldLineNo: 18, Text: `fmt.Print(color.RedString("失败: %v", err))`},
				{Kind: LineAdd, NewLineNo: 18, Text: `surface.Write(errorDocument("失败: %v", err))`},
				{Kind: LineContext, OldLineNo: 19, NewLineNo: 19, Text: "\t\treturn nil"},
				{Kind: LineMeta, Text: hunkSeparatorText},
			},
		}},
	}

	// 10 leaves no usable content column (prefix alone is 12 wide) and must
	// still clip rather than emit one grapheme per row.
	for _, width := range []int{10, 14, 24, 40, 80, 200} {
		opts := DefaultRenderOptions(width, testThemeContext(render.ColorTrueColor, "monokai"))
		doc := Document(fd, opts)
		rows := 0
		for _, block := range doc.Blocks {
			for i, line := range block.Lines {
				rows++
				if got := render.LineWidth(line); got > width {
					t.Fatalf("width=%d row %d is %d cells wide: %q", width, i, got,
						render.PlainBackend{}.Render(render.LinesDoc(line)))
				}
			}
		}
		if rows == 0 {
			t.Fatalf("width=%d produced no rows", width)
		}
	}
}
