package tools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func stubGrepToolWithLines(t *testing.T, lines []string) *GrepTool {
	t.Helper()
	tool := NewGrepTool()
	tool.lookPath = func(string) (string, error) { return "rg", nil }
	tool.runCommand = func(context.Context, string, string, []string) ([]byte, error) {
		return []byte(strings.Join(lines, "\n")), nil
	}
	return tool
}

func grepPayloadLines(t *testing.T, content string) []string {
	t.Helper()
	body := content
	if index := strings.Index(body, "\n\n("); index >= 0 {
		body = body[:index]
	}
	if strings.TrimSpace(body) == "" || strings.Contains(body, "未找到匹配的内容") {
		return nil
	}
	return strings.Split(body, "\n")
}

func TestGrepPaginationSlicesFixedOutput(t *testing.T) {
	lines := make([]string, 0, 6)
	for index := 1; index <= 6; index++ {
		lines = append(lines, fmt.Sprintf("src/file.txt:%d:needle %d", index, index))
	}
	tool := stubGrepToolWithLines(t, lines)

	first, err := tool.Execute(context.Background(), map[string]interface{}{
		"pattern":    "needle",
		"head_limit": 2,
	})
	if err != nil || first == nil || !first.Success {
		t.Fatalf("page 1 failed: %v %+v", err, first)
	}
	pageOne := grepPayloadLines(t, first.Content)
	if len(pageOne) != 2 || !strings.Contains(pageOne[0], ":1:") || !strings.Contains(pageOne[1], ":2:") {
		t.Fatalf("unexpected page 1: %#v (%q)", pageOne, first.Content)
	}
	if first.Metadata["next_offset"] != 2 || first.Metadata["has_more"] != true {
		t.Fatalf("unexpected page 1 metadata: %#v", first.Metadata)
	}

	second, err := tool.Execute(context.Background(), map[string]interface{}{
		"pattern":    "needle",
		"head_limit": 2,
		"offset":     2,
	})
	if err != nil || second == nil || !second.Success {
		t.Fatalf("page 2 failed: %v %+v", err, second)
	}
	pageTwo := grepPayloadLines(t, second.Content)
	if len(pageTwo) != 2 || !strings.Contains(pageTwo[0], ":3:") || !strings.Contains(pageTwo[1], ":4:") {
		t.Fatalf("unexpected page 2: %#v (%q)", pageTwo, second.Content)
	}
	if second.Metadata["next_offset"] != 4 {
		t.Fatalf("expected next_offset=4, got %#v", second.Metadata["next_offset"])
	}
	if !strings.Contains(second.Content, "next_offset=4") {
		t.Fatalf("expected pagination notice, got %q", second.Content)
	}
}

func TestGrepOffsetBeyondWindowGivesNextAction(t *testing.T) {
	tool := stubGrepToolWithLines(t, []string{"src/file.txt:1:needle"})

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"pattern":    "needle",
		"head_limit": 2,
		"offset":     500,
	})
	if err != nil || result == nil || !result.Success {
		t.Fatalf("deep offset failed: %v %+v", err, result)
	}
	nextAction, _ := result.Metadata["next_action"].(string)
	if !strings.Contains(nextAction, "offset 超出") {
		t.Fatalf("expected offset guidance, got %q (%#v)", nextAction, result.Metadata)
	}
}

func TestGrepWalkerPaginationIsConsistent(t *testing.T) {
	root := t.TempDir()
	body := strings.Join([]string{"needle one", "keep", "needle two", "keep", "needle three"}, "\n")
	if err := os.WriteFile(filepath.Join(root, "match.txt"), []byte(body), 0o644); err != nil {
		t.Fatalf("seed file: %v", err)
	}

	tool := NewGrepTool()
	tool.SetBasePath(root)
	// 强制走内置 walker，覆盖 walkerSearchContent 的 collectLimit 分页。
	tool.lookPath = func(string) (string, error) { return "", os.ErrNotExist }

	first, err := tool.Execute(context.Background(), map[string]interface{}{
		"pattern":    "needle",
		"path":       root,
		"head_limit": 2,
	})
	if err != nil || first == nil || !first.Success {
		t.Fatalf("walker page 1 failed: %v %+v", err, first)
	}
	pageOne := grepPayloadLines(t, first.Content)
	if len(pageOne) != 2 {
		t.Fatalf("expected 2 lines on page 1, got %#v (%q)", pageOne, first.Content)
	}
	if first.Metadata["has_more"] != true || first.Metadata["next_offset"] != 2 {
		t.Fatalf("unexpected page 1 metadata: %#v", first.Metadata)
	}

	second, err := tool.Execute(context.Background(), map[string]interface{}{
		"pattern":    "needle",
		"path":       root,
		"head_limit": 2,
		"offset":     2,
	})
	if err != nil || second == nil || !second.Success {
		t.Fatalf("walker page 2 failed: %v %+v", err, second)
	}
	pageTwo := grepPayloadLines(t, second.Content)
	if len(pageTwo) != 1 {
		t.Fatalf("expected 1 remaining line on page 2, got %#v (%q)", pageTwo, second.Content)
	}
	if pageTwo[0] == pageOne[0] {
		t.Fatalf("page 2 repeated page 1 content: %q", pageTwo[0])
	}
}

// TestGrepDefaultCapNoticePublishesNextOffset pins the default-path truncation
// notice: when the built-in maxMatches cap (100) cuts a search, the notice must
// publish next_offset like the explicit-pagination path does. Without it the
// model reads the notice as data loss and keeps re-issuing reworded searches
// instead of paging with offset/head_limit.
func TestGrepDefaultCapNoticePublishesNextOffset(t *testing.T) {
	lines := make([]string, 0, 105)
	for index := 1; index <= 105; index++ {
		lines = append(lines, fmt.Sprintf("src/file.txt:%d:needle %d", index, index))
	}
	tool := stubGrepToolWithLines(t, lines)

	result, err := tool.Execute(context.Background(), map[string]interface{}{"pattern": "needle"})
	if err != nil || result == nil || !result.Success {
		t.Fatalf("default grep failed: %v %+v", err, result)
	}
	if !strings.Contains(result.Content, "已分页返回前 100 个匹配") || !strings.Contains(result.Content, "next_offset=100") {
		t.Fatalf("default cap notice lost the paging/continuation contract: %q", result.Content)
	}
	if page := grepPayloadLines(t, result.Content); len(page) != 100 {
		t.Fatalf("expected the first 100 matches in the default window, got %d", len(page))
	}
}
