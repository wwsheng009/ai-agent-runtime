package remotetools_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/remotetools"
)

// TestRemoteGrepOffsetPagesOverMCP proves the declared offset parameter works
// end to end: schema -> handler -> runtime -> engine. Page two must advance,
// whichever search engine the surface selects.
func TestRemoteGrepOffsetPagesOverMCP(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "hits.txt"), []byte("hit one\nmiss\nhit two\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	server, err := remotetools.NewServer(newTestConfig(t, root), "dev")
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	defer server.Close()
	session, ctx := startTestSession(t, server)

	first := callTool(t, ctx, session, "remote_dev__grep", map[string]interface{}{
		"pattern": "hit", "path": "hits.txt", "limit": 1, "offset": 0,
	})
	if !strings.Contains(first, "hit one") || strings.Contains(first, "hit two") {
		t.Fatalf("page one should contain only the first match:\n%s", first)
	}
	second := callTool(t, ctx, session, "remote_dev__grep", map[string]interface{}{
		"pattern": "hit", "path": "hits.txt", "limit": 1, "offset": 1,
	})
	if !strings.Contains(second, "hit two") || strings.Contains(second, "hit one") {
		t.Fatalf("page two should advance to the second match:\n%s", second)
	}
}

// TestRemoteGrepPatternOptionsOverMCP proves the pattern switches declared in
// the remote schema reach the shared implementation through a real session:
// multi-pattern OR, literal text and invert_match used to be local-only.
func TestRemoteGrepPatternOptionsOverMCP(t *testing.T) {
	root := t.TempDir()
	content := "one\nAlpha\nbeta\na.b\naXb\n"
	if err := os.WriteFile(filepath.Join(root, "opts.txt"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	server, err := remotetools.NewServer(newTestConfig(t, root), "dev")
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	defer server.Close()
	session, ctx := startTestSession(t, server)

	or := callTool(t, ctx, session, "remote_dev__grep", map[string]interface{}{
		"patterns": []interface{}{"one", "beta"}, "path": "opts.txt",
	})
	if !strings.Contains(or, "one") || !strings.Contains(or, "beta") {
		t.Fatalf("patterns OR should match both:\n%s", or)
	}

	literal := callTool(t, ctx, session, "remote_dev__grep", map[string]interface{}{
		"pattern": "a.b", "path": "opts.txt", "literal": true,
	})
	if !strings.Contains(literal, "a.b") || strings.Contains(literal, "aXb") {
		t.Fatalf("literal search must not treat '.' as a wildcard:\n%s", literal)
	}

	regexp := callTool(t, ctx, session, "remote_dev__grep", map[string]interface{}{
		"pattern": "a.b", "path": "opts.txt",
	})
	if !strings.Contains(regexp, "aXb") {
		t.Fatalf("regex '.' should match the wildcard line:\n%s", regexp)
	}

	inverted := callTool(t, ctx, session, "remote_dev__grep", map[string]interface{}{
		"pattern": "one", "path": "opts.txt", "invert_match": true,
	})
	if !strings.Contains(inverted, "Alpha") || strings.Contains(inverted, "opts.txt:1:one") {
		t.Fatalf("invert_match should keep the non-matching lines:\n%s", inverted)
	}
}

// TestRemoteGrepContextOverMCP proves the context parameters survive the MCP
// schema validation and render the documented shapes: `path:line:text` for
// matches, `path-line-text` for context, `--` between blocks.
func TestRemoteGrepContextOverMCP(t *testing.T) {
	root := t.TempDir()
	content := "one\ntwo\nMATCH three\nfour\nfive\nsix\nMATCH seven\neight\n"
	if err := os.WriteFile(filepath.Join(root, "ctx.txt"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	server, err := remotetools.NewServer(newTestConfig(t, root), "dev")
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	defer server.Close()
	session, ctx := startTestSession(t, server)

	out := callTool(t, ctx, session, "remote_dev__grep", map[string]interface{}{
		"pattern": "MATCH", "path": "ctx.txt", "context": 1,
	})
	for _, want := range []string{"ctx.txt-2-two", "ctx.txt:3:MATCH three", "--", "ctx.txt:7:MATCH seven"} {
		if !strings.Contains(out, want) {
			t.Fatalf("context output missing %q:\n%s", want, out)
		}
	}
}

// TestRemoteGrepFiltersOverMCP proves the include/exclude filters survive the
// MCP schema validation and select the same files a local search would:
// a bare pattern matches the basename anywhere below the root.
func TestRemoteGrepFiltersOverMCP(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "nested"), 0o755); err != nil {
		t.Fatal(err)
	}
	for path, content := range map[string]string{
		"a.go":        "hit go\n",
		"b.ts":        "hit ts\n",
		"nested/c.go": "hit nested\n",
	} {
		if err := os.WriteFile(filepath.Join(root, filepath.FromSlash(path)), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	server, err := remotetools.NewServer(newTestConfig(t, root), "dev")
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	defer server.Close()
	session, ctx := startTestSession(t, server)

	included := callTool(t, ctx, session, "remote_dev__grep", map[string]interface{}{
		"pattern": "hit", "include": "*.go",
	})
	if !strings.Contains(included, "a.go:1:hit go") || !strings.Contains(included, "nested/c.go:1:hit nested") {
		t.Fatalf("include *.go should keep nested Go files:\n%s", included)
	}
	if strings.Contains(included, "b.ts") {
		t.Fatalf("include *.go must drop TypeScript files:\n%s", included)
	}

	excluded := callTool(t, ctx, session, "remote_dev__grep", map[string]interface{}{
		"pattern": "hit", "exclude": []interface{}{"b.ts"},
	})
	if strings.Contains(excluded, "b.ts") {
		t.Fatalf("exclude b.ts must drop the file:\n%s", excluded)
	}
	if !strings.Contains(excluded, "a.go:1:hit go") {
		t.Fatalf("exclude must not drop unrelated files:\n%s", excluded)
	}
}

// TestRemoteGrepMultiRootsOverMCP proves `paths` survives schema validation and
// searches every root in order, including the single-string spelling.
func TestRemoteGrepMultiRootsOverMCP(t *testing.T) {
	root := t.TempDir()
	for path, content := range map[string]string{
		"alpha/one.txt": "hit one\n",
		"beta/two.txt":  "hit two\n",
	} {
		full := filepath.Join(root, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	server, err := remotetools.NewServer(newTestConfig(t, root), "dev")
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	defer server.Close()
	session, ctx := startTestSession(t, server)

	both := callTool(t, ctx, session, "remote_dev__grep", map[string]interface{}{
		"pattern": "hit", "paths": []interface{}{"alpha", "beta"},
	})
	if !strings.Contains(both, "alpha/one.txt:1:hit one") || !strings.Contains(both, "beta/two.txt:1:hit two") {
		t.Fatalf("paths should search every root:\n%s", both)
	}

	one := callTool(t, ctx, session, "remote_dev__grep", map[string]interface{}{
		"pattern": "hit", "paths": "alpha",
	})
	if !strings.Contains(one, "one.txt") || strings.Contains(one, "two.txt") {
		t.Fatalf("the string spelling must search exactly that root:\n%s", one)
	}
}

// TestRemoteGrepModesOverMCP proves the mode parameter survives schema
// validation and renders the documented shapes: bare paths for files mode,
// `path:count` for count mode, and the rg-compatible boolean aliases.
func TestRemoteGrepModesOverMCP(t *testing.T) {
	root := t.TempDir()
	for name, content := range map[string]string{
		"a.txt": "hit one\nhit two\n",
		"b.txt": "hit again\n",
	} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	server, err := remotetools.NewServer(newTestConfig(t, root), "dev")
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	defer server.Close()
	session, ctx := startTestSession(t, server)

	files := callTool(t, ctx, session, "remote_dev__grep", map[string]interface{}{
		"pattern": "hit", "mode": "files",
	})
	if !strings.Contains(files, "a.txt") || !strings.Contains(files, "b.txt") {
		t.Fatalf("files mode should list both files:\n%s", files)
	}
	if strings.Contains(files, "hit one") {
		t.Fatalf("files mode must not print matching lines:\n%s", files)
	}

	counts := callTool(t, ctx, session, "remote_dev__grep", map[string]interface{}{
		"pattern": "hit", "mode": "count",
	})
	if !strings.Contains(counts, "a.txt:2") || !strings.Contains(counts, "b.txt:1") {
		t.Fatalf("count mode should print per-file matching line counts:\n%s", counts)
	}

	alias := callTool(t, ctx, session, "remote_dev__grep", map[string]interface{}{
		"pattern": "hit", "files_with_matches": true,
	})
	if !strings.Contains(alias, "a.txt") || strings.Contains(alias, "hit one") {
		t.Fatalf("files_with_matches must behave like mode=files:\n%s", alias)
	}
}
