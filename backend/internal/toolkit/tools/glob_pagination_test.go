package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func seedGlobTestFiles(t *testing.T, dir string, files map[string]time.Time) {
	t.Helper()
	for name, mod := range files {
		full := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(full, []byte("x"), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
		if err := os.Chtimes(full, mod, mod); err != nil {
			t.Fatalf("chtimes: %v", err)
		}
	}
}

func TestGlobPaginationOrdersByModTime(t *testing.T) {
	root := t.TempDir()
	base := time.Now().Add(-2 * time.Hour)
	seedGlobTestFiles(t, root, map[string]time.Time{
		"old.txt": base,
		"mid.txt": base.Add(30 * time.Minute),
		"new.txt": base.Add(60 * time.Minute),
	})

	tool := NewGlobTool()
	tool.SetBasePath(root)

	first, err := tool.Execute(context.Background(), map[string]interface{}{"pattern": "*.txt", "limit": 2})
	if err != nil || first == nil || !first.Success {
		t.Fatalf("glob page 1 failed: %v %+v", err, first)
	}
	files, _ := first.Metadata["files"].([]string)
	if len(files) != 2 || files[0] != "new.txt" || files[1] != "mid.txt" {
		t.Fatalf("expected mtime-ordered first page [new.txt mid.txt], got %#v", files)
	}
	if first.Metadata["has_more"] != true {
		t.Fatalf("expected has_more=true on first page, got %#v", first.Metadata["has_more"])
	}
	if first.Metadata["next_offset"] != 2 {
		t.Fatalf("expected next_offset=2, got %#v", first.Metadata["next_offset"])
	}
	if !strings.Contains(first.Content, "next_offset=2") {
		t.Fatalf("expected pagination notice in content, got %q", first.Content)
	}

	second, err := tool.Execute(context.Background(), map[string]interface{}{"pattern": "*.txt", "limit": 2, "offset": 2})
	if err != nil || second == nil || !second.Success {
		t.Fatalf("glob page 2 failed: %v %+v", err, second)
	}
	files, _ = second.Metadata["files"].([]string)
	if len(files) != 1 || files[0] != "old.txt" {
		t.Fatalf("expected second page [old.txt], got %#v", files)
	}
	if second.Metadata["has_more"] != false {
		t.Fatalf("expected has_more=false on last page, got %#v", second.Metadata["has_more"])
	}
}

func TestGlobOffsetBeyondWindowGivesNextAction(t *testing.T) {
	root := t.TempDir()
	seedGlobTestFiles(t, root, map[string]time.Time{"only.txt": time.Now()})

	tool := NewGlobTool()
	tool.SetBasePath(root)
	result, err := tool.Execute(context.Background(), map[string]interface{}{"pattern": "*.txt", "offset": 50})
	if err != nil || result == nil || !result.Success {
		t.Fatalf("glob deep offset failed: %v %+v", err, result)
	}
	files, _ := result.Metadata["files"].([]string)
	if len(files) != 0 {
		t.Fatalf("expected empty page, got %#v", files)
	}
	nextAction, _ := result.Metadata["next_action"].(string)
	if !strings.Contains(nextAction, "offset 超出") {
		t.Fatalf("expected offset guidance in next_action, got %q (metadata %#v)", nextAction, result.Metadata)
	}
}

func TestGlobTimeoutReturnsPartialResultsInsteadOfFailure(t *testing.T) {
	root := t.TempDir()
	seedGlobTestFiles(t, root, map[string]time.Time{"a.txt": time.Now()})

	tool := NewGlobTool()
	tool.SetBasePath(root)
	// 强制走内置 walker，避免 rg 分支在不同环境下的差异。
	tool.lookPath = func(string) (string, error) { return "", os.ErrNotExist }

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // 预算立即耗尽：应返回部分结果而不是失败

	result, err := tool.Execute(ctx, map[string]interface{}{"pattern": "**/*.txt"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result == nil || !result.Success {
		t.Fatalf("expected partial success on budget expiry, got %+v", result)
	}
	if result.Metadata["timed_out"] != true {
		t.Fatalf("expected timed_out=true, got %#v", result.Metadata)
	}
	if !strings.Contains(result.Content, "搜索预算") {
		t.Fatalf("expected budget notice in content, got %q", result.Content)
	}
}

func TestGlobWalkKeepsCollectedMatchesOnBudgetExpiry(t *testing.T) {
	root := t.TempDir()
	seedGlobTestFiles(t, root, map[string]time.Time{"a.txt": time.Now()})

	tool := NewGlobTool()
	tool.SetBasePath(root)
	matches := []string{"already-collected.txt"}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	truncated, timedOut, err := tool.walkGlobTree(ctx, root, nil, compileGlobPattern("**/*.txt"), &matches, 100, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !timedOut || !truncated {
		t.Fatalf("expected timedOut+truncated on expired budget, got timedOut=%v truncated=%v", timedOut, truncated)
	}
	if len(matches) != 1 || matches[0] != "already-collected.txt" {
		t.Fatalf("expected already collected matches preserved, got %#v", matches)
	}
}

func TestParseGlobOffsetRejectsNegative(t *testing.T) {
	if _, err := parseGlobOffset(-1); err == nil {
		t.Fatal("expected negative offset to be rejected")
	}
	if value, err := parseGlobOffset(7); err != nil || value != 7 {
		t.Fatalf("expected 7, got %d (err %v)", value, err)
	}
	if value, err := parseGlobOffset(globFetchHardCap + 100); err != nil || value != globFetchHardCap {
		t.Fatalf("expected offset clamped to %d, got %d (err %v)", globFetchHardCap, value, err)
	}
}
