package remotetools

import (
	"context"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/toolkit"
)

func execGrep(t *testing.T, tool *searchFacade, args map[string]interface{}) *toolkit.ToolResult {
	t.Helper()
	result, err := tool.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("grep: %v", err)
	}
	if !result.Success {
		t.Fatalf("grep failed: %+v", result.Error)
	}
	return result
}

func statsMap(t *testing.T, metadata map[string]interface{}) map[string]interface{} {
	t.Helper()
	raw, ok := metadata["stats"].(map[string]interface{})
	if !ok {
		t.Fatalf("stats missing from metadata: %+v", metadata)
	}
	return raw
}

// TestRemoteGrepStatsPageSummary pins the stats contract at the tool boundary
// (metadata is not visible through the MCP text content): stats appears only on
// request and summarizes the returned page, with the has_more evidence file
// excluded from files/count pages.
func TestRemoteGrepStatsPageSummary(t *testing.T) {
	cfg, workspace := newSearchFixture(t)
	tool := newRemoteGrepTool(cfg, workspace)

	without := execGrep(t, tool, map[string]interface{}{"pattern": "needle", "include": "visible.txt"})
	if _, exists := without.Metadata["stats"]; exists {
		t.Fatalf("stats must not appear unless requested: %+v", without.Metadata)
	}
	if got := without.Metadata["engine"]; got != "walker" {
		t.Fatalf("engine = %v, want walker", got)
	}

	with := execGrep(t, tool, map[string]interface{}{"pattern": "needle", "include": "visible.txt", "stats": true})
	stats := statsMap(t, with.Metadata)
	if stats["matches"] != 1 || stats["matched_lines"] != 1 || stats["files_with_matched"] != 1 {
		t.Fatalf("unexpected page stats: %+v", stats)
	}
	// The walker does not track FilesScanned; only the ripgrep summary fills
	// it. matches/matched_lines/files_with_matched are the page contract.
	if _, exists := stats["files_scanned"]; !exists {
		t.Fatalf("files_scanned key missing: %+v", stats)
	}

	// files mode with limit 1: the page holds one file; the second file is only
	// read as has_more evidence and must not be counted.
	filesPage := execGrep(t, tool, map[string]interface{}{
		"pattern": "needle", "mode": "files", "limit": 1, "stats": true,
	})
	pageStats := statsMap(t, filesPage.Metadata)
	if pageStats["files_with_matched"] != 1 {
		t.Fatalf("files_with_matched = %v, want 1 (evidence file must be excluded)", pageStats["files_with_matched"])
	}
	if pageStats["matches"] != 1 {
		t.Fatalf("matches = %v, want the page file's single matching line", pageStats["matches"])
	}
}

// TestRemoteGrepStatsMatchesPageMetadata keeps the stats numbers consistent
// with the page metadata the caller already sees: in files mode the page's
// matches and file counts must agree with their independently computed peers.
func TestRemoteGrepStatsMatchesPageMetadata(t *testing.T) {
	cfg, workspace := newSearchFixture(t)
	tool := newRemoteGrepTool(cfg, workspace)

	result := execGrep(t, tool, map[string]interface{}{"pattern": "needle", "mode": "files", "stats": true})
	stats := statsMap(t, result.Metadata)
	if stats["matches"] != result.Metadata["matches"] {
		t.Fatalf("stats.matches=%v but metadata.matches=%v", stats["matches"], result.Metadata["matches"])
	}
	if stats["files_with_matched"] != result.Metadata["files"] {
		t.Fatalf("stats.files_with_matched=%v but metadata.files=%v", stats["files_with_matched"], result.Metadata["files"])
	}
}
