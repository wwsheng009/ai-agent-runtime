package executor

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestPrependPathDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "rgtools")
	env := []string{
		"HOME=/home/u",
		"PATH=" + strings.Join([]string{"/usr/bin", "/bin"}, string(os.PathListSeparator)),
	}
	prepared := PrependPathDir(env, dir)
	got := envPathEntry(t, prepared)
	if !strings.HasPrefix(got, dir+string(os.PathListSeparator)) {
		t.Fatalf("expected %q prepended, got %q", dir, got)
	}
	if !strings.Contains(got, "/usr/bin") || !strings.Contains(got, "/bin") {
		t.Fatalf("expected original PATH preserved, got %q", got)
	}
	if !strings.Contains(strings.Join(prepared, "\n"), "HOME=/home/u") {
		t.Fatal("unrelated env entries must be preserved")
	}

	// Already present: no reorder, no duplicate.
	again := PrependPathDir(prepared, dir)
	if envPathEntry(t, again) != got {
		t.Fatalf("second prepend must be a no-op, got %q", envPathEntry(t, again))
	}

	// Empty dir is a no-op.
	if out := PrependPathDir(env, "  "); envPathEntry(t, out) != envPathEntry(t, env) {
		t.Fatal("empty dir must not change PATH")
	}

	// Missing PATH entry is appended.
	out := PrependPathDir([]string{"HOME=/home/u"}, dir)
	if envPathEntry(t, out) != dir {
		t.Fatalf("expected PATH appended as %q, got %q", dir, envPathEntry(t, out))
	}

	if runtime.GOOS == "windows" {
		upper := strings.ToUpper(dir)
		upperEnv := []string{"PATH=" + upper + ";" + `C:\bin`}
		if got := envPathEntry(t, PrependPathDir(upperEnv, dir)); got != upper+`;C:\bin` {
			t.Fatalf("case-insensitive duplicate must not be reordered, got %q", got)
		}
	}
}

func TestWithResolvedRipgrepPathPrependsResolverDir(t *testing.T) {
	rgDir := t.TempDir()
	rgName := "rg"
	if runtime.GOOS == "windows" {
		rgName = "rg.exe"
	}
	rgPath := writeRipgrepFixture(t, filepath.Join(rgDir, rgName))
	basePath := t.TempDir()
	t.Setenv("AICLI_RG_PATH", rgPath)
	t.Setenv("PATH", basePath)

	env := WithResolvedRipgrepPath([]string{"PATH=" + basePath})
	got := envPathEntry(t, env)
	if !strings.HasPrefix(got, rgDir+string(os.PathListSeparator)) {
		t.Fatalf("expected resolver dir %q prepended, got %q", rgDir, got)
	}
	if !strings.Contains(got, basePath) {
		t.Fatalf("original PATH must be preserved, got %q", got)
	}

	// Idempotent on a second pass.
	again := WithResolvedRipgrepPath(env)
	if envPathEntry(t, again) != got {
		t.Fatalf("second pass must be a no-op, got %q", envPathEntry(t, again))
	}

	// A non-canonical override cannot be aliased through PATH.
	oddDir := t.TempDir()
	oddName := "rg-alt"
	if runtime.GOOS == "windows" {
		oddName = "rg-alt.exe"
	}
	t.Setenv("AICLI_RG_PATH", writeRipgrepFixture(t, filepath.Join(oddDir, oddName)))
	if out := WithResolvedRipgrepPath([]string{"PATH=" + basePath}); envPathEntry(t, out) != basePath {
		t.Fatalf("non-canonical rg must not change PATH, got %q", envPathEntry(t, out))
	}

	// Nothing resolvable: unchanged.
	t.Setenv("AICLI_RG_PATH", "")
	if out := WithResolvedRipgrepPath([]string{"PATH=" + basePath}); envPathEntry(t, out) != basePath {
		t.Fatalf("unresolvable rg must not change PATH, got %q", envPathEntry(t, out))
	}
}

func writeRipgrepFixture(t *testing.T, path string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte("rg"), 0o755); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		t.Fatalf("abs: %v", err)
	}
	return abs
}

func envPathEntry(t *testing.T, env []string) string {
	t.Helper()
	for _, entry := range env {
		parts := strings.SplitN(entry, "=", 2)
		if len(parts) == 2 && strings.EqualFold(parts[0], "PATH") {
			return parts[1]
		}
	}
	t.Fatalf("PATH entry missing in %#v", env)
	return ""
}
