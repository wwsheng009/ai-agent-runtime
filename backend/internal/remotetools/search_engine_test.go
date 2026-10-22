package remotetools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/toolcore/searchcore"
)

func newSearchFixture(t *testing.T) (*Config, *Workspace) {
	t.Helper()
	root := t.TempDir()
	for path, content := range map[string]string{
		"visible.txt":           "needle visible\n",
		".hidden.txt":           "needle hidden\n",
		".gitignore":            "ignored.txt\n",
		"ignored.txt":           "needle ignored but deliberately included\n",
		"node_modules/skip.txt": "needle dependency\n",
		"nested/also.txt":       "needle nested\n",
	} {
		full := filepath.Join(root, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	cfg := DefaultConfig()
	cfg.Workspace = root
	if err := cfg.Normalize(); err != nil {
		t.Fatal(err)
	}
	workspace, err := NewWorkspace(cfg.Workspace, cfg.AllowedRoots)
	if err != nil {
		t.Fatal(err)
	}
	return cfg, workspace
}

func TestRemoteGrepEnginesPreserveWalkerSemantics(t *testing.T) {
	cfg, workspace := newSearchFixture(t)
	engines := []struct {
		name string
		rg   ripgrepRunner
	}{
		{name: "walker"},
		{name: "ripgrep", rg: resolveRipgrepRunner()},
	}
	for _, engine := range engines {
		t.Run(engine.name, func(t *testing.T) {
			if engine.name == "ripgrep" && !engine.rg.available() {
				t.Skip("ripgrep is not available")
			}
			tool := newRemoteGrepTool(cfg, workspace)
			tool.rg = engine.rg
			result, err := tool.Execute(context.Background(), map[string]interface{}{
				"pattern": "needle",
				"path":    ".",
			})
			if err != nil {
				t.Fatal(err)
			}
			if !result.Success {
				t.Fatalf("grep failed: %+v", result)
			}
			for _, want := range []string{"visible.txt", ".hidden.txt", "ignored.txt", "nested"} {
				if !strings.Contains(result.Content, want) {
					t.Fatalf("%s grep output missing %q:\n%s", engine.name, want, result.Content)
				}
			}
			if strings.Contains(result.Content, "node_modules") {
				t.Fatalf("%s grep must skip node_modules:\n%s", engine.name, result.Content)
			}
			if got := result.Metadata["engine"]; got != engine.name {
				t.Fatalf("engine = %v, want %s", got, engine.name)
			}
			if got := result.Metadata["hidden_files"]; got != true {
				t.Fatalf("hidden_files = %v, want true", got)
			}
			if got := result.Metadata["ignore_files"]; got != false {
				t.Fatalf("ignore_files = %v, want false", got)
			}
		})
	}
}

func TestRemoteGlobEnginesPreserveWalkerSemantics(t *testing.T) {
	cfg, workspace := newSearchFixture(t)
	engines := []struct {
		name string
		rg   ripgrepRunner
	}{
		{name: "walker"},
		{name: "ripgrep", rg: resolveRipgrepRunner()},
	}
	for _, engine := range engines {
		t.Run(engine.name, func(t *testing.T) {
			if engine.name == "ripgrep" && !engine.rg.available() {
				t.Skip("ripgrep is not available")
			}
			tool := newRemoteGlobTool(cfg, workspace)
			tool.rg = engine.rg
			result, err := tool.Execute(context.Background(), map[string]interface{}{
				"pattern": "**/*.txt",
			})
			if err != nil {
				t.Fatal(err)
			}
			if !result.Success {
				t.Fatalf("glob failed: %+v", result)
			}
			for _, want := range []string{"visible.txt", ".hidden.txt", "ignored.txt", "nested"} {
				if !strings.Contains(result.Content, want) {
					t.Fatalf("%s glob output missing %q:\n%s", engine.name, want, result.Content)
				}
			}
			if strings.Contains(result.Content, "node_modules") {
				t.Fatalf("%s glob must skip node_modules:\n%s", engine.name, result.Content)
			}
			if got := result.Metadata["engine"]; got != engine.name {
				t.Fatalf("engine = %v, want %s", got, engine.name)
			}
			if got := result.Metadata["hidden_files"]; got != true {
				t.Fatalf("hidden_files = %v, want true", got)
			}
			if got := result.Metadata["ignore_files"]; got != false {
				t.Fatalf("ignore_files = %v, want false", got)
			}
		})
	}
}

func TestRemoteSearchLimitTruncates(t *testing.T) {
	cfg, workspace := newSearchFixture(t)
	engines := []struct {
		name string
		rg   ripgrepRunner
	}{
		{name: "walker"},
		{name: "ripgrep", rg: resolveRipgrepRunner()},
	}
	for _, engine := range engines {
		if engine.name == "ripgrep" && !engine.rg.available() {
			continue
		}
		t.Run(engine.name, func(t *testing.T) {
			grep := newRemoteGrepTool(cfg, workspace)
			grep.rg = engine.rg
			result, err := grep.Execute(context.Background(), map[string]interface{}{
				"pattern": "needle",
				"path":    ".",
				"limit":   1,
			})
			if err != nil {
				t.Fatal(err)
			}
			if got := result.Metadata["matches"]; got != 1 {
				t.Fatalf("grep matches = %v, want 1", got)
			}
			if got := result.Metadata["is_truncated"]; got != true {
				t.Fatalf("grep is_truncated = %v, want true", got)
			}

			glob := newRemoteGlobTool(cfg, workspace)
			glob.rg = engine.rg
			result, err = glob.Execute(context.Background(), map[string]interface{}{
				"pattern": "**/*.txt",
				"limit":   1,
			})
			if err != nil {
				t.Fatal(err)
			}
			if got := result.Metadata["matches"]; got != 1 {
				t.Fatalf("glob matches = %v, want 1", got)
			}
			if got := result.Metadata["is_truncated"]; got != true {
				t.Fatalf("glob is_truncated = %v, want true", got)
			}
		})
	}
}

func TestRemoteSearchCallerGlobCannotReincludeSkippedDirs(t *testing.T) {
	cfg, workspace := newSearchFixture(t)
	engines := []struct {
		name string
		rg   ripgrepRunner
	}{
		{name: "walker"},
		{name: "ripgrep", rg: resolveRipgrepRunner()},
	}
	for _, engine := range engines {
		if engine.name == "ripgrep" && !engine.rg.available() {
			continue
		}
		t.Run(engine.name, func(t *testing.T) {
			grep := newRemoteGrepTool(cfg, workspace)
			grep.rg = engine.rg
			result, err := grep.Execute(context.Background(), map[string]interface{}{
				"pattern": "needle",
				"glob":    "**/skip.txt",
			})
			if err != nil {
				t.Fatal(err)
			}
			if got := result.Metadata["matches"]; got != 0 {
				t.Fatalf("grep matches = %v, want 0:\n%s", got, result.Content)
			}

			glob := newRemoteGlobTool(cfg, workspace)
			glob.rg = engine.rg
			result, err = glob.Execute(context.Background(), map[string]interface{}{
				"pattern": "**/skip.txt",
			})
			if err != nil {
				t.Fatal(err)
			}
			if got := result.Metadata["matches"]; got != 0 {
				t.Fatalf("glob matches = %v, want 0:\n%s", got, result.Content)
			}
		})
	}
}

func TestRemoteSearchUnsafeRipgrepGlobFallsBackToWalker(t *testing.T) {
	cfg, workspace := newSearchFixture(t)
	rg := resolveRipgrepRunner()
	if !rg.available() {
		t.Skip("ripgrep is not available")
	}

	glob := newRemoteGlobTool(cfg, workspace)
	glob.rg = rg
	result, err := glob.Execute(context.Background(), map[string]interface{}{
		"pattern": "nested/*.txt",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := result.Metadata["engine"]; got != "walker" {
		t.Fatalf("engine = %v, want walker", got)
	}
	if got := result.Metadata["matches"]; got != 1 {
		t.Fatalf("glob matches = %v, want 1:\n%s", got, result.Content)
	}

	grep := newRemoteGrepTool(cfg, workspace)
	grep.rg = rg
	result, err = grep.Execute(context.Background(), map[string]interface{}{
		"pattern": "needle",
		"glob":    "nested/*.txt",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := result.Metadata["engine"]; got != "walker" {
		t.Fatalf("engine = %v, want walker", got)
	}
	if got := result.Metadata["matches"]; got != 0 {
		t.Fatalf("grep matches = %v, want 0:\n%s", got, result.Content)
	}
}

func TestRemoteGrepInvalidRegexStillFails(t *testing.T) {
	cfg, workspace := newSearchFixture(t)
	engines := []struct {
		name string
		rg   ripgrepRunner
	}{
		{name: "walker"},
		{name: "ripgrep", rg: resolveRipgrepRunner()},
	}
	for _, engine := range engines {
		if engine.name == "ripgrep" && !engine.rg.available() {
			continue
		}
		t.Run(engine.name, func(t *testing.T) {
			tool := newRemoteGrepTool(cfg, workspace)
			tool.rg = engine.rg
			result, err := tool.Execute(context.Background(), map[string]interface{}{
				"pattern": "[",
			})
			if err != nil {
				t.Fatal(err)
			}
			if result.Success {
				t.Fatalf("invalid regex succeeded with engine %v", result.Metadata["engine"])
			}
			if got := result.Metadata["error_code"]; got != CodeInvalidArgument {
				t.Fatalf("error_code = %v, want %s", got, CodeInvalidArgument)
			}
		})
	}
}

func TestRemoteRipgrepMatchesWalkerConformance(t *testing.T) {
	cfg, _ := newSearchFixture(t)
	root := cfg.Workspace
	files := map[string][]byte{
		"crlf.txt":      []byte("needle\r\n"),
		"unicode.txt":   []byte("héllo 世界 needle\n"),
		"nul.bin":       []byte("needle\x00binary\n"),
		"invalid.bin":   []byte("needle\xff\n"),
		"long.txt":      []byte("needle" + strings.Repeat("x", 5000) + "\n"),
		".gitignore":    []byte("ignored.txt\n"),
		"ignored.txt":   []byte("needle ignored but not filtered\n"),
		"sub/deep.txt":  []byte("needle deep\n"),
		"sub/empty.txt": []byte("nothing here\n"),
	}
	for rel, content := range files {
		full := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, content, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("needle outside\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "secret.txt"), filepath.Join(root, "link-file.txt")); err != nil {
		t.Logf("symlink fixture unavailable: %v", err)
	} else if err := os.Symlink(outside, filepath.Join(root, "link-dir")); err != nil {
		t.Logf("directory symlink fixture unavailable: %v", err)
	}

	opts := searchcore.GrepOptions{
		Pattern:      "needle",
		Path:         root,
		Limit:        100,
		MaxFileBytes: 1 << 20,
	}
	walker, err := searchcore.Grep(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	rg := resolveRipgrepRunner()
	if !rg.available() {
		t.Skip("ripgrep is not available")
	}
	ripgrepResult, used, err := rg.grep(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	if !used {
		t.Fatal("ripgrep runner declined a standard search")
	}

	if got, want := normalizedGrepMatches(ripgrepResult), normalizedGrepMatches(walker); !reflect.DeepEqual(got, want) {
		t.Fatalf("ripgrep/walker mismatch\nripgrep: %#v\nwalker:  %#v", got, want)
	}
}

func normalizedGrepMatches(result searchcore.GrepResult) []string {
	matches := make([]string, 0, len(result.Matches))
	for _, match := range result.Matches {
		matches = append(matches, fmt.Sprintf("%s:%d:%s", filepath.ToSlash(filepath.Base(match.File)), match.Line, match.Text))
	}
	sort.Strings(matches)
	return matches
}

func TestRemoteGrepExplicitSymlinkFollows(t *testing.T) {
	cfg, _ := newSearchFixture(t)
	link := filepath.Join(cfg.Workspace, "visible-link.txt")
	if err := os.Symlink(filepath.Join(cfg.Workspace, "visible.txt"), link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	opts := searchcore.GrepOptions{Pattern: "needle", Path: link, Limit: 10, MaxFileBytes: 1 << 20}
	walker, err := searchcore.Grep(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	rg := resolveRipgrepRunner()
	if !rg.available() {
		t.Skip("ripgrep is not available")
	}
	ripgrepResult, used, err := rg.grep(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	if !used {
		t.Fatal("ripgrep runner declined an explicit symlink")
	}
	if got, want := normalizedGrepMatches(ripgrepResult), normalizedGrepMatches(walker); !reflect.DeepEqual(got, want) {
		t.Fatalf("explicit symlink mismatch\nripgrep: %#v\nwalker:  %#v", got, want)
	}
	if len(walker.Matches) != 1 {
		t.Fatalf("explicit symlink matches = %d, want 1", len(walker.Matches))
	}
}

func TestSafeRipgrepGlob(t *testing.T) {
	cases := map[string]bool{
		"*.go":        true,
		"**/*.go":     true,
		"**":          true,
		"foo?bar":     true,
		"foo/**":      false,
		"nested/*.go": false,
		"[!a]*":       false,
		"{a,b}":       false,
		"!*.go":       false,
		"/root/*.go":  false,
	}
	for pattern, want := range cases {
		if got := safeRipgrepGlob(pattern); got != want {
			t.Errorf("safeRipgrepGlob(%q) = %v, want %v", pattern, got, want)
		}
	}
}
