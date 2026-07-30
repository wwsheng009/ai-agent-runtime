package markdown

import (
	"fmt"
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/render"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/style"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/syntax"
)

func sampleDualEngineMarkdown() string {
	var b strings.Builder
	b.WriteString("# 长回复\n\n")
	for i := 1; i <= 3; i++ {
		fmt.Fprintf(&b, "## 章节 %d\n\n这是第 %d 段正文。\n\n- 项目 A\n- 项目 B\n\n", i, i)
	}
	b.WriteString("```go\nfunc Hello() {}\n```\n\n收尾段落。\n")
	b.WriteString("[链接](https://example.com)\n")
	return b.String()
}

func maxBlankRunPlain(s string) int {
	maxRun, run := 0, 0
	for _, line := range strings.Split(s, "\n") {
		if strings.TrimSpace(line) == "" {
			run++
			if run > maxRun {
				maxRun = run
			}
			continue
		}
		run = 0
	}
	return maxRun
}

func plainRender(source string, opts Options) string {
	return strings.TrimRight((render.PlainBackend{}).Render(Render(source, opts)), "\r\n")
}

// TestAssistantBodyEngines_SharedContractParity is the dual-engine acceptance
// gate: ActiveBand body options and Formatter-style transcript options must
// produce identical plain text and blank rhythm for the same source.
func TestAssistantBodyEngines_SharedContractParity(t *testing.T) {
	src := sampleDualEngineMarkdown()
	width := 80
	theme := style.BuildThemeContext(style.ThemeSelection{
		PaletteName: style.PaletteFocus,
		SyntaxName:  "auto",
		Mode:        style.ThemeModeDark,
	}, style.ColorProfile{ColorProfile: render.NoColorProfile()})

	// Formatter.Format path: DefaultOptions + assistant body contract.
	fmtOpts := DefaultOptions(width, theme)
	fmtOpts.SyntaxTheme = "auto"
	fmtOpts.ApplyAssistantBodyContract()

	// ActiveBand path: dedicated helper (Highlighter limits + hide fallback).
	hl := syntax.NewChromaHighlighter()
	hl.Limits = syntax.Limits{MaxBytes: 64 * 1024, MaxLines: 2000}
	bandOpts := ActiveBandBodyOptions(width, "auto", hl)

	fmtPlain := plainRender(src, fmtOpts)
	bandPlain := plainRender(src, bandOpts)

	if got, want := maxBlankRunPlain(fmtPlain), maxBlankRunPlain(bandPlain); got != want {
		t.Fatalf("blank-run drift: formatter=%d activeband=%d", got, want)
	}
	if fmtPlain != bandPlain {
		t.Fatalf("plain drift under shared assistant body contract\nformatter:\n%s\n---\nactiveband:\n%s", fmtPlain, bandPlain)
	}
	if !strings.Contains(fmtPlain, "https://example.com") {
		t.Fatalf("expected visible URL fallback in shared plain output, got %q", fmtPlain)
	}
}

// TestAssistantBodyEngines_LegacyHyperlinkDrift documents the pre-R1 production
// mismatch: Hyperlinks=true (old ActiveBand) drops the visible URL in plain
// backends while Formatter keeps it. Guard against reintroducing that split
// without updating both paths together.
func TestAssistantBodyEngines_LegacyHyperlinkDrift(t *testing.T) {
	src := sampleDualEngineMarkdown()
	width := 80
	theme := style.BuildThemeContext(style.ThemeSelection{
		PaletteName: style.PaletteFocus,
		SyntaxName:  "auto",
		Mode:        style.ThemeModeDark,
	}, style.ColorProfile{ColorProfile: render.NoColorProfile()})

	fmtOpts := DefaultOptions(width, theme)
	fmtOpts.ApplyAssistantBodyContract()

	legacyBand := ActiveBandBodyOptions(width, "auto", syntax.NewChromaHighlighter())
	legacyBand.Hyperlinks = true // old ActiveBand behavior

	fmtPlain := plainRender(src, fmtOpts)
	bandPlain := plainRender(src, legacyBand)
	if fmtPlain == bandPlain {
		t.Fatal("expected legacy Hyperlinks=true band plain to drift from transcript contract")
	}
	if maxBlankRunPlain(fmtPlain) != maxBlankRunPlain(bandPlain) {
		t.Fatalf("legacy hyperlink drift should not change blank rhythm: fmt=%d band=%d",
			maxBlankRunPlain(fmtPlain), maxBlankRunPlain(bandPlain))
	}
}
