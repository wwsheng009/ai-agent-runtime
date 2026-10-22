package remotetools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func newSearchFixture(t *testing.T) (*Config, *Workspace) {
	t.Helper()
	root := t.TempDir()
	for path, content := range map[string]string{
		"visible.txt":           "needle visible\n",
		".hidden.txt":           "needle hidden\n",
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
			for _, want := range []string{"visible.txt", ".hidden.txt", "nested"} {
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
			for _, want := range []string{"visible.txt", ".hidden.txt", "nested"} {
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
