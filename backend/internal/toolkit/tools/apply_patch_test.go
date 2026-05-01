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
