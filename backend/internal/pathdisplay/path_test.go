package pathdisplay

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestFileUsesRelativePathWithoutDroppingFilename(t *testing.T) {
	base := t.TempDir()
	filename := strings.Repeat("very-long-component-", 4) + "settings.generated.tsx"
	absPath := filepath.Join(base, "apps", "portal-modern", "src", filename)

	key, got := File(map[string]interface{}{
		"file_path": absPath,
		"workdir":   base,
	})
	want := filepath.Join("apps", "portal-modern", "src", filename)
	if key != "file_path" || got != want {
		t.Fatalf("unexpected display path: key=%q got=%q want=%q", key, got, want)
	}
	if !strings.HasSuffix(got, filename) || strings.Contains(got, "...") {
		t.Fatalf("filename was not preserved: %q", got)
	}
	if !NeedsOwnLine(got) {
		t.Fatalf("expected long path on its own line: %q", got)
	}
}

func TestRelativeKeepsPathOutsideWorkingDirectory(t *testing.T) {
	base := t.TempDir()
	outside := filepath.Join(filepath.Dir(base), "outside", "file.go")
	if got := Relative(outside, base); got != outside {
		t.Fatalf("outside path must stay absolute: got=%q want=%q", got, outside)
	}
}
