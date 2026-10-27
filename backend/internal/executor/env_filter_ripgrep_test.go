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

func TestNeutralizeConsoleStylingEnvForcesNoColorBaseline(t *testing.T) {
	env := []string{
		"HOME=/home/u",
		"PATH=/usr/bin:/bin",
		// Host values that would otherwise enable styling in a captured pipe.
		"TERM=xterm-256color",
		"COLORTERM=truecolor",
		"NO_COLOR=",
		"PAGER=less",
		"GIT_PAGER=less",
		"CLICOLOR=1",
		"CLICOLOR_FORCE=1",
	}
	out := NeutralizeConsoleStylingEnv(env)

	want := map[string]string{
		"NO_COLOR":       "1",
		"TERM":           "dumb",
		"COLORTERM":      "",
		"PAGER":          "cat",
		"GIT_PAGER":      "cat",
		"GH_PAGER":       "cat",
		"CLICOLOR":       "",
		"CLICOLOR_FORCE": "",
	}
	got := map[string]string{}
	for _, entry := range out {
		parts := strings.SplitN(entry, "=", 2)
		if len(parts) != 2 {
			continue
		}
		if _, ok := want[parts[0]]; ok {
			got[parts[0]] = parts[1]
		}
	}
	for k, v := range want {
		if got[k] != v {
			t.Fatalf("neutralized %s = %q, want %q (env=%#v)", k, got[k], v, out)
		}
	}
	// Each styling var must appear exactly once (forced value wins, not appended).
	for k := range want {
		count := 0
		for _, entry := range out {
			if strings.HasPrefix(entry, k+"=") {
				count++
			}
		}
		if count != 1 {
			t.Fatalf("expected exactly one %s entry, got %d (env=%#v)", k, count, out)
		}
	}
	// Unrelated variables must survive intact.
	if joined := strings.Join(out, "\n"); !strings.Contains(joined, "HOME=/home/u") || !strings.Contains(joined, "PATH=/usr/bin:/bin") {
		t.Fatalf("unrelated env entries dropped: %#v", out)
	}
}

func TestNeutralizeConsoleStylingEnvIdempotentAndPreservesValues(t *testing.T) {
	// Stress: applying twice (e.g. shellexec.Run + a nested wrapper) must not
	// grow the environment or flip values.
	first := NeutralizeConsoleStylingEnv([]string{"A=1"})
	second := NeutralizeConsoleStylingEnv(first)
	if strings.Join(second, "\x00") != strings.Join(first, "\x00") {
		t.Fatalf("not idempotent:\n first=%#v\nsecond=%#v", first, second)
	}
	// Forced styling vars are present with their neutralized values and are
	// unique (forced value wins, not appended).
	want := map[string]string{
		"NO_COLOR": "1", "TERM": "dumb", "COLORTERM": "", "PAGER": "cat",
		"GIT_PAGER": "cat", "GH_PAGER": "cat", "CLICOLOR": "", "CLICOLOR_FORCE": "",
	}
	for k, v := range want {
		count, gotV := 0, ""
		for _, entry := range second {
			if strings.HasPrefix(entry, k+"=") {
				count++
				gotV = strings.TrimPrefix(entry, k+"=")
			}
		}
		if count != 1 {
			t.Fatalf("expected exactly one %s entry, got %d in %#v", k, count, second)
		}
		if gotV != v {
			t.Fatalf("neutralized %s = %q, want %q in %#v", k, gotV, v, second)
		}
	}
	// User variables that do not collide are preserved.
	if !containsEntry(second, "A=1") {
		t.Fatalf("preserved user var A=1 dropped: %#v", second)
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

func containsEntry(env []string, entry string) bool {
	for _, e := range env {
		if e == entry {
			return true
		}
	}
	return false
}
