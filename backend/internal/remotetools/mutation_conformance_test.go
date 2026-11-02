package remotetools_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/remotetools"
)

// TestRemoteMutationsPreserveEncoding locks the shared mutatecore codec at the
// remote surface: BOM/UTF-16 must round-trip through write/append/edit/patch.
func TestRemoteMutationsPreserveEncoding(t *testing.T) {
	root := newTestWorkspace(t)
	server, err := remotetools.NewServer(newOpsConfig(t, root, []string{"cat"}), "dev")
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	defer server.Close()
	session, ctx := startTestSession(t, server)

	utf8BOM := []byte{0xEF, 0xBB, 0xBF}
	bomPath := filepath.Join(root, "bom.txt")
	if err := os.WriteFile(bomPath, append(append([]byte{}, utf8BOM...), "old\n"...), 0o644); err != nil {
		t.Fatalf("seed BOM file: %v", err)
	}
	callTool(t, ctx, session, "remote_dev__write", map[string]interface{}{
		"file_path": "bom.txt",
		"content":   "new\n",
	})
	assertBytes(t, bomPath, append(append([]byte{}, utf8BOM...), "new\n"...))
	callTool(t, ctx, session, "remote_dev__append_write", map[string]interface{}{
		"file_path": "bom.txt",
		"content":   "tail\n",
	})
	assertBytes(t, bomPath, append(append([]byte{}, utf8BOM...), "new\ntail\n"...))
	callTool(t, ctx, session, "remote_dev__edit", map[string]interface{}{
		"file_path":  "bom.txt",
		"old_string": "new\ntail",
		"new_string": "NEW\ntail",
	})
	assertBytes(t, bomPath, append(append([]byte{}, utf8BOM...), "NEW\ntail\n"...))

	crlfPath := filepath.Join(root, "crlf.txt")
	if err := os.WriteFile(crlfPath, append(append([]byte{}, utf8BOM...), "a\r\nb\r\n"...), 0o644); err != nil {
		t.Fatalf("seed CRLF file: %v", err)
	}
	patch := strings.Join([]string{
		"*** Begin Patch",
		"*** Update File: crlf.txt",
		"@@",
		"-a",
		"+A",
		"*** End Patch",
	}, "\n")
	callTool(t, ctx, session, "remote_dev__apply_patch", map[string]interface{}{"input": patch})
	assertBytes(t, crlfPath, append(append([]byte{}, utf8BOM...), "A\r\nb\r\n"...))
}

func assertBytes(t *testing.T, path string, want []byte) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if string(got) != string(want) {
		t.Fatalf("%s = %q, want %q", path, got, want)
	}
}
