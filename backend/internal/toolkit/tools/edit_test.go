package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEditTool_PathNotFoundIncludesCandidateHint(t *testing.T) {
	root := t.TempDir()
	candidate := filepath.Join(root, "project", "settings", "runtime.yaml")
	if err := os.MkdirAll(filepath.Dir(candidate), 0o755); err != nil {
		t.Fatalf("mkdir candidate tree: %v", err)
	}
	if err := os.WriteFile(candidate, []byte("old"), 0o644); err != nil {
		t.Fatalf("write candidate file: %v", err)
	}

	tool := NewEditTool()
	tool.SetBasePath(root)
	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"file_path":   "project/setting/runtime.yaml",
		"old_string":  "old",
		"new_string":  "new",
		"replace_all": false,
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

func TestEditTool_DirectoryPathIncludesKindMismatchHint(t *testing.T) {
	root := t.TempDir()
	candidate := filepath.Join(root, "project", "settings")
	if err := os.MkdirAll(candidate, 0o755); err != nil {
		t.Fatalf("mkdir candidate tree: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(root, "project", "setting"), 0o755); err != nil {
		t.Fatalf("mkdir directory path: %v", err)
	}

	tool := NewEditTool()
	tool.SetBasePath(root)
	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"file_path":  "project/setting",
		"old_string": "old",
		"new_string": "new",
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
	if !strings.Contains(hint, "路径是目录，不是文件") {
		t.Fatalf("expected kind mismatch guidance, got %q", hint)
	}
	if !strings.Contains(hint, candidate) {
		t.Fatalf("expected candidate path %q in hint, got %q", candidate, hint)
	}
}

func TestEditTool_NotFoundGuidesApplyPatch(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "README.md")
	if err := os.WriteFile(path, []byte("current text\n"), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}

	tool := NewEditTool()
	tool.SetBasePath(root)
	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"file_path":  "README.md",
		"old_string": "stale text",
		"new_string": "updated text",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Success {
		t.Fatalf("expected failure, got success with content %q", result.Content)
	}
	if result.Error == nil {
		t.Fatal("expected old_string error, got nil")
	}
	message := result.Error.Error()
	if !strings.Contains(message, "apply_patch") || !strings.Contains(message, "view/grep") {
		t.Fatalf("expected apply_patch/view guidance, got %q", message)
	}
	if !strings.Contains(message, "old_string 预览") {
		t.Fatalf("expected old_string preview, got %q", message)
	}
	if !strings.Contains(message, "next_action") {
		t.Fatalf("expected next_action text in error message, got %q", message)
	}
	next, _ := result.Metadata["next_action"].(string)
	if !strings.Contains(next, "apply_patch") && !strings.Contains(next, "view") {
		t.Fatalf("expected structured next_action metadata, got %q metadata=%#v", next, result.Metadata)
	}
	if code, _ := result.Metadata["error_code"].(string); code != "STALE_CONTEXT" {
		t.Fatalf("expected STALE_CONTEXT error_code, got %#v", result.Metadata)
	}
	if retryable, _ := result.Metadata["retryable"].(bool); retryable {
		t.Fatalf("STALE_CONTEXT must not be retryable, got %#v", result.Metadata)
	}
}

func TestEditTool_NotFoundDetectsLineEndingDifference(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "crlf.txt")
	if err := os.WriteFile(path, []byte("alpha\r\nbeta\r\n"), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}

	tool := NewEditTool()
	tool.SetBasePath(root)
	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"file_path":  "crlf.txt",
		"old_string": "alpha\nbeta\n",
		"new_string": "updated\n",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected CRLF/LF auto-heal success, got error: %v content=%q", result.Error, result.Content)
	}
	data, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatalf("read file: %v", readErr)
	}
	if got := string(data); got != "updated\r\n" {
		t.Fatalf("expected CRLF-preserving replacement, got %q", got)
	}
}

