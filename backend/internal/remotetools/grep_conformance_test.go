package remotetools_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

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

// TestRemoteGrepTypeFiltersOverMCP proves the type parameters survive schema
// validation and expand through the shared table: `type=go` selects Go files
// anywhere below the root and `type_not` removes them again.
func TestRemoteGrepTypeFiltersOverMCP(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "nested"), 0o755); err != nil {
		t.Fatal(err)
	}
	for path, content := range map[string]string{
		"a.go":        "hit go\n",
		"b.py":        "hit py\n",
		"nested/c.go": "hit nested go\n",
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

	goOnly := callTool(t, ctx, session, "remote_dev__grep", map[string]interface{}{
		"pattern": "hit", "type": "go",
	})
	if !strings.Contains(goOnly, "a.go") || !strings.Contains(goOnly, "nested/c.go") {
		t.Fatalf("type go should select Go files at every depth:\n%s", goOnly)
	}
	if strings.Contains(goOnly, "b.py") {
		t.Fatalf("type go must drop Python files:\n%s", goOnly)
	}

	notGo := callTool(t, ctx, session, "remote_dev__grep", map[string]interface{}{
		"pattern": "hit", "type_not": "go",
	})
	if !strings.Contains(notGo, "b.py") || strings.Contains(notGo, "a.go") {
		t.Fatalf("type_not go must keep only the other types:\n%s", notGo)
	}

	alias := callTool(t, ctx, session, "remote_dev__grep", map[string]interface{}{
		"pattern": "hit", "file_type": "go",
	})
	if !strings.Contains(alias, "a.go") || strings.Contains(alias, "b.py") {
		t.Fatalf("file_type must behave like type:\n%s", alias)
	}
}

// TestRemoteGrepMaxCountOverMCP proves the per-file cap survives schema
// validation, including the explicit-zero contract shared with rg -m 0.
func TestRemoteGrepMaxCountOverMCP(t *testing.T) {
	root := t.TempDir()
	for name, content := range map[string]string{
		"a.txt": "hit a1\nhit a2\n",
		"b.txt": "hit b1\nhit b2\n",
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

	capped := callTool(t, ctx, session, "remote_dev__grep", map[string]interface{}{
		"pattern": "hit", "max_count": 1,
	})
	if !strings.Contains(capped, "hit a1") || !strings.Contains(capped, "hit b1") {
		t.Fatalf("max_count=1 should keep the first match of each file:\n%s", capped)
	}
	if strings.Contains(capped, "hit a2") || strings.Contains(capped, "hit b2") {
		t.Fatalf("max_count=1 must drop later matches in each file:\n%s", capped)
	}

	suppressed := callTool(t, ctx, session, "remote_dev__grep", map[string]interface{}{
		"pattern": "hit", "max_count": 0,
	})
	if !strings.Contains(suppressed, "(no matches)") {
		t.Fatalf("max_count=0 must suppress every match:\n%s", suppressed)
	}
}

func callToolExpectingError(t *testing.T, ctx context.Context, session *mcp.ClientSession, name string, args map[string]interface{}) string {
	t.Helper()
	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("call %s: %v", name, err)
	}
	if !result.IsError {
		t.Fatalf("tool %s should have failed:\n%s", name, textContent(result))
	}
	return textContent(result)
}

// TestRemoteGrepOnlyMatchingOverMCP proves the fragment flag survives schema
// validation, renders `path:line:column:fragment`, and that the unsupported
// only_matching+context combination fails loudly instead of silently dropping
// one of the two parameters.
func TestRemoteGrepOnlyMatchingOverMCP(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("foo bar foo\nother\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	server, err := remotetools.NewServer(newTestConfig(t, root), "dev")
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	defer server.Close()
	session, ctx := startTestSession(t, server)

	fragments := callTool(t, ctx, session, "remote_dev__grep", map[string]interface{}{
		"pattern": "foo", "only_matching": true,
	})
	if !strings.Contains(fragments, "a.txt:1:1:foo") || !strings.Contains(fragments, "a.txt:1:9:foo") {
		t.Fatalf("only_matching should print one line per fragment with its column:\n%s", fragments)
	}
	if strings.Contains(fragments, "foo bar foo") {
		t.Fatalf("only_matching must not print whole lines:\n%s", fragments)
	}

	conflict := callToolExpectingError(t, ctx, session, "remote_dev__grep", map[string]interface{}{
		"pattern": "foo", "only_matching": true, "context": 1,
	})
	if !strings.Contains(conflict, "only_matching") || !strings.Contains(conflict, "context") {
		t.Fatalf("the conflict message should name both parameters:\n%s", conflict)
	}
}
