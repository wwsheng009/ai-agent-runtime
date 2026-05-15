package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestApplyPatchTool_AppliesAddUpdateMoveAndDelete(t *testing.T) {
	root := t.TempDir()
	requireWriteFile(t, filepath.Join(root, "a.txt"), "hello\nworld\n")
	requireWriteFile(t, filepath.Join(root, "b.txt"), "bye\n")
	requireWriteFile(t, filepath.Join(root, "obsolete.txt"), "remove me\n")

	tool := NewApplyPatchTool()
	tool.SetBasePath(root)

	patch := strings.Join([]string{
		"*** Begin Patch",
		"*** Update File: a.txt",
		"@@",
		"-hello",
		"+HELLO",
		" world",
		"*** Update File: b.txt",
		"*** Move to: moved/b.txt",
		"@@",
		"-bye",
		"+goodbye",
		"*** Add File: new.txt",
		"+new line",
		"*** Delete File: obsolete.txt",
		"*** End Patch",
	}, "\n")

	result, err := tool.Execute(context.Background(), map[string]interface{}{"patch": patch})
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected success, got error: %v", result.Error)
	}

	assertFileContent(t, filepath.Join(root, "a.txt"), "HELLO\nworld\n")
	assertFileContent(t, filepath.Join(root, "moved", "b.txt"), "goodbye\n")
	assertFileContent(t, filepath.Join(root, "new.txt"), "new line\n")
	if _, err := os.Stat(filepath.Join(root, "b.txt")); !os.IsNotExist(err) {
		t.Fatalf("expected b.txt to be moved, stat err=%v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "obsolete.txt")); !os.IsNotExist(err) {
		t.Fatalf("expected obsolete.txt to be deleted, stat err=%v", err)
	}

	rawPaths, ok := result.Metadata["mutated_paths"].([]string)
	if !ok {
		t.Fatalf("expected mutated_paths metadata, got %#v", result.Metadata["mutated_paths"])
	}
	if len(rawPaths) != 5 {
		t.Fatalf("expected 5 mutated paths, got %v", rawPaths)
	}

	combinedPatch, _ := result.Metadata["patch"].(string)
	if !strings.Contains(combinedPatch, "+++ b/") {
		t.Fatalf("expected combined unified diff metadata, got %q", combinedPatch)
	}
	if !strings.Contains(result.Content, "影响 5 个路径") {
		t.Fatalf("unexpected result content: %q", result.Content)
	}
}

func TestApplyPatchTool_RejectsMalformedPatch(t *testing.T) {
	tool := NewApplyPatchTool()
	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"patch": "*** Update File: broken.txt\n@@\n-old\n+new\n",
	})
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if result.Success {
		t.Fatal("expected malformed patch to fail")
	}
	if result.Error == nil || !strings.Contains(result.Error.Error(), "*** Begin Patch") {
		t.Fatalf("unexpected error: %v", result.Error)
	}
}

func TestApplyPatchTool_AcceptsHeredocWrapper(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "wrapped.txt")
	requireWriteFile(t, path, "hello\n")

	tool := NewApplyPatchTool()
	tool.SetBasePath(root)
	patch := strings.Join([]string{
		"<<'EOF'",
		"*** Begin Patch",
		"*** Update File: wrapped.txt",
		"@@",
		"-hello",
		"+HELLO",
		"*** End Patch",
		"EOF",
	}, "\n")

	result, err := tool.Execute(context.Background(), map[string]interface{}{"patch": patch})
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected success, got error: %v", result.Error)
	}
	assertFileContent(t, path, "HELLO\n")
}

func TestApplyPatchTool_AcceptsFirstUpdateChunkWithoutContextMarker(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "no-context.txt")
	requireWriteFile(t, path, "alpha\nbeta\n")

	tool := NewApplyPatchTool()
	tool.SetBasePath(root)
	patch := strings.Join([]string{
		"*** Begin Patch",
		"*** Update File: no-context.txt",
		"-alpha",
		"+ALPHA",
		" beta",
		"*** End Patch",
	}, "\n")

	result, err := tool.Execute(context.Background(), map[string]interface{}{"patch": patch})
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected success, got error: %v", result.Error)
	}
	assertFileContent(t, path, "ALPHA\nbeta\n")
}

func TestApplyPatchTool_AcceptsWhitespaceAroundOperationHeader(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "header.txt")
	requireWriteFile(t, path, "old\n")

	tool := NewApplyPatchTool()
	tool.SetBasePath(root)
	patch := strings.Join([]string{
		"*** Begin Patch",
		"  *** Update File: header.txt  ",
		"@@",
		"-old",
		"+new",
		"*** End Patch",
	}, "\n")

	result, err := tool.Execute(context.Background(), map[string]interface{}{"patch": patch})
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected success, got error: %v", result.Error)
	}
	assertFileContent(t, path, "new\n")
}