func TestEditTool_NotFoundIncludesClosestSnippet(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "snippet.txt")
	if err := os.WriteFile(path, []byte("func HelloWorld() {\n\treturn 1\n}\n"), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}

	tool := NewEditTool()
	tool.SetBasePath(root)
	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"file_path":  "snippet.txt",
		"old_string": "func HelloWord() {\n\treturn 2\n}",
		"new_string": "func HelloWorld() {\n\treturn 2\n}",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Success {
		t.Fatalf("expected failure, got success")
	}
	message := result.Error.Error()
	if !strings.Contains(message, "最接近的当前内容") && !strings.Contains(message, "最接近片段") {
		t.Fatalf("expected closest snippet guidance, got %q", message)
	}
	if !strings.Contains(message, "func HelloWorld()") {
		t.Fatalf("expected copy-pasteable current lines in error body, got %q", message)
	}
	if code, _ := result.Metadata["error_code"].(string); code != "STALE_CONTEXT" {
		t.Fatalf("expected STALE_CONTEXT error_code, got %#v", result.Metadata)
	}
	if offset, ok := result.Metadata["suggested_view_offset"].(int); !ok || offset != 0 {
		t.Fatalf("expected suggested_view_offset=0 for first-line closest match, got %#v", result.Metadata)
	}
	if limit, ok := result.Metadata["suggested_view_limit"].(int); !ok || limit != 40 {
		t.Fatalf("expected suggested_view_limit=40, got %#v", result.Metadata)
	}
	snippet, _ := result.Metadata["current_snippet"].(string)
	if !strings.Contains(snippet, "func HelloWorld()") {
		t.Fatalf("expected current_snippet metadata with file lines, got %#v", result.Metadata["current_snippet"])
	}
	if start, ok := result.Metadata["current_snippet_start_line"].(int); !ok || start != 1 {
		t.Fatalf("expected current_snippet_start_line=1, got %#v", result.Metadata["current_snippet_start_line"])
	}
	if !strings.Contains(message, "suggested_view_offset=") {
		t.Fatalf("expected suggested_view_offset hint in error text, got %q", message)
	}
	next, _ := result.Metadata["next_action"].(string)
	if !strings.Contains(next, "current_snippet") {
		t.Fatalf("expected next_action to mention current_snippet, got %q", next)
	}
}

func TestEditTool_HealsCRLFOldStringAgainstLFFile(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "lf.txt")
	if err := os.WriteFile(path, []byte("alpha\nbeta\n"), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}

	tool := NewEditTool()
	tool.SetBasePath(root)
	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"file_path":  "lf.txt",
		"old_string": "alpha\r\nbeta\r\n",
		"new_string": "updated\r\n",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected CRLF old_string vs LF file auto-heal, got error: %v content=%q", result.Error, result.Content)
	}
	data, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatalf("read file: %v", readErr)
	}
	if got := string(data); got != "updated\n" {
		t.Fatalf("expected LF-preserving replacement, got %q", got)
	}
}

func TestEditTool_HealsMissingLeadingIndent(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "indent.go")
	// Model often drops the outer tab when copying from a nested block.
	original := "func TestFoo(t *testing.T) {\n\tif session.ChatExecutor == nil {\n\t\tt.Fatal(\"expected chat executor\")\n\t}\n}\n"
	if err := os.WriteFile(path, []byte(original), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}

	tool := NewEditTool()
	tool.SetBasePath(root)
	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"file_path": "indent.go",
		"old_string": "if session.ChatExecutor == nil {\n" +
			"\t\tt.Fatal(\"expected chat executor\")\n" +
			"\t}",
		"new_string": "if session.ChatExecutor == nil {\n" +
			"\t\tt.Fatal(\"expected chat executor ready\")\n" +
			"\t}",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected indent auto-heal success, got error: %v content=%q", result.Error, result.Content)
	}
	data, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatalf("read file: %v", readErr)
	}
	want := "func TestFoo(t *testing.T) {\n\tif session.ChatExecutor == nil {\n\t\tt.Fatal(\"expected chat executor ready\")\n\t}\n}\n"
	if got := string(data); got != want {
		t.Fatalf("indent heal mismatch\nwant:\n%q\ngot:\n%q", want, got)
	}
}

func TestEditTool_HealsTrailingWhitespacePerLine(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "trail.txt")
	original := "alpha  \nbeta\t\ngamma\n"
	if err := os.WriteFile(path, []byte(original), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}

	tool := NewEditTool()
	tool.SetBasePath(root)
	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"file_path":  "trail.txt",
		"old_string": "alpha\nbeta\ngamma",
		"new_string": "alpha\nbeta\nupdated",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected trailing-ws auto-heal success, got error: %v", result.Error)
	}
	data, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatalf("read file: %v", readErr)
	}
	// Unchanged lines keep the file's exact trailing whitespace; only the
	// changed line is rewritten from the model body.
	if got := string(data); got != "alpha  \nbeta\t\nupdated\n" {
		t.Fatalf("unexpected content after trailing-ws heal: %q", got)
	}
}

