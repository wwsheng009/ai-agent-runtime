package cell

import (
	"fmt"
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/render"
)

func previewPlainText(t *testing.T, res PreviewResult) string {
	t.Helper()
	return render.PlainBackend{}.Render(render.Document{
		Blocks: []render.Block{{Lines: res.Lines}},
	})
}

func numberedPreviewSource(count int) string {
	lines := make([]string, 0, count)
	for i := 0; i < count; i++ {
		lines = append(lines, fmt.Sprintf("line-%02d", i))
	}
	return strings.Join(lines, "\n")
}

// TestBuildPreviewExpandedKeepsWholeResult locks the expansion contract: an
// expanded cell drops the head/tail fold, so the display-only marker that
// dead-ends the collapsed transcript cannot reappear.
func TestBuildPreviewExpandedKeepsWholeResult(t *testing.T) {
	raw := numberedPreviewSource(60)

	collapsed := BuildPreview(raw, ToolDisplayPreviewOptions())
	if collapsed.OmittedLines == 0 {
		t.Fatalf("production display budget did not fold a 60-line result: %#v", collapsed)
	}
	collapsedText := previewPlainText(t, collapsed)
	if !strings.Contains(collapsedText, "omitted from this preview (display only)") {
		t.Fatalf("collapsed preview lost its display-scoped marker:\n%s", collapsedText)
	}

	expanded := BuildPreview(raw, PreviewOptions{Expanded: true})
	if expanded.OmittedLines != 0 || expanded.ByteTruncated {
		t.Fatalf("expanded preview still folded: omitted=%d byteTruncated=%v",
			expanded.OmittedLines, expanded.ByteTruncated)
	}
	expandedText := previewPlainText(t, expanded)
	for _, want := range []string{"line-00", "line-30", "line-59"} {
		if !strings.Contains(expandedText, want) {
			t.Fatalf("expanded preview dropped %q:\n%s", want, expandedText)
		}
	}
	if strings.Contains(expandedText, "omitted from this preview") ||
		strings.Contains(expandedText, "preview limited to the first") {
		t.Fatalf("expanded preview still emitted an omission marker:\n%s", expandedText)
	}
}

// TestBuildPreviewExpandedDropsByteCap: a result larger than the byte cap must
// survive expansion instead of being cut at the collapsed budget.
func TestBuildPreviewExpandedDropsByteCap(t *testing.T) {
	raw := strings.Repeat("x", 9000) + "\ntail-marker"

	if collapsed := BuildPreview(raw, ToolDisplayPreviewOptions()); !collapsed.ByteTruncated {
		t.Fatalf("production display budget did not apply the byte cap: %#v", collapsed)
	}

	expanded := BuildPreview(raw, PreviewOptions{Expanded: true})
	if expanded.ByteTruncated {
		t.Fatal("expanded preview kept the byte cap")
	}
	if text := previewPlainText(t, expanded); !strings.Contains(text, "tail-marker") {
		t.Fatalf("expanded preview dropped content past the byte cap:\n%s", text)
	}
}

// TestBuildPreviewHintOnlyWhenRequested keeps the interactive affordance
// caller-supplied: projections without a recovery path must not print a
// keybinding that does nothing for them.
func TestBuildPreviewHintOnlyWhenRequested(t *testing.T) {
	raw := numberedPreviewSource(10)
	opts := PreviewOptions{MaxLines: 4, HeadLines: 2, TailLines: 2}

	if text := previewPlainText(t, BuildPreview(raw, opts)); strings.Contains(text, "Ctrl+T") {
		t.Fatalf("default preview leaked an interactive hint:\n%s", text)
	}

	opts.Hint = "Ctrl+T 查看完整文本"
	text := previewPlainText(t, BuildPreview(raw, opts))
	if !strings.Contains(text, "(display only); Ctrl+T 查看完整文本") {
		t.Fatalf("marker hint missing:\n%s", text)
	}
}

// TestBuildPreviewExpandedHonoursRendererCeiling documents the one cap that
// expansion intentionally keeps: the renderer must not build an unbounded
// number of rows from a runaway dump.
func TestBuildPreviewExpandedHonoursRendererCeiling(t *testing.T) {
	raw := numberedPreviewSource(expandedMaxLines + 100)
	expanded := BuildPreview(raw, PreviewOptions{Expanded: true})
	if expanded.OmittedLines == 0 {
		t.Fatal("expanded preview ignored the renderer line ceiling")
	}
	if expanded.OmittedLines >= 1000 {
		t.Fatalf("ceiling omitted %d lines; head-only fold expected near the ceiling", expanded.OmittedLines)
	}
}
