package searchcore

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestGrepRenderByteBudgetKeepsLeadingLines pins the shared byte window: a
// match list longer than grep's model-visible budget stops at complete leading
// lines, flags truncation, appends the byte-budget hint and publishes the
// metadata dispositions the local tool has always stamped.
func TestGrepRenderByteBudgetKeepsLeadingLines(t *testing.T) {
	root := t.TempDir()
	line := strings.Repeat("x", 2048)
	var content strings.Builder
	for i := 0; i < 200; i++ {
		content.WriteString(line)
		content.WriteString("\n")
	}
	if err := os.WriteFile(filepath.Join(root, "big.txt"), []byte(content.String()), 0o644); err != nil {
		t.Fatal(err)
	}

	result, err := Grep(context.Background(), GrepOptions{Pattern: "x", Path: root, Limit: 500})
	if err != nil {
		t.Fatal(err)
	}
	page := renderGrepPage(grepRenderInputs{
		paths: fakeRenderPaths{root: root}, result: &result,
		options: GrepOptions{Limit: 500}, mode: GrepModeContent, roots: []string{root},
	})
	if !page.byteTruncated {
		t.Fatalf("a 400 KiB page must stop at the %d byte budget", GrepRenderBudget())
	}
	first := strings.SplitN(page.content, "\n", 2)[0]
	if !strings.HasPrefix(first, "big.txt:1: ") {
		t.Fatalf("leading matches must stay intact, got %q", first[:min(len(first), 48)])
	}
	if !strings.Contains(page.content, "字节预算") || !strings.Contains(page.content, "next_step") {
		t.Fatalf("byte-budget hint missing:\n%s", tailOf(page.content))
	}

	metadata := renderGrepMetadataStamps(map[string]any{}, page, &result, 0)
	if metadata["skip_render_truncation"] != true {
		t.Fatalf("render opt-out missing: %#v", metadata)
	}
	if metadata["results_truncated"] != true || metadata["truncation_reason"] != "byte_budget" {
		t.Fatalf("byte-budget dispositions missing: %#v", metadata)
	}
	if _, ok := metadata["next_action"].(string); !ok {
		t.Fatalf("next_action missing: %#v", metadata)
	}
	if budget, ok := metadata["model_visible_budget_bytes"].(int); !ok || budget != GrepRenderBudget() {
		t.Fatalf("declared window = %#v", metadata["model_visible_budget_bytes"])
	}
}

// TestGrepRenderEmptySuccessDisposition pins the empty-page stamps: a true
// no-match success is marked empty_result/outcome=empty so the model treats it
// as valid evidence; a byte-truncated page never gets the empty marking.
func TestGrepRenderEmptySuccessDisposition(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("needle\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	empty, err := Grep(context.Background(), GrepOptions{Pattern: "nothing", Path: root})
	if err != nil {
		t.Fatal(err)
	}
	page := renderGrepPage(grepRenderInputs{
		paths: fakeRenderPaths{root: root}, result: &empty,
		options: GrepOptions{}, mode: GrepModeContent, roots: []string{root},
	})
	metadata := renderGrepMetadataStamps(map[string]any{}, page, &empty, 0)
	if metadata["empty_result"] != true || metadata["outcome"] != "empty" {
		t.Fatalf("empty success must be marked: %#v", metadata)
	}

	truncated := GrepResult{Truncated: true, Matches: []GrepMatch{{File: "f", Line: 1}}}
	truncatedPage := grepPage{content: "f:1: m", byteTruncated: true}
	stamped := renderGrepMetadataStamps(map[string]any{}, truncatedPage, &truncated, 0)
	if stamped["empty_result"] == true {
		t.Fatalf("a truncated page must not be marked empty: %#v", stamped)
	}
}

func tailOf(content string) string {
	if len(content) > 300 {
		return content[len(content)-300:]
	}
	return content
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