func TestEditTool_WhitespaceHealRequiresUniqueWindow(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "dup.txt")
	// Exact "alpha\nbeta" is not a substring (file lines are tab-indented), but
	// TrimSpace matching would hit two identical windows — must not auto-heal.
	original := "\talpha\n\tbeta\nmiddle\n\talpha\n\tbeta\n"
	if err := os.WriteFile(path, []byte(original), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}

	tool := NewEditTool()
	tool.SetBasePath(root)
	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"file_path":  "dup.txt",
		"old_string": "alpha\nbeta",
		"new_string": "alpha\nomega",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Success {
		t.Fatalf("expected ambiguous whitespace match to fail, got success content=%q", result.Content)
	}
	if code, _ := result.Metadata["error_code"].(string); code != "STALE_CONTEXT" {
		t.Fatalf("expected STALE_CONTEXT on ambiguous match, got %#v", result.Metadata)
	}
}

func TestEditTool_HealsIndentWithCRLFFile(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "crlf_indent.txt")
	original := "block:\r\n\tline one\r\n\tline two\r\n"
	if err := os.WriteFile(path, []byte(original), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}

	tool := NewEditTool()
	tool.SetBasePath(root)
	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"file_path":  "crlf_indent.txt",
		"old_string": "line one\nline two",
		"new_string": "line one\nline two updated",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected CRLF+indent heal, got error: %v", result.Error)
	}
	data, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatalf("read file: %v", readErr)
	}
	want := "block:\r\n\tline one\r\n\tline two updated\r\n"
	if got := string(data); got != want {
		t.Fatalf("CRLF indent heal mismatch\nwant %q\ngot  %q", want, got)
	}
}

func TestEditTool_HealsInternalColumnWhitespace(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "align.go")
	// File uses column-alignment padding; model often collapses to single spaces.
	original := "\tEventCheckpointCreated  Event = \"checkpoint_created\"\n" +
		"\tEventRewindCompleted    Event = \"rewind_completed\"\n" +
		"\tEventBacktrackCompleted Event = \"backtrack_completed\"\n"
	if err := os.WriteFile(path, []byte(original), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}

	tool := NewEditTool()
	tool.SetBasePath(root)
	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"file_path": "align.go",
		"old_string": "EventCheckpointCreated Event = \"checkpoint_created\"\n" +
			"\tEventRewindCompleted Event = \"rewind_completed\"\n" +
			")",
		"new_string": "EventCheckpointCreated Event = \"checkpoint_created\"\n" +
			"\tEventRewindCompleted Event = \"rewind_completed\"\n" +
			"\tEventBacktrackCompleted Event = \"backtrack_completed\"\n" +
			")",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Trailing ")" is true content drift — must still fail, not invent a match.
	if result.Success {
		t.Fatalf("expected failure when significant lines include non-file content, got success")
	}

	// Pure column padding (no extra significant line) should auto-heal.
	result, err = tool.Execute(context.Background(), map[string]interface{}{
		"file_path": "align.go",
		"old_string": "EventCheckpointCreated Event = \"checkpoint_created\"\n" +
			"\tEventRewindCompleted Event = \"rewind_completed\"\n" +
			"\tEventBacktrackCompleted Event = \"backtrack_completed\"",
		"new_string": "EventCheckpointCreated Event = \"checkpoint_created\"\n" +
			"\tEventRewindCompleted Event = \"rewind_completed\"\n" +
			"\tEventBacktrackCompleted Event = \"backtrack_completed\"\n" +
			"\tEventNew Event = \"new\"",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected internal column-ws auto-heal, got error: %v", result.Error)
	}
	data, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatalf("read file: %v", readErr)
	}
	// File indent/padding preserved on matched lines; new line inherits first-line indent.
	want := "\tEventCheckpointCreated  Event = \"checkpoint_created\"\n" +
		"\tEventRewindCompleted    Event = \"rewind_completed\"\n" +
		"\tEventBacktrackCompleted Event = \"backtrack_completed\"\n" +
		"\tEventNew Event = \"new\"\n"
	if got := string(data); got != want {
		t.Fatalf("column-ws heal mismatch\nwant:\n%q\ngot:\n%q", want, got)
	}
}

