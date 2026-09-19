package tools

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/wwsheng009/ai-agent-runtime/internal/toolresult"
)

func TestViewTool_DescriptionAndSchemaSupportBatchReads(t *testing.T) {
	tool := NewViewTool()

	desc := tool.Description()
	if !strings.Contains(desc, "多个文件") || !strings.Contains(desc, "files") {
		t.Fatalf("expected view description to advertise batch reads, got %q", desc)
	}

	params := tool.Parameters()
	props, ok := params["properties"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected properties in schema, got %#v", params)
	}
	pathSchema, ok := props["file_path"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected file_path schema in properties, got %#v", props)
	}
	pathDesc, _ := pathSchema["description"].(string)
	if !strings.Contains(pathDesc, "files") {
		t.Fatalf("expected file_path description to point to batch mode, got %q", pathDesc)
	}
	filesSchema, ok := props["files"].(map[string]interface{})
	if !ok || filesSchema["type"] != "array" {
		t.Fatalf("expected files array schema, got %#v", props["files"])
	}
	compactSchema, ok := props["compact"].(map[string]interface{})
	if !ok || compactSchema["type"] != "boolean" {
		t.Fatalf("expected compact boolean schema, got %#v", props["compact"])
	}
}

func TestViewTool_OutputPreservesLinesAndAddsLineNumbers(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "notes.txt"), []byte("one\ntwo\nthree\n"), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}
	tool := NewViewTool()
	tool.SetBasePath(root)
	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"file_path": "notes.txt",
		"offset":    1,
		"limit":     2,
	})
	if err != nil || !result.Success {
		t.Fatalf("expected successful read, result=%#v err=%v", result, err)
	}
	if result.Content != "2: two\n3: three" {
		t.Fatalf("expected stable line-oriented output, got %q", result.Content)
	}
}

func TestViewTool_BatchReadsReturnSuccessfulFilesAndPartialErrors(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("alpha\n"), 0o644); err != nil {
		t.Fatalf("write a: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "b.txt"), []byte("bravo\ncharlie\n"), 0o644); err != nil {
		t.Fatalf("write b: %v", err)
	}
	tool := NewViewTool()
	tool.SetBasePath(root)
	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"files": []interface{}{
			map[string]interface{}{"file_path": "a.txt", "limit": 1},
			map[string]interface{}{"file_path": "b.txt", "offset": 1, "limit": 1},
			map[string]interface{}{"file_path": "missing.txt"},
		},
	})
	if err != nil || !result.Success {
		t.Fatalf("expected partial batch success, result=%#v err=%v", result, err)
	}
	for _, want := range []string{"===== a.txt =====", "1: alpha", "===== b.txt =====", "2: charlie", "===== errors =====", "missing.txt"} {
		if !strings.Contains(result.Content, want) {
			t.Fatalf("expected batch output to contain %q, got %q", want, result.Content)
		}
	}
	if result.Metadata["succeeded_count"] != 2 || result.Metadata["failed_count"] != 1 || result.Metadata["partial_failure"] != true {
		t.Fatalf("unexpected batch metadata: %#v", result.Metadata)
	}
	rawFailed, ok := result.Metadata[toolresult.MetadataFailedItemsKey].([]map[string]interface{})
	if !ok || len(rawFailed) != 1 {
		t.Fatalf("expected structured failed_items, got %#v", result.Metadata[toolresult.MetadataFailedItemsKey])
	}
	if rawFailed[0]["path"] != "missing.txt" {
		t.Fatalf("expected missing.txt failed item, got %#v", rawFailed[0])
	}
	if idx, ok := rawFailed[0]["index"].(int); !ok || idx != 2 {
		t.Fatalf("expected failed item index=2, got %#v", rawFailed[0]["index"])
	}
	// 只有 missing.txt（索引 2）未显式指定 limit，应被记入 defaulted。
	if applied, ok := result.Metadata["batch_default_limit_applied"].([]int); !ok || len(applied) != 1 || applied[0] != 2 {
		t.Fatalf("expected defaulted index [2] for missing.txt, got %#v", result.Metadata["batch_default_limit_applied"])
	}
}

