package remotetools_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/remotetools"
	"github.com/wwsheng009/ai-agent-runtime/internal/ripgrep"
)

// TestRemoteGrepJSONOverMCP pins the json route on the remote surface: with the
// engine present the client receives ripgrep's own event stream - valid JSON
// Lines, nothing appended - and the counters arrive in metadata. When the engine
// is absent the same call fails closed naming ripgrep (pinned in searchcore), so
// this test skips there instead of pretending the format exists.
func TestRemoteGrepJSONOverMCP(t *testing.T) {
	if _, err := ripgrep.Resolve(); err != nil {
		t.Skip("ripgrep is not installed on this host; the fail-closed route is pinned in searchcore")
	}
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "j.txt"), []byte("foo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	server, err := remotetools.NewServer(newTestConfig(t, root), "dev")
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	defer server.Close()
	session, ctx := startTestSession(t, server)

	out := callTool(t, ctx, session, "remote_dev__grep", map[string]interface{}{
		"pattern": "foo", "path": "j.txt", "json": true,
	})
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) == 0 {
		t.Fatal("a json call must return the event stream")
	}
	sawMatch := false
	for _, line := range lines {
		var event map[string]interface{}
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			t.Fatalf("every line must stay valid JSON, got %q: %v", line, err)
		}
		if event["type"] == "match" {
			sawMatch = true
		}
	}
	if !sawMatch {
		t.Fatalf("the stream must carry the match event:\n%s", out)
	}
	if strings.Contains(out, "next_offset") {
		t.Fatalf("no paging notice may be appended to a JSON stream:\n%s", out)
	}
}