func TestEditTool_HealsBlankRunLengthDrift(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "blank.ts")
	// File has 3 blank lines between blocks; model often invents 4 or 5.
	original := "  return 'info'\n}\n\n\n\nfunction Sub2APIMonitorSection({\n  monitor,\n"
	if err := os.WriteFile(path, []byte(original), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}

	tool := NewEditTool()
	tool.SetBasePath(root)
	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"file_path": "blank.ts",
		"old_string": "return 'info'\n" +
			"}\n" +
			"\n\n\n\n" +
			"function Sub2APIMonitorSection({",
		"new_string": "return 'info'\n" +
			"}\n" +
			"\n\n\n\n" +
			"function Sub2APIMonitorSectionUpdated({",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected blank-run auto-heal, got error: %v", result.Error)
	}
	data, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatalf("read file: %v", readErr)
	}
	// File blank-run length preserved; indent on first line healed; function renamed.
	want := "  return 'info'\n}\n\n\n\nfunction Sub2APIMonitorSectionUpdated({\n  monitor,\n"
	if got := string(data); got != want {
		t.Fatalf("blank-run heal mismatch\nwant:\n%q\ngot:\n%q", want, got)
	}
}

func TestEditTool_ClosestSnippetPrefersMultiLineWindow(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "multi.go")
	// Generic first line "import (" appears twice; the true drift target is the
	// second block. First-line-only scoring historically anchored on line 1.
	original := strings.Join([]string{
		"package demo",
		"",
		"import (",
		"\t\"fmt\"",
		")",
		"",
		"func Unused() {}",
		"",
		"import (",
		"\t\"strings\"",
		"\t\"testing\"",
		")",
		"",
		"func Target() {",
		"\treturn strings.TrimSpace(\"x\")",
		"}",
		"",
	}, "\n")
	if err := os.WriteFile(path, []byte(original), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}

	tool := NewEditTool()
	tool.SetBasePath(root)
	// old_string first line is generic import (; distinctive later lines drift.
	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"file_path": "multi.go",
		"old_string": strings.Join([]string{
			"import (",
			"\t\"strings\"",
			"\t\"testing\"",
			")",
			"",
			"func Target() {",
			"\treturn strings.TrimSpace(\"y\")",
			"}",
		}, "\n"),
		"new_string": strings.Join([]string{
			"import (",
			"\t\"strings\"",
			"\t\"testing\"",
			")",
			"",
			"func Target() {",
			"\treturn strings.TrimSpace(\"z\")",
			"}",
		}, "\n"),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Success {
		t.Fatalf("expected true content drift to stay STALE, got success")
	}
	snippet, _ := result.Metadata["current_snippet"].(string)
	if !strings.Contains(snippet, "func Target()") {
		t.Fatalf("expected multi-line closest to anchor near Target, got %q meta=%#v", snippet, result.Metadata)
	}
	if strings.Contains(snippet, "\"fmt\"") && !strings.Contains(snippet, "\"strings\"") {
		t.Fatalf("expected second import block, not first generic import, got %q", snippet)
	}
	start, _ := result.Metadata["current_snippet_start_line"].(int)
	if start < 8 {
		t.Fatalf("expected start line near second block (>=8), got %d snippet=%q", start, snippet)
	}
	offset, _ := result.Metadata["suggested_view_offset"].(int)
	if offset != start-1 {
		t.Fatalf("expected suggested_view_offset=%d, got %#v", start-1, result.Metadata["suggested_view_offset"])
	}
	message := result.Error.Error()
	if !strings.Contains(message, "func Target()") {
		t.Fatalf("expected Target in error body closest block, got %q", message)
	}
}