func TestViewTool_BatchAppliesSmallerDefaultLimit(t *testing.T) {
	root := t.TempDir()
	var b strings.Builder
	for i := 1; i <= 300; i++ {
		fmt.Fprintf(&b, "line-%d\n", i)
	}
	if err := os.WriteFile(filepath.Join(root, "big.txt"), []byte(b.String()), 0o644); err != nil {
		t.Fatalf("write big: %v", err)
	}
	tool := NewViewTool()
	tool.SetBasePath(root)
	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"files": []interface{}{
			map[string]interface{}{"file_path": "big.txt"},
		},
	})
	if err != nil || !result.Success {
		t.Fatalf("expected batch success, result=%#v err=%v", result, err)
	}
	if strings.Contains(result.Content, "300: line-300") {
		t.Fatalf("expected batch default limit to cap per-item output, got full file: %q", result.Content)
	}
	if !strings.Contains(result.Content, "200: line-200") {
		t.Fatalf("expected batch default window up to line 200, got %q", result.Content)
	}
	applied, ok := result.Metadata["batch_default_limit_applied"].([]int)
	if !ok || len(applied) != 1 || applied[0] != 0 {
		t.Fatalf("expected defaulted index [0], got %#v", result.Metadata["batch_default_limit_applied"])
	}
	items, ok := result.Metadata["items"].([]map[string]interface{})
	if !ok || len(items) != 1 {
		t.Fatalf("expected 1 item metadata, got %#v", result.Metadata["items"])
	}
	if limit, _ := items[0]["limit"].(int); limit != viewBatchDefaultLimit {
		t.Fatalf("expected item limit=%d, got %#v", viewBatchDefaultLimit, items[0]["limit"])
	}
	if trunc, _ := items[0]["is_truncated"].(bool); !trunc {
		t.Fatalf("expected truncated metadata on capped batch item, got %#v", items[0])
	}
	if next, _ := items[0]["suggested_next_offset"].(int); next != viewBatchDefaultLimit {
		t.Fatalf("expected suggested_next_offset=%d, got %#v", viewBatchDefaultLimit, items[0]["suggested_next_offset"])
	}
}

func TestViewTool_BatchCompactMode(t *testing.T) {
	root := t.TempDir()
	var b strings.Builder
	for i := 1; i <= 30; i++ {
		fmt.Fprintf(&b, "c%d\n", i)
	}
	if err := os.WriteFile(filepath.Join(root, "c.txt"), []byte(b.String()), 0o644); err != nil {
		t.Fatalf("write c: %v", err)
	}
	tool := NewViewTool()
	tool.SetBasePath(root)
	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"compact": true,
		"files": []interface{}{
			map[string]interface{}{"file_path": "c.txt"},
			map[string]interface{}{"file_path": "c.txt", "offset": 10, "limit": 20},
		},
	})
	if err != nil || !result.Success {
		t.Fatalf("expected compact batch success, result=%#v err=%v", result, err)
	}
	if compact, _ := result.Metadata["compact"].(bool); !compact {
		t.Fatalf("expected compact metadata true, got %#v", result.Metadata["compact"])
	}
	// 第一项：未显式 limit 的默认窗口被压缩到 compactHeadLines（行 1-10）。
	if !strings.Contains(result.Content, "1: c1") || !strings.Contains(result.Content, "10: c10") {
		t.Fatalf("expected compact head window lines 1-10, got %q", result.Content)
	}
	// 第二项：显式 limit=20 仍被压缩到 10 行（offset=10 → 行 11-20）。
	// "11: c11" 只允许出现一次（由第二项提供），证明默认项停在 10 行。
	if strings.Count(result.Content, "11: c11") != 1 || !strings.Contains(result.Content, "20: c20") {
		t.Fatalf("expected compact window lines 11-20 for explicit range, got %q", result.Content)
	}
	if strings.Contains(result.Content, "21: c21") {
		t.Fatalf("expected compact to cap explicit range at 10 lines, got %q", result.Content)
	}
	// 摘要行携带继续读的元数据。
	if !strings.Contains(result.Content, "suggested_next_offset=10") {
		t.Fatalf("expected compact summary with suggested_next_offset=10, got %q", result.Content)
	}
	if !strings.Contains(result.Content, "suggested_next_offset=20") {
		t.Fatalf("expected compact summary with suggested_next_offset=20, got %q", result.Content)
	}
	// 未显式 limit 的项（索引 0）记入 batch_default_limit_applied。
	applied, ok := result.Metadata["batch_default_limit_applied"].([]int)
	if !ok || len(applied) != 1 || applied[0] != 0 {
		t.Fatalf("expected defaulted index [0], got %#v", result.Metadata["batch_default_limit_applied"])
	}
}

