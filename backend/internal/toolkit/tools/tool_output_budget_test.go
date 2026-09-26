package tools

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

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

// TestFoldShellOutputToWindowKeepsHeadAndTail pins the head+tail contract: the
// last lines (errors / test verdicts) survive the fold, the omitted middle is
// absent, and the notice carries the exact artifact_read route into it.
func TestFoldShellOutputToWindowKeepsHeadAndTail(t *testing.T) {
	head := "HEAD-MARKER " + strings.Repeat("h", 4096) + "\n"
	// 深中段标记必须落在 head/tail 窗口之间：head 侧先垫 20 KB，
	// tail 侧再垫 20 KB，两个窗口都够不到中间的标记。
	middle := strings.Repeat("m", 20000) + "\nDEEP-MIDDLE-MARKER\n" + strings.Repeat("m", 20000) + "\n"
	tail := strings.Repeat("z", 4096) + "\nTAIL-MARKER-THESIS: 3 passed, 1 failed\n"
	full := head + middle + tail

	folded, stats, didFold := foldShellOutputToWindow(full, shellOutputBudgetBytes)
	if !didFold {
		t.Fatalf("expected a fold for a %d byte payload", len(full))
	}
	if len(folded) > shellOutputBudgetBytes {
		t.Fatalf("folded payload exceeds the window: %d > %d", len(folded), shellOutputBudgetBytes)
	}
	if !strings.HasPrefix(folded, "HEAD-MARKER ") {
		t.Fatalf("folded window must start with the head, got %q", folded[:64])
	}
	if !strings.HasSuffix(folded, "TAIL-MARKER-THESIS: 3 passed, 1 failed\n") {
		t.Fatalf("folded window must end with the tail verdict, got %q", folded[len(folded)-96:])
	}
	if stats.HeadBytes <= 0 || stats.TailBytes <= 0 {
		t.Fatalf("expected both window halves to hold bytes: %+v", stats)
	}
	if stats.OmittedEnd <= stats.OmittedStart {
		t.Fatalf("expected a non-empty omitted middle: %+v", stats)
	}
	if strings.Contains(folded, "DEEP-MIDDLE-MARKER") {
		t.Fatal("deep middle content leaked into the model-visible window")
	}
	// 中间值入口：精确区间 + 第一页 offset/limit + bisect offset + 总量成本。
	if !strings.Contains(folded, fmt.Sprintf("omitted middle bytes [%d,%d)", stats.OmittedStart, stats.OmittedEnd)) {
		t.Fatalf("notice must name the omitted byte range, got %q", folded)
	}
	if !strings.Contains(folded, fmt.Sprintf("offset=%d, limit=%d", stats.OmittedStart, artifactReadMaxLimitBytes())) {
		t.Fatalf("notice must give the first artifact_read page, got %q", folded)
	}
	if !strings.Contains(folded, "bisect from offset=") {
		t.Fatalf("notice must offer a bisect offset, got %q", folded)
	}
	if !strings.Contains(folded, "pages") || !strings.Contains(folded, "Cheapest route") {
		t.Fatalf("notice must state the paging cost and the cheaper route, got %q", folded)
	}
	if !utf8.ValidString(folded) {
		t.Fatal("folded window must stay valid UTF-8")
	}
}

// TestFoldShellOutputToWindowDoesNotSplitRunes proves both window edges snap to
// rune boundaries even when the budget lands inside a multi-byte sequence.
func TestFoldShellOutputToWindowDoesNotSplitRunes(t *testing.T) {
	full := strings.Repeat("é", 4096) + strings.Repeat("漢", 4096) + strings.Repeat("é", 4096)
	for _, budget := range []int{64, 65, 100, 257} {
		folded, _, didFold := foldShellOutputToWindow(full, budget)
		if !didFold {
			t.Fatalf("budget %d: expected a fold", budget)
		}
		if !utf8.ValidString(folded) {
			t.Fatalf("budget %d: folded window is not valid UTF-8", budget)
		}
		if len(folded) > budget {
			t.Fatalf("budget %d: folded window is %d bytes", budget, len(folded))
		}
	}
}

// TestOwnShellOutputWindowPublishesMiddleRoute pins the metadata the model uses
// to price and address the omitted middle without re-deriving it from the body.
func TestOwnShellOutputWindowPublishesMiddleRoute(t *testing.T) {
	full := strings.Repeat("head line\n", 2048) + strings.Repeat("middle\n", 8192) + "final: ok\n"
	result := ownShellOutputWindow(context.Background(), "bash", &toolkit.ToolResult{
		Success:    true,
		OutputKind: toolresult.KindText,
		Content:    full,
	})
	if result == nil || !result.Success {
		t.Fatalf("unexpected result: %+v", result)
	}
	headBytes, _ := result.Metadata["output_window_head_bytes"].(int)
	tailBytes, _ := result.Metadata["output_window_tail_bytes"].(int)
	omittedStart, _ := result.Metadata["output_window_omitted_start"].(int)
	omittedEnd, _ := result.Metadata["output_window_omitted_end"].(int)
	pages, _ := result.Metadata["output_window_middle_pages"].(int)
	if headBytes <= 0 || tailBytes <= 0 || omittedEnd <= omittedStart || pages < 1 {
		t.Fatalf("unexpected window metadata: %#v", result.Metadata)
	}
	if omittedStart != headBytes {
		t.Fatalf("omitted middle must start where the head ends: start=%d head=%d", omittedStart, headBytes)
	}
	if wantPages := (omittedEnd - omittedStart + artifactReadMaxLimitBytes() - 1) / artifactReadMaxLimitBytes(); pages != wantPages {
		t.Fatalf("expected %d pages, got %d", wantPages, pages)
	}
	if !strings.HasSuffix(result.Content, "final: ok\n") {
		t.Fatalf("tail verdict must stay visible, got %q", result.Content[len(result.Content)-48:])
	}
}