func TestApplyPatchTool_DescriptionGuidesPatchSplitting(t *testing.T) {
	tool := NewApplyPatchTool()

	desc := tool.Description()
	if !strings.Contains(desc, "拆分") || !strings.Contains(desc, "patch") {
		t.Fatalf("expected apply_patch description to guide patch splitting, got %q", desc)
	}

	params := tool.Parameters()
	props, ok := params["properties"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected properties in schema, got %#v", params)
	}
	patchSchema, ok := props["patch"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected patch schema in properties, got %#v", props)
	}
	patchDesc, _ := patchSchema["description"].(string)
	if !strings.Contains(patchDesc, "拆分") || !strings.Contains(patchDesc, "截断") {
		t.Fatalf("expected patch description to guide patch splitting, got %q", patchDesc)
	}
}

func TestApplyPatchTool_MissingUpdatePathIncludesCandidateHint(t *testing.T) {
	root := t.TempDir()
	candidate := filepath.Join(root, "project", "settings", "runtime.yaml")
	requireWriteFile(t, candidate, "hello\n")

	tool := NewApplyPatchTool()
	tool.SetBasePath(root)
	patch := strings.Join([]string{
		"*** Begin Patch",
		"*** Update File: project/setting/runtime.yaml",
		"@@",
		"-hello",
		"+HELLO",
		"*** End Patch",
	}, "\n")

	result, err := tool.Execute(context.Background(), map[string]interface{}{"patch": patch})
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
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

func TestApplyPatchTool_MissingDeletePathIncludesCandidateHint(t *testing.T) {
	root := t.TempDir()
	candidate := filepath.Join(root, "project", "settings", "runtime.yaml")
	requireWriteFile(t, candidate, "hello\n")

	tool := NewApplyPatchTool()
	tool.SetBasePath(root)
	patch := strings.Join([]string{
		"*** Begin Patch",
		"*** Delete File: project/setting/runtime.yaml",
		"*** End Patch",
	}, "\n")

	result, err := tool.Execute(context.Background(), map[string]interface{}{"patch": patch})
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
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

func TestApplyPatchTool_DirectoryPathIncludesKindMismatchHint(t *testing.T) {
	root := t.TempDir()
	candidate := filepath.Join(root, "project", "settings")
	if err := os.MkdirAll(candidate, 0o755); err != nil {
		t.Fatalf("mkdir candidate tree: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(root, "project", "setting"), 0o755); err != nil {
		t.Fatalf("mkdir directory path: %v", err)
	}

	tool := NewApplyPatchTool()
	tool.SetBasePath(root)
	patch := strings.Join([]string{
		"*** Begin Patch",
		"*** Update File: project/setting",
		"@@",
		"-placeholder",
		"+UPDATED",
		"*** End Patch",
	}, "\n")

	result, err := tool.Execute(context.Background(), map[string]interface{}{"patch": patch})
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
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

func TestApplyPatchTool_UpdateIgnoresTrailingWhitespace(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "space.txt")
	requireWriteFile(t, path, "foo   \nbar\t\nbaz\n")

	tool := NewApplyPatchTool()
	tool.SetBasePath(root)
	patch := strings.Join([]string{
		"*** Begin Patch",
		"*** Update File: space.txt",
		"@@",
		"-foo",
		"-bar",
		"+FOO",
		"+BAR",
		" baz",
		"*** End Patch",
	}, "\n")

	result, err := tool.Execute(context.Background(), map[string]interface{}{"patch": patch})
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected success, got error: %v", result.Error)
	}
	assertFileContent(t, path, "FOO\nBAR\nbaz\n")
}

func TestApplyPatchTool_UpdatePureAdditionAppendsAtEnd(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "append.txt")
	requireWriteFile(t, path, "alpha\n")

	tool := NewApplyPatchTool()
	tool.SetBasePath(root)
	patch := strings.Join([]string{
		"*** Begin Patch",
		"*** Update File: append.txt",
		"@@",
		"+omega",
		"*** End Patch",
	}, "\n")

	result, err := tool.Execute(context.Background(), map[string]interface{}{"patch": patch})
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected success, got error: %v", result.Error)
	}
	assertFileContent(t, path, "alpha\nomega\n")
}

func TestApplyPatchTool_UpdateAcceptsBlankContextLine(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "blank.txt")
	requireWriteFile(t, path, "func main() {\n\n\tprintln(\"x\")\n}\n")

	tool := NewApplyPatchTool()
	tool.SetBasePath(root)
	patch := strings.Join([]string{
		"*** Begin Patch",
		"*** Update File: blank.txt",
		"@@",
		" func main() {",
		"",
		"-\tprintln(\"x\")",
		"+\tprintln(\"y\")",
		" }",
		"*** End Patch",
	}, "\n")

	result, err := tool.Execute(context.Background(), map[string]interface{}{"patch": patch})
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected success, got error: %v", result.Error)
	}
	assertFileContent(t, path, "func main() {\n\n\tprintln(\"y\")\n}\n")
}