func TestViewTool_LongUnicodeLineIsReadableAndTruncatedSafely(t *testing.T) {
	root := t.TempDir()
	line := strings.Repeat("界", 25000)
	if err := os.WriteFile(filepath.Join(root, "long.txt"), []byte(line+"\n"), 0o644); err != nil {
		t.Fatalf("write long line: %v", err)
	}
	tool := NewViewTool()
	tool.SetBasePath(root)
	result, err := tool.Execute(context.Background(), map[string]interface{}{"file_path": "long.txt", "limit": 1})
	if err != nil || !result.Success {
		t.Fatalf("expected long line read to succeed, result=%#v err=%v", result, err)
	}
	if !utf8.ValidString(result.Content) {
		t.Fatalf("expected valid UTF-8 after truncation")
	}
	// Honest truncation: the marker reports the hidden remainder instead of
	// a silent "..." (plan §10.6).
	if !strings.HasSuffix(result.Content, "]") {
		t.Fatalf("expected long line truncation marker, got suffix %q", result.Content[len(result.Content)-10:])
	}
	if got, _ := result.Metadata["long_lines_truncated"].(int); got != 1 {
		t.Fatalf("expected long_lines_truncated=1, got %#v", result.Metadata["long_lines_truncated"])
	}
	if got, _ := result.Metadata["hidden_bytes"].(int); got <= 0 {
		t.Fatalf("expected hidden_bytes > 0, got %#v", result.Metadata["hidden_bytes"])
	}
}

func TestViewTool_LargeFileCanBeReadInSmallRanges(t *testing.T) {
	root := t.TempDir()
	content := append([]byte("one\ntwo\n"), bytes.Repeat([]byte("x\n"), 3*1024*1024)...)
	if err := os.WriteFile(filepath.Join(root, "large.log"), content, 0o644); err != nil {
		t.Fatalf("write large file: %v", err)
	}
	tool := NewViewTool()
	tool.SetBasePath(root)
	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"file_path": "large.log",
		"limit":     1,
	})
	if err != nil || !result.Success {
		t.Fatalf("expected range read from large file, result=%#v err=%v", result, err)
	}
	if result.Content != "1: one" || result.Metadata["is_truncated"] != true {
		t.Fatalf("unexpected large-file range result: content=%q metadata=%#v", result.Content, result.Metadata)
	}
	if next, ok := result.Metadata["suggested_next_offset"].(int); !ok || next != 1 {
		t.Fatalf("expected suggested_next_offset=1 for truncated small window, got %#v", result.Metadata["suggested_next_offset"])
	}
	if _, ok := result.Metadata["efficiency_advisory"]; ok {
		t.Fatalf("small explicit limit should not stamp efficiency_advisory, got %#v", result.Metadata)
	}
}

