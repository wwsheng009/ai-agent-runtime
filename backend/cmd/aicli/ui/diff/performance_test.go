package diff

import (
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/render"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/style"
)

func BenchmarkParseUnified100KiB(b *testing.B) {
	source := benchmarkUnifiedDiff(100 * 1024)
	b.ReportAllocs()
	b.SetBytes(int64(len(source)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = ParseUnified(source, DefaultParseOptions())
	}
}

func BenchmarkRenderDiff100KiB(b *testing.B) {
	source := benchmarkUnifiedDiff(100 * 1024)
	files := ParseUnified(source, DefaultParseOptions())
	if len(files) != 1 {
		b.Fatalf("files=%d, want 1", len(files))
	}
	theme := style.BuildThemeContext(style.ThemeSelection{
		PaletteName: style.PaletteFocus,
		Mode:        style.ThemeModeDark,
	}, style.ColorProfile{ColorProfile: render.TrueColorProfile()})
	opts := DefaultRenderOptions(100, theme)
	b.ReportAllocs()
	b.SetBytes(int64(len(source)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = Document(files[0], opts)
	}
}

func benchmarkUnifiedDiff(targetBytes int) string {
	var source strings.Builder
	source.Grow(targetBytes + 256)
	source.WriteString("diff --git a/large.go b/large.go\n")
	source.WriteString("--- a/large.go\n+++ b/large.go\n")
	source.WriteString("@@ -1,4000 +1,4000 @@\n")
	for line := 1; source.Len() < targetBytes; line++ {
		switch line % 3 {
		case 0:
			source.WriteString("-oldValue := renderLegacy(item) // removed row\n")
		case 1:
			source.WriteString("+newValue := renderStructured(item) // added row\n")
		default:
			source.WriteString(" contextValue := keep(item) // unchanged row\n")
		}
	}
	return source.String()
}