func TestApplyPatchTool_UpdateNormalizesUnicodePunctuation(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "unicode.py")
	requireWriteFile(t, path, "import asyncio  # local import \u2013 avoids top\u2011level dep\n")

	tool := NewApplyPatchTool()
	tool.SetBasePath(root)
	patch := strings.Join([]string{
		"*** Begin Patch",
		"*** Update File: unicode.py",
		"@@",
		"-import asyncio  # local import - avoids top-level dep",
		"+import asyncio  # HELLO",
		"*** End Patch",
	}, "\n")

	result, err := tool.Execute(context.Background(), map[string]interface{}{"patch": patch})
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected success, got error: %v", result.Error)
	}
	assertFileContent(t, path, "import asyncio  # HELLO\n")
}

func TestApplyPatchTool_UpdateUsesContextMarker(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "context.go")
	requireWriteFile(t, path, strings.Join([]string{
		"func first() {",
		"\tvalue := 1",
		"}",
		"",
		"func second() {",
		"\tvalue := 1",
		"}",
		"",
	}, "\n"))

	tool := NewApplyPatchTool()
	tool.SetBasePath(root)
	patch := strings.Join([]string{
		"*** Begin Patch",
		"*** Update File: context.go",
		"@@ func second() {",
		"-\tvalue := 1",
		"+\tvalue := 2",
		"*** End Patch",
	}, "\n")

	result, err := tool.Execute(context.Background(), map[string]interface{}{"patch": patch})
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected success, got error: %v", result.Error)
	}
	assertFileContent(t, path, strings.Join([]string{
		"func first() {",
		"\tvalue := 1",
		"}",
		"",
		"func second() {",
		"\tvalue := 2",
		"}",
		"",
	}, "\n"))
}

func TestApplyPatchTool_UpdateEndOfFilePrefersTail(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "tail.txt")
	requireWriteFile(t, path, "target\nmiddle\ntarget\n")

	tool := NewApplyPatchTool()
	tool.SetBasePath(root)
	patch := strings.Join([]string{
		"*** Begin Patch",
		"*** Update File: tail.txt",
		"@@",
		"-target",
		"+TAIL",
		"*** End of File",
		"*** End Patch",
	}, "\n")

	result, err := tool.Execute(context.Background(), map[string]interface{}{"patch": patch})
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected success, got error: %v", result.Error)
	}
	assertFileContent(t, path, "target\nmiddle\nTAIL\n")
}

func TestApplyPatchTool_UpdateEndOfFileRequiresTailMatch(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "tail-required.txt")
	requireWriteFile(t, path, "target\nmiddle\nother\n")

	tool := NewApplyPatchTool()
	tool.SetBasePath(root)
	patch := strings.Join([]string{
		"*** Begin Patch",
		"*** Update File: tail-required.txt",
		"@@",
		"-target",
		"+TAIL",
		"*** End of File",
		"*** End Patch",
	}, "\n")

	result, err := tool.Execute(context.Background(), map[string]interface{}{"patch": patch})
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if result.Success {
		t.Fatalf("expected EOF-anchored patch to fail, got success with content %q", result.Content)
	}
	if result.Error == nil || !strings.Contains(result.Error.Error(), "期望内容") {
		t.Fatalf("expected missing hunk diagnostic, got %v", result.Error)
	}
	assertFileContent(t, path, "target\nmiddle\nother\n")
}

func TestApplyPatchTool_MissingContextIncludesExpectedLines(t *testing.T) {
	root := t.TempDir()
	requireWriteFile(t, filepath.Join(root, "missing.txt"), "actual\n")

	tool := NewApplyPatchTool()
	tool.SetBasePath(root)
	patch := strings.Join([]string{
		"*** Begin Patch",
		"*** Update File: missing.txt",
		"@@",
		"-expected",
		"+updated",
		"*** End Patch",
	}, "\n")

	result, err := tool.Execute(context.Background(), map[string]interface{}{"patch": patch})
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if result.Success {
		t.Fatalf("expected failure, got success with content %q", result.Content)
	}
	if result.Error == nil {
		t.Fatal("expected hunk error, got nil")
	}
	message := result.Error.Error()
	if !strings.Contains(message, "期望内容") || !strings.Contains(message, "expected") {
		t.Fatalf("expected missing-context diagnostic, got %q", message)
	}
	if !strings.Contains(message, "view/grep") || !strings.Contains(message, "@@") {
		t.Fatalf("expected actionable guidance, got %q", message)
	}
}

func requireWriteFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll(%s): %v", path, err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile(%s): %v", path, err)
	}
}

func assertFileContent(t *testing.T, path, want string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(%s): %v", path, err)
	}
	if string(data) != want {
		t.Fatalf("file %s = %q, want %q", path, string(data), want)
	}
}