func TestViewTool_DefaultWindowTruncationEmitsEfficiencyAdvisory(t *testing.T) {
	root := t.TempDir()
	var body strings.Builder
	for i := 0; i < viewDefaultLimit+5; i++ {
		fmt.Fprintf(&body, "line-%d\n", i+1)
	}
	if err := os.WriteFile(filepath.Join(root, "big.txt"), []byte(body.String()), 0o644); err != nil {
		t.Fatalf("write big file: %v", err)
	}
	tool := NewViewTool()
	tool.SetBasePath(root)
	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"file_path": "big.txt",
		// omit limit so default window applies
	})
	if err != nil || !result.Success {
		t.Fatalf("expected default-window read success, result=%#v err=%v", result, err)
	}
	if result.Metadata["is_truncated"] != true {
		t.Fatalf("expected truncated default window, got %#v", result.Metadata)
	}
	if next, ok := result.Metadata["suggested_next_offset"].(int); !ok || next != viewDefaultLimit {
		t.Fatalf("expected suggested_next_offset=%d, got %#v", viewDefaultLimit, result.Metadata["suggested_next_offset"])
	}
	if got, _ := result.Metadata["efficiency_advisory"].(string); got != "prefer_offset_limit" {
		t.Fatalf("expected efficiency_advisory=prefer_offset_limit, got %#v", result.Metadata)
	}
	if !strings.Contains(result.Content, "[efficiency]") || !strings.Contains(result.Content, "offset=") {
		t.Fatalf("expected efficiency advisory text in content, got %q", result.Content)
	}
}

// TestViewTool_DefaultWindowStaysWithinByteBudget pins P0-1: a default-size
// window over a wide-content file must stop inside the model-visible byte
// budget (byte_budget_applied=true), never relying on the L4 echo layer fold.
func TestViewTool_DefaultWindowStaysWithinByteBudget(t *testing.T) {
	root := t.TempDir()
	budget := viewByteBudgetBytes()
	var body strings.Builder
	line := strings.Repeat("a", 300)
	for i := 0; i < 800; i++ {
		fmt.Fprintf(&body, "%s\n", line)
	}
	if err := os.WriteFile(filepath.Join(root, "wide.txt"), []byte(body.String()), 0o644); err != nil {
		t.Fatalf("write wide file: %v", err)
	}
	tool := NewViewTool()
	tool.SetBasePath(root)
	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"file_path": "wide.txt",
	})
	if err != nil || !result.Success {
		t.Fatalf("expected success, result=%#v err=%v", result, err)
	}
	if result.Metadata["is_truncated"] != true {
		t.Fatalf("expected byte-budget truncated window, got %#v", result.Metadata)
	}
	if result.Metadata["byte_budget_applied"] != true {
		t.Fatalf("expected byte_budget_applied=true, got %#v", result.Metadata)
	}
	next, ok := result.Metadata["suggested_next_offset"].(int)
	if !ok || next <= 0 || next >= 800 {
		t.Fatalf("expected suggested_next_offset pointing before EOF (line 800), got %#v", result.Metadata["suggested_next_offset"])
	}
	if len(result.Content) > budget+4096 { // advisory tail allowance
		t.Fatalf("content %d bytes exceeds byte budget %d (+advisory)", len(result.Content), budget)
	}
}

func TestViewTool_PathNotFoundIncludesCandidateHint(t *testing.T) {
	root := t.TempDir()
	candidate := filepath.Join(root, "project", "settings", "file.txt")
	if err := os.MkdirAll(filepath.Dir(candidate), 0o755); err != nil {
		t.Fatalf("mkdir candidate tree: %v", err)
	}
	if err := os.WriteFile(candidate, []byte("ok"), 0o644); err != nil {
		t.Fatalf("write candidate file: %v", err)
	}

	tool := NewViewTool()
	tool.SetBasePath(root)
	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"file_path": "project/setting/file.txt",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Success {
		t.Fatalf("expected failure, got success with content %q", result.Content)
	}
	if result.Error == nil {
		t.Fatal("expected path error, got nil")
	}
	hint := result.Error.Error()
	if !strings.Contains(hint, candidate) {
		t.Fatalf("expected candidate path %q in hint, got %q", candidate, hint)
	}
}

