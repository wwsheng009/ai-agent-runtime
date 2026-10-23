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
