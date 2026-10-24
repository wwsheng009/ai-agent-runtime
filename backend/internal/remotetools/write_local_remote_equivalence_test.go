package remotetools_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/remotetools"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolkit"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolkit/tools"
)

func runLocalWrite(t *testing.T, root string, args map[string]interface{}) *toolkit.ToolResult {
	t.Helper()
	tool := tools.NewWriteTool()
	tool.SetBasePath(root)
	result, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("local write execute: %v", err)
	}
	return result
}

// TestRemoteWriteMatchesLocalSemantics pins the shared write contract over MCP:
// creating a file, overwriting it, and the expected_sha256 precondition all
// behave the way the local tool does, because both run the same handler.
func TestRemoteWriteMatchesLocalSemantics(t *testing.T) {
	root := t.TempDir()
	server, err := remotetools.NewServer(newStandardTestConfig(t, root), "dev")
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	defer server.Close()
	session, ctx := startTestSession(t, server)

	// Create.
	created := callTool(t, ctx, session, "remote_dev__write", map[string]interface{}{
		"file_path": "notes.txt",
		"content":   "first\n",
	})
	if !strings.Contains(created, "notes.txt") {
		t.Fatalf("create body missing path: %q", created)
	}
	if raw, readErr := os.ReadFile(filepath.Join(root, "notes.txt")); readErr != nil || string(raw) != "first\n" {
		t.Fatalf("remote create did not land: %q %v", raw, readErr)
	}

	// Overwrite with the correct precondition.
	localFirst := runLocalWrite(t, root, map[string]interface{}{
		"file_path": "local.txt",
		"content":   "first\n",
	})
	if !localFirst.Success {
		t.Fatalf("local create failed: %+v", localFirst)
	}
	revision, _ := localFirst.Metadata["new_sha256"].(string)
	if revision == "" {
		t.Fatalf("write must publish new_sha256, got %#v", localFirst.Metadata)
	}
	updated := callTool(t, ctx, session, "remote_dev__write", map[string]interface{}{
		"file_path":       "notes.txt",
		"content":         "second\n",
		"expected_sha256": revision,
	})
	if !strings.Contains(updated, "notes.txt") {
		t.Fatalf("update body missing path: %q", updated)
	}

	// A stale precondition is a classified failure, not a silent overwrite.
	stale := callToolExpectingError(t, ctx, session, "remote_dev__write", map[string]interface{}{
		"file_path":       "notes.txt",
		"content":         "third\n",
		"expected_sha256": revision,
	})
	if !strings.Contains(stale, "WRITE_PRECONDITION_FAILED") {
		t.Fatalf("stale precondition must be classified, got %q", stale)
	}
	if raw, _ := os.ReadFile(filepath.Join(root, "notes.txt")); string(raw) != "second\n" {
		t.Fatalf("a refused write must leave the file untouched, got %q", raw)
	}
}

// TestRemoteWriteKeepsLocalGuards pins the two guards that used to exist only
// locally: document containers are refused, and an oversized inline payload is
// rejected before the transport can truncate it.
func TestRemoteWriteKeepsLocalGuards(t *testing.T) {
	root := t.TempDir()
	server, err := remotetools.NewServer(newStandardTestConfig(t, root), "dev")
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	defer server.Close()
	session, ctx := startTestSession(t, server)

	refused := callToolExpectingError(t, ctx, session, "remote_dev__write", map[string]interface{}{
		"file_path": "report.docx",
		"content":   "plain text pretending to be a document\n",
	})
	if !strings.Contains(refused, "文档格式") {
		t.Fatalf("a document container must be refused, got %q", refused)
	}
	if _, statErr := os.Stat(filepath.Join(root, "report.docx")); statErr == nil {
		t.Fatal("a refused document write must not create the file")
	}

	oversized := strings.Repeat("x", 65*1024)
	tooBig := callToolExpectingError(t, ctx, session, "remote_dev__write", map[string]interface{}{
		"file_path": "big.txt",
		"content":   oversized,
	})
	if !strings.Contains(tooBig, "过大") {
		t.Fatalf("an oversized inline payload must be refused, got %q", tooBig)
	}
}

// newStandardTestConfig is the readonly fixture plus the mutating tools, which
// are what a write test needs. The readonly profile deliberately excludes them.
func newStandardTestConfig(t *testing.T, root string) *remotetools.Config {
	t.Helper()
	cfg := newTestConfig(t, root)
	cfg.Profile = remotetools.ProfileStandard
	if err := cfg.Normalize(); err != nil {
		t.Fatalf("normalize: %v", err)
	}
	return cfg
}