func TestViewTool_DirectoryPathAutoListsContents(t *testing.T) {
	root := t.TempDir()
	dirPath := filepath.Join(root, "project", "setting")
	if err := os.MkdirAll(dirPath, 0o755); err != nil {
		t.Fatalf("mkdir directory path: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dirPath, "token.go"), []byte("package types\n"), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(dirPath, "sub"), 0o755); err != nil {
		t.Fatalf("mkdir sub: %v", err)
	}

	tool := NewViewTool()
	tool.SetBasePath(root)
	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"file_path": "project/setting",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected auto-list success, got error %v content %q", result.Error, result.Content)
	}
	if !strings.Contains(result.Content, "路径是目录，不是文件") {
		t.Fatalf("expected directory notice, got %q", result.Content)
	}
	if !strings.Contains(result.Content, "token.go") {
		t.Fatalf("expected listed file token.go, got %q", result.Content)
	}
	if result.Metadata["is_directory"] != true || result.Metadata["auto_listed"] != true {
		t.Fatalf("expected directory auto-list metadata, got %#v", result.Metadata)
	}
}

func TestViewTool_OffsetBeyondEOFReturnsExplicitMessage(t *testing.T) {
	root := t.TempDir()
	filePath := filepath.Join(root, "notes.txt")
	if err := os.WriteFile(filePath, []byte("one\ntwo\nthree\n"), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}

	tool := NewViewTool()
	tool.SetBasePath(root)
	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"file_path": "notes.txt",
		"offset":    float64(10),
		"limit":     float64(5),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected success, got error %v", result.Error)
	}
	if !strings.Contains(result.Content, "Reached end of file: offset 10 is beyond total lines 3.") {
		t.Fatalf("expected explicit EOF message, got %q", result.Content)
	}
	if result.Metadata["total_lines"] != 3 {
		t.Fatalf("expected total_lines metadata 3, got %#v", result.Metadata["total_lines"])
	}
	if result.Metadata["eof"] != true {
		t.Fatalf("expected eof metadata true, got %#v", result.Metadata["eof"])
	}
}

func TestViewTool_TruncatedReadDoesNotRequireTotalLineCount(t *testing.T) {
	root := t.TempDir()
	filePath := filepath.Join(root, "notes.txt")
	if err := os.WriteFile(filePath, []byte("one\ntwo\nthree\nfour\n"), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}

	tool := NewViewTool()
	tool.SetBasePath(root)
	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"file_path": "notes.txt",
		"offset":    float64(0),
		"limit":     float64(2),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected success, got error %v", result.Error)
	}
	if result.Metadata["is_truncated"] != true {
		t.Fatalf("expected truncated metadata true, got %#v", result.Metadata["is_truncated"])
	}
	if _, ok := result.Metadata["total_lines"]; ok {
		t.Fatalf("did not expect total_lines on truncated read, got %#v", result.Metadata["total_lines"])
	}
}

func TestViewTool_OffsetAtEOFReturnsExplicitMessage(t *testing.T) {
	root := t.TempDir()
	filePath := filepath.Join(root, "notes.txt")
	if err := os.WriteFile(filePath, []byte("one\ntwo\nthree\n"), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}

	tool := NewViewTool()
	tool.SetBasePath(root)
	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"file_path": "notes.txt",
		"offset":    float64(3),
		"limit":     float64(5),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected success, got error %v", result.Error)
	}
	if !strings.Contains(result.Content, "Reached end of file: offset 3 equals total lines 3.") {
		t.Fatalf("expected exact EOF message, got %q", result.Content)
	}
	if result.Metadata["total_lines"] != 3 {
		t.Fatalf("expected total_lines metadata 3, got %#v", result.Metadata["total_lines"])
	}
	if result.Metadata["is_truncated"] != false {
		t.Fatalf("expected truncated metadata false, got %#v", result.Metadata["is_truncated"])
	}
	if result.Metadata["eof"] != true {
		t.Fatalf("expected eof metadata true, got %#v", result.Metadata["eof"])
	}
}
