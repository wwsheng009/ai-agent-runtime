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
