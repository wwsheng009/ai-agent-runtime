package remotetools_test

// After S6 the envd grep is the shared handler: it walks the target through the
// accessor, pages its matches, and folds to the model window with the same
// budget rule as every other transport. The byte ceiling of the retired shell
// script went with the script - what bounds a caller's page now is the page
// size and the fold, and the result declares both instead of leaving the reader
// to infer them from a stream cut.
//
// The assertions stay on deterministic ground: the stub answers ListDir, Stat
// and /files without timing games, so no expectation depends on how fast a
// socket was drained.

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/remotetools"
)

func envDGrepFixture(count int) map[string]string {
	files := map[string]string{}
	for i := 0; i < count; i++ {
		files[fmt.Sprintf("/srv/app/f%02d.txt", i)] = "needle\n"
	}
	return files
}

// TestEnvDGrepFoldsToTheModelWindow pins the cost model that replaced the
// script's stream cut: a large match set is cut by the configured model-visible
// budget and says it was cut.
func TestEnvDGrepFoldsToTheModelWindow(t *testing.T) {
	stub := newEnvdStub(t, envDGrepFixture(40), nil)

	cfg := newEnvDConfig(stub.server.URL)
	// One documented knob is enough: the fold budget derives from it.
	cfg.Limits.ModelVisibleCap = 512
	server, err := remotetools.NewServer(cfg, "envd")
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	t.Cleanup(func() { _ = server.Close() })
	session, ctx := connectEnvDServer(t, server)

	result := callToolResult(t, ctx, session, "remote_envd__grep", map[string]interface{}{
		"path": ".", "pattern": "needle",
	})
	got := textContent(result)
	if !strings.Contains(got, "needle") {
		t.Fatalf("no matches came back at all: %q", got)
	}
	// 40 matches at ~16 bytes per line exceed the 512-byte window several times;
	// the body must be folded, not delivered whole.
	if strings.Count(got, "needle") >= 40 {
		t.Fatalf("the fold delivered every match under a 512-byte window:\n%s", got)
	}
	// The fold reserves a marker inside the budget; a body that overshoots the
	// window by more than the marker would make the budget a suggestion.
	if len(got) > 512+256 {
		t.Fatalf("returned %d bytes under a 512-byte model window:\n%s", len(got), got)
	}
	if strings.Contains(got, "stopped at") {
		t.Fatalf("the retired script wording came back: %q", got)
	}
	encoded, _ := json.Marshal(result.Meta)
	if !strings.Contains(string(encoded), `"is_truncated":true`) {
		t.Fatalf("a folded page must disclose the cut: %s", encoded)
	}
}

// TestEnvDGrepPagesThroughTheAccessor pins the continuation contract: an
// explicit page size is filled from the target's own enumeration (sorted by the
// core), the walk stops instead of draining every file, and offset continues
// rather than repeating.
func TestEnvDGrepPagesThroughTheAccessor(t *testing.T) {
	stub := newEnvdStub(t, envDGrepFixture(12), nil)
	session, ctx, _ := connectEnvDServerWithProfile(t, stub, remotetools.ProfileReadonly)

	first := callToolResult(t, ctx, session, "remote_envd__grep", map[string]interface{}{
		"path": ".", "pattern": "needle", "limit": 3,
	})
	got := textContent(first)
	if strings.Count(got, "needle") != 3 {
		t.Fatalf("a limit=3 page must carry exactly three matches:\n%s", got)
	}
	if !strings.Contains(got, "f00.txt") {
		t.Fatalf("the page did not start at the lexical head of the target's order:\n%s", got)
	}
	encoded, _ := json.Marshal(first.Meta)
	if !strings.Contains(string(encoded), `"has_more":true`) {
		t.Fatalf("a cut page must say has_more: %s", encoded)
	}
	// The point of paging: the walk stops once the page is full. Three lines over
	// twelve files means a handful of reads, not the whole tree.
	if reads := len(stub.filesRequests()); reads == 0 || reads >= 12 {
		t.Fatalf("reads = %d for a 3-line page over 12 files: the walk drained the target instead of stopping early", reads)
	}

	second := callTool(t, ctx, session, "remote_envd__grep", map[string]interface{}{
		"path": ".", "pattern": "needle", "limit": 3, "offset": 3,
	})
	if strings.Contains(second, "f00.txt") {
		t.Fatalf("page two repeated page one:\n%s", second)
	}
	if !strings.Contains(second, "f03.txt") {
		t.Fatalf("page two did not continue the sequence:\n%s", second)
	}
}

// Without a ceiling in the way, a normal search keeps the shared render
// (path:line: text), reports the honest engine, and keeps the no-match wording
// callers already branch on.
func TestEnvDGrepKeepsTheSharedWording(t *testing.T) {
	stub := newEnvdStub(t, map[string]string{
		"/srv/app/README.md": "line one\nhello envd\nline three\n",
		"/srv/app/other.md":  "nothing here\n",
	}, nil)
	session, ctx, _ := connectEnvDServerWithProfile(t, stub, remotetools.ProfileReadonly)

	result := callToolResult(t, ctx, session, "remote_envd__grep", map[string]interface{}{
		"path": ".", "pattern": "hello",
	})
	got := textContent(result)
	// Exact, not substring: a label that grew an absolute "/srv/app" prefix still
	// "contains" the relative form, which is how a rendering regression slipped
	// past the first draft of this assertion. The shared render is
	// path:line: text, relative to the search root.
	if strings.TrimSpace(got) != "README.md:2: hello envd" {
		t.Fatalf("the shared render changed: %q", got)
	}
	if strings.Contains(got, "searched with") {
		t.Fatalf("the retired script wording came back: %q", got)
	}
	// ripgrep is the host's engine and cannot read the target's files; the engine
	// that can honestly serve this call is the walker over the accessor.
	encoded, _ := json.Marshal(result.Meta)
	if !strings.Contains(string(encoded), `"engine":"walker"`) {
		t.Fatalf("engine must be the walker here: %s", encoded)
	}

	empty := callTool(t, ctx, session, "remote_envd__grep", map[string]interface{}{
		"path": ".", "pattern": "absent-token",
	})
	if !strings.Contains(empty, "未找到匹配的内容") {
		t.Fatalf("no-match wording changed: %q", empty)
	}
}