func TestFindClosestEditSnippetWithLine_AnchorsOnLaterDistinctiveLine(t *testing.T) {
	content := strings.Join([]string{
		"import (",
		"\t\"fmt\"",
		")",
		"",
		"func Other() {}",
		"",
		"func HelloWorld() {",
		"\treturn 1",
		"}",
	}, "\n")
	// First line completely wrong/generic; later lines nearly match.
	old := strings.Join([]string{
		"import (",
		"\t\"missing\"",
		")",
		"",
		"func HelloWord() {",
		"\treturn 1",
		"}",
	}, "\n")
	snippet, start := findClosestEditSnippetWithLine(content, old)
	if start < 6 {
		t.Fatalf("expected tightened anchor near HelloWorld block, got start=%d snippet=%q", start, snippet)
	}
	if !strings.Contains(snippet, "func HelloWorld()") {
		t.Fatalf("expected HelloWorld in closest snippet, got %q", snippet)
	}
	// Weak prefix from forced full-window alignment must not dominate recovery.
	if strings.HasPrefix(strings.TrimSpace(snippet), ")") {
		t.Fatalf("expected trimmed weak leading lines, got %q", snippet)
	}
}

func TestFindClosestEditSnippetWithLine_TokenFallbackWhenFirstLineGone(t *testing.T) {
	// Live residual: model invents a schema block (max_targets/match) while the
	// file only has a differently shaped selector id. Multi-line window score
	// falls under 0.45; token fallback must still surface the real id site.
	content := strings.Join([]string{
		"providers:",
		"  - name: demo",
		"routing:",
		"  actions:",
		"    - id: rewrite",
		"      selectors:",
		"        - id: custom-tool-call-namespace",
		"          type: json_targets",
		"          json_targets:",
		"            container_path: input",
		"            mode: array_items",
		"        - id: other-namespace",
		"          type: path",
		"misc: true",
	}, "\n")
	old := strings.Join([]string{
		"max_targets: 128",
		"      match:",
		"        type: selector",
		"        selector:",
		"          selector_id: custom-tool-call-namespace",
		"          operator: exists",
	}, "\n")
	snippet, start := findClosestEditSnippetWithLine(content, old)
	if snippet == "" || start <= 0 {
		t.Fatalf("expected token-fallback closest snippet, got start=%d snippet=%q", start, snippet)
	}
	if !strings.Contains(snippet, "custom-tool-call-namespace") {
		t.Fatalf("expected distinctive id in closest snippet, got %q", snippet)
	}
	// Prefer the real selector block, not file head noise.
	if start < 5 {
		t.Fatalf("expected anchor near selectors block, got start=%d snippet=%q", start, snippet)
	}
}

func TestEditTool_NotFoundTokenFallbackExportsCurrentSnippet(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "guide.md")
	body := strings.Join([]string{
		"# guide",
		"",
		"routing:",
		"  actions:",
		"    - id: rewrite",
		"      selectors:",
		"        - id: custom-tool-call-namespace",
		"          type: json_targets",
		"",
	}, "\n")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}
	tool := NewEditTool()
	tool.SetBasePath(root)
	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"file_path": "guide.md",
		"old_string": strings.Join([]string{
			"max_targets: 128",
			"      match:",
			"        type: selector",
			"        selector:",
			"          selector_id: custom-tool-call-namespace",
			"          operator: exists",
		}, "\n"),
		"new_string": "max_targets: 64\n",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Success {
		t.Fatalf("expected STALE failure, got success")
	}
	snippet, _ := result.Metadata["current_snippet"].(string)
	if !strings.Contains(snippet, "custom-tool-call-namespace") {
		t.Fatalf("expected current_snippet from token fallback, got %#v meta=%#v err=%v",
			result.Metadata["current_snippet"], result.Metadata, result.Error)
	}
	if start, _ := result.Metadata["current_snippet_start_line"].(int); start <= 0 {
		t.Fatalf("expected current_snippet_start_line > 0, got %#v", result.Metadata["current_snippet_start_line"])
	}
	message := ""
	if result.Error != nil {
		message = result.Error.Error()
	}
	if !strings.Contains(message, "最接近的当前内容") {
		t.Fatalf("expected multi-line closest body, got %q", message)
	}
}

func TestEditTool_NotFoundIncludesStructuredNextAction(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "sample.txt")
	if err := os.WriteFile(path, []byte("alpha\n"), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}
	tool := NewEditTool()
	tool.SetBasePath(root)
	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"file_path":  "sample.txt",
		"old_string": "missing",
		"new_string": "new",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Success || result.Error == nil {
		t.Fatalf("expected failure, got %#v", result)
	}
	next, _ := result.Metadata["next_action"].(string)
	if next == "" {
		t.Fatalf("expected next_action metadata, got %#v", result.Metadata)
	}
}
