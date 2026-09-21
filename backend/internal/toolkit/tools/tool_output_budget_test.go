package tools

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/toolkit"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolresult"
)

// TestBudgetOwningToolsStampRenderTruncationOptOut pins the render-layer opt-out
// for the tools whose stamp was previously covered only indirectly (fetch/ls/
// glob). view/grep/artifact_read have their own assertions; this test makes a
// lost or renamed stamp in the remaining budget-owning tools fail loudly.
func TestBudgetOwningToolsStampRenderTruncationOptOut(t *testing.T) {
	ctx := context.Background()

	t.Run("ls", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "a.go"), []byte("package a"), 0o644); err != nil {
			t.Fatalf("write fixture: %v", err)
		}
		result, err := NewLsTool().Execute(ctx, map[string]interface{}{"path": dir})
		if err != nil {
			t.Fatalf("ls execute: %v", err)
		}
		assertStampsRenderOptOut(t, "ls", result)
	})

	t.Run("glob", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "a.go"), []byte("package a"), 0o644); err != nil {
			t.Fatalf("write fixture: %v", err)
		}
		result, err := NewGlobTool().Execute(ctx, map[string]interface{}{"pattern": "*.go", "path": dir})
		if err != nil {
			t.Fatalf("glob execute: %v", err)
		}
		assertStampsRenderOptOut(t, "glob", result)
	})

	t.Run("fetch", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/plain")
			_, _ = w.Write([]byte("hello from fetch"))
		}))
		defer server.Close()

		result, err := NewFetchTool().Execute(ctx, map[string]interface{}{
			"url":    server.URL,
			"format": "text",
		})
		if err != nil {
			t.Fatalf("fetch execute: %v", err)
		}
		assertStampsRenderOptOut(t, "fetch", result)
	})
}

func assertStampsRenderOptOut(t *testing.T, name string, result *toolkit.ToolResult) {
	t.Helper()
	if result == nil {
		t.Fatalf("%s: nil result", name)
	}
	if !result.Success {
		t.Fatalf("%s: expected success, got %v", name, result.Error)
	}
	if !toolresult.SkipsRenderTruncation(result.Metadata) {
		t.Fatalf("%s: metadata must stamp %q, got %#v",
			name, toolresult.MetadataSkipRenderTruncationKey, result.Metadata)
	}
}
