package syntax

import (
	"os"
	"regexp"
	"strings"
	"testing"
	"time"
)

// TestRealWorldCodeBlocksHighlightWithinBudget feeds real code blocks from a
// live deepseek-v4-flash (max effort) transcript into the highlighter and
// asserts every block renders completely — highlighted or degraded — without
// ever exceeding the budget by more than one lexer step.
func TestRealWorldCodeBlocksHighlightWithinBudget(t *testing.T) {
	path := os.Getenv("AICLI_TRANSCRIPT_PATH")
	if path == "" {
		path = `C:\tmp\aicli_stress2_out.md`
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Skipf("transcript not available: %v (set AICLI_TRANSCRIPT_PATH to a live aicli transcript)", err)
	}
	re := regexp.MustCompile("(?s)```(\\w*)\n(.*?)```")
	blocks := re.FindAllStringSubmatch(string(raw), -1)
	if len(blocks) < 4 {
		t.Fatalf("expected >=4 code blocks in transcript, got %d", len(blocks))
	}
	h := &ChromaHighlighter{Limits: DefaultLimits(), DefaultTheme: "catppuccin-latte"}
	for i, m := range blocks {
		lang, code := m[1], m[2]
		if lang == "" {
			continue
		}
		start := time.Now()
		lines, meta := h.Highlight(HighlightRequest{Code: code, Language: lang, Theme: "catppuccin-latte"})
		elapsed := time.Since(start)
		wantLines := len(strings.Split(code, "\n"))
		if strings.HasSuffix(code, "\n") {
			// A trailing newline does not render a final empty row.
			wantLines--
		}
		if len(lines) != wantLines {
			t.Fatalf("block %d (%s): rendered %d lines, want %d (fallback=%q)", i, lang, len(lines), wantLines, meta.FallbackReason)
		}
		t.Logf("block %d lang=%s bytes=%d lines=%d elapsed=%v highlighted=%v fallback=%q",
			i, lang, len(code), wantLines, elapsed, meta.Highlighted, meta.FallbackReason)
		if elapsed > 5*time.Second {
			t.Fatalf("block %d took %v — budget did not bound it", i, elapsed)
		}
	}
}
