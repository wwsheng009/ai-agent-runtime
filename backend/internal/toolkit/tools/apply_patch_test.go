package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	runtimeerrors "github.com/wwsheng009/ai-agent-runtime/internal/errors"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolresult"
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
	if code, _ := result.Metadata[toolresult.MetadataErrorCodeKey].(string); code != string(runtimeerrors.ErrToolInvalidArgs) {
		t.Fatalf("error_code=%q want TOOL_INVALID_ARGS meta=%#v", code, result.Metadata)
	}
	if retryable, _ := result.Metadata[toolresult.MetadataRetryableKey].(bool); retryable {
		t.Fatalf("malformed patch must not be retryable: %#v", result.Metadata)
	}
}

func TestApplyPatchTool_InvalidHunkSyntaxIsToolInvalidArgs(t *testing.T) {
	// Live residual: nested Begin marker inside a hunk body was bare TOOL_EXECUTION
	// with generic "Inspect the error details…" next_action.
	root := t.TempDir()
	path := filepath.Join(root, "target.go")
	requireWriteFile(t, path, "package target\n")

	tool := NewApplyPatchTool()
	tool.SetBasePath(root)
	// Build nested-marker body without embedding the begin marker as a contiguous
	// freeform apply_patch document that confuses outer tooling.
	nested := "*** " + "Begin Patch" + `",`
	patch := strings.Join([]string{
		"*** Begin Patch",
		"*** Update File: target.go",
		"@@",
		" package target",
		nested,
		"+package other",
		"*** End Patch",
	}, "\n")

	result, err := tool.Execute(context.Background(), map[string]interface{}{"patch": patch})
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if result.Success {
		t.Fatal("expected nested begin marker inside hunk to fail")
	}
	if result.Error == nil || !strings.Contains(result.Error.Error(), "不是合法的 hunk") {
		t.Fatalf("unexpected error: %v", result.Error)
	}
	if code, _ := result.Metadata[toolresult.MetadataErrorCodeKey].(string); code != string(runtimeerrors.ErrToolInvalidArgs) {
		t.Fatalf("error_code=%q want TOOL_INVALID_ARGS meta=%#v", code, result.Metadata)
	}
	next, _ := result.Metadata[toolresult.MetadataNextActionKey].(string)
	if !strings.Contains(next, "TOOL_INVALID_ARGS") && !strings.Contains(strings.ToLower(next), "syntax") {
		t.Fatalf("expected syntax-focused next_action, got %q", next)
	}
	if fc, _ := result.Metadata["failure_class"].(string); fc != "invalid_patch_syntax" {
		t.Fatalf("failure_class=%q want invalid_patch_syntax", fc)
	}
}

func TestSanitizeApplyPatchPath(t *testing.T) {
	cases := map[string]string{
		`snippet.go",`:   "snippet.go",
		`"snippet.go"`:   "snippet.go",
		`'pkg/foo.go'`:   "pkg/foo.go",
		"  bar.go  ":     "bar.go",
		"`quoted.go`":    "quoted.go",
		`path/file.go,`:  "path/file.go",
		`E:\x\y.go",`:    `E:\x\y.go`,
		"normal/path.go": "normal/path.go",
		"":               "",
	}
	for in, want := range cases {
		if got := sanitizeApplyPatchPath(in); got != want {
			t.Fatalf("sanitizeApplyPatchPath(%q)=%q want %q", in, got, want)
		}
	}
}

func TestApplyPatchTool_SanitizesTrailingQuoteOnUpdatePath(t *testing.T) {
	// Live residual: Update File path carried trailing `",` from JSON/prose paste
	// and hard-failed with Windows "filename syntax is incorrect".
	root := t.TempDir()
	path := filepath.Join(root, "snippet.go")
	requireWriteFile(t, path, "package snippet\n\nfunc Hello() {}\n")

	tool := NewApplyPatchTool()
	tool.SetBasePath(root)
	dirtyPath := "snippet.go" + `",`
	patch := strings.Join([]string{
		"*** Begin Patch",
		"*** Update File: " + dirtyPath,
		"@@",
		"-func Hello() {}",
		"+func Hello() string { return \"ok\" }",
		"*** End Patch",
	}, "\n")

	result, err := tool.Execute(context.Background(), map[string]interface{}{"patch": patch})
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected trailing-quote path sanitize success, got error: %v meta=%#v", result.Error, result.Metadata)
	}
	assertFileContent(t, path, "package snippet\n\nfunc Hello() string { return \"ok\" }\n")
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

func TestApplyPatchTool_NormalizesCommonModelEnvelopeVariants(t *testing.T) {
	for _, wrapper := range []struct {
		name   string
		prefix string
		suffix string
		begin  string
		end    string
	}{
		{name: "redundant boundary stars", begin: "*** Begin Patch ***", end: "*** End Patch ***"},
		{name: "markdown fence", prefix: "```patch\n", suffix: "\n```", begin: "*** Begin Patch", end: "*** End Patch"},
	} {
		t.Run(wrapper.name, func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, "wrapped.txt")
			requireWriteFile(t, path, "old\n")
			tool := NewApplyPatchTool()
			tool.SetBasePath(root)
			patch := wrapper.prefix + strings.Join([]string{
				wrapper.begin,
				"*** Update File: wrapped.txt",
				"@@",
				"-old",
				"+new",
				wrapper.end,
			}, "\n") + wrapper.suffix

			result, err := tool.Execute(context.Background(), map[string]interface{}{"patch": patch})
			if err != nil {
				t.Fatalf("Execute returned error: %v", err)
			}
			if !result.Success {
				t.Fatalf("expected success, got error: %v", result.Error)
			}
			assertFileContent(t, path, "new\n")
		})
	}
}

func TestApplyPatchTool_AcceptsCollapsedSingleLineEnvelope(t *testing.T) {
	root := t.TempDir()
	tool := NewApplyPatchTool()
	tool.SetBasePath(root)
	// Models sometimes emit Begin + Add File on one line without newlines.
	patch := "*** Begin Patch *** *** Add File: package.go\npackage agentconfig\n\nfunc Hello() string { return \"ok\" }\n*** End Patch"
	result, err := tool.Execute(context.Background(), map[string]interface{}{"patch": patch})
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected collapsed envelope success, got error: %v", result.Error)
	}
	assertFileContent(t, filepath.Join(root, "package.go"), "package agentconfig\n\nfunc Hello() string { return \"ok\" }\n")
}

func TestApplyPatchTool_IgnoresUnifiedDiffLineNumberContext(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "chat_bootstrap.go")
	requireWriteFile(t, path, strings.Join([]string{
		"package commands",
		"",
		"func bootstrap() {",
		"\tsetup()",
		"}",
		"",
	}, "\n"))

	tool := NewApplyPatchTool()
	tool.SetBasePath(root)
	// Unified-diff style header must not be treated as a literal context line.
	patch := strings.Join([]string{
		"*** Begin Patch",
		"*** Update File: chat_bootstrap.go",
		"@@ -185,6 +185,12 @@",
		" func bootstrap() {",
		"-\tsetup()",
		"+\tsetup()",
		"+\tinitFastMode()",
		" }",
		"*** End Patch",
	}, "\n")

	result, err := tool.Execute(context.Background(), map[string]interface{}{"patch": patch})
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected unified-diff header to be ignored as context, got error: %v", result.Error)
	}
	assertFileContent(t, path, strings.Join([]string{
		"package commands",
		"",
		"func bootstrap() {",
		"\tsetup()",
		"\tinitFastMode()",
		"}",
		"",
	}, "\n"))
}

func TestHunkChangeContextLine_StripsUnifiedRanges(t *testing.T) {
	cases := []struct {
		header string
		want   string
	}{
		{"@@", ""},
		{"@@ func Foo()", "func Foo()"},
		{"@@ -185,6 +185,12 @@", ""},
		{"@@ -10 +12 @@", ""},
		{"@@ -185,6 +185,12 @@ func bootstrap()", "func bootstrap()"},
		{"@@ -1,3 +1,4 @@ type X struct {", "type X struct {"},
	}
	for _, tc := range cases {
		if got := hunkChangeContextLine(tc.header); got != tc.want {
			t.Fatalf("hunkChangeContextLine(%q)=%q, want %q", tc.header, got, tc.want)
		}
	}
}

func TestApplyPatchTool_AcceptsBareAddFileContentWithoutPlusPrefix(t *testing.T) {
	root := t.TempDir()
	tool := NewApplyPatchTool()
	tool.SetBasePath(root)
	patch := strings.Join([]string{
		"*** Begin Patch",
		"*** Add File: package.go",
		"package agentconfig",
		"",
		"func Hello() {}",
		"*** End Patch",
	}, "\n")

	result, err := tool.Execute(context.Background(), map[string]interface{}{"patch": patch})
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected bare add-file content to succeed, got error: %v", result.Error)
	}
	assertFileContent(t, filepath.Join(root, "package.go"), "package agentconfig\n\nfunc Hello() {}\n")
}

func TestApplyPatchTool_AcceptsEndOfFileMarkerVariants(t *testing.T) {
	root := t.TempDir()
	tool := NewApplyPatchTool()
	tool.SetBasePath(root)

	// Residual failure mode: models emit "*** End of File ***" inside Add File.
	patch := strings.Join([]string{
		"*** Begin Patch",
		"*** Add File: no-final-nl.txt",
		"+line-one",
		"*** End of File ***",
		"*** End Patch",
	}, "\n")
	result, err := tool.Execute(context.Background(), map[string]interface{}{"patch": patch})
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected End of File *** variant to succeed, got error: %v", result.Error)
	}
	// NoFinalNewline should omit trailing newline.
	assertFileContent(t, filepath.Join(root, "no-final-nl.txt"), "line-one")

	// Same variant should work for Update File hunks.
	path := filepath.Join(root, "tail.txt")
	requireWriteFile(t, path, "alpha\nbeta\n")
	update := strings.Join([]string{
		"*** Begin Patch",
		"*** Update File: tail.txt",
		"@@",
		"-beta",
		"+BETA",
		"*** End of File***",
		"*** End Patch",
	}, "\n")
	result, err = tool.Execute(context.Background(), map[string]interface{}{"patch": update})
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected update End of File*** variant to succeed, got error: %v", result.Error)
	}
	assertFileContent(t, path, "alpha\nBETA\n")
}

func TestApplyPatchTool_IgnoresTrailingContentAfterEndMarker(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "tail.txt")
	requireWriteFile(t, path, "old\n")
	tool := NewApplyPatchTool()
	tool.SetBasePath(root)
	patch := strings.Join([]string{
		"*** Begin Patch",
		"*** Update File: tail.txt",
		"@@",
		"-old",
		"+new",
		"*** End Patch",
		"Here is extra model commentary that used to fail parsing.",
		"More trailing text.",
	}, "\n")

	result, err := tool.Execute(context.Background(), map[string]interface{}{"patch": patch})
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected trailing content after end marker to be ignored, got error: %v", result.Error)
	}
	assertFileContent(t, path, "new\n")
}

func TestApplyPatchTool_AllowsMissingEndMarkerWhenOperationsExist(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "no-end.txt")
	requireWriteFile(t, path, "old\n")
	tool := NewApplyPatchTool()
	tool.SetBasePath(root)
	patch := strings.Join([]string{
		"*** Begin Patch",
		"*** Update File: no-end.txt",
		"@@",
		"-old",
		"+new",
	}, "\n")

	result, err := tool.Execute(context.Background(), map[string]interface{}{"patch": patch})
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected missing end marker with complete operations to succeed, got error: %v", result.Error)
	}
	assertFileContent(t, path, "new\n")
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

func TestApplyPatchTool_MissingContextIncludesClosestCurrentLines(t *testing.T) {
	root := t.TempDir()
	requireWriteFile(t, filepath.Join(root, "manager.go"), strings.Join([]string{
		"package manager",
		"",
		"func (m *Manager) RegisterGroup(name string, active bool) error {",
		"\treturn m.register(name, active)",
		"}",
		"",
	}, "\n"))

	tool := NewApplyPatchTool()
	tool.SetBasePath(root)
	patch := strings.Join([]string{
		"*** Begin Patch",
		"*** Update File: manager.go",
		"@@ func (m *Manager) RegisterGroup(name string, priority int, active bool) error {",
		"-\treturn m.register(name, priority, active)",
		"+\treturn m.registerWithPriority(name, priority, active)",
		"*** End Patch",
	}, "\n")

	result, err := tool.Execute(context.Background(), map[string]interface{}{"patch": patch})
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if result.Success || result.Error == nil {
		t.Fatalf("expected patch failure, got %#v", result)
	}
	message := result.Error.Error()
	if !strings.Contains(message, "最接近的当前内容") {
		t.Fatalf("expected current context in diagnostic, got %q", message)
	}
	// Line-prefixed full current lines (pipe form, no mid-line truncate) for
	// copy-paste / offline rehydrate parity with edit formatEditClosestLines.
	if !strings.Contains(message, "3|func (m *Manager) RegisterGroup(name string, active bool) error {") {
		t.Fatalf("expected stable current line numbers, got %q", message)
	}
	next, _ := result.Metadata["next_action"].(string)
	if !strings.Contains(next, "view") && !strings.Contains(next, "grep") {
		t.Fatalf("expected structured next_action metadata for hunk miss, got %q metadata=%#v", next, result.Metadata)
	}
	if code, _ := result.Metadata["error_code"].(string); code != "STALE_CONTEXT" {
		t.Fatalf("expected STALE_CONTEXT error_code for hunk miss, got %#v", result.Metadata)
	}
	if path, _ := result.Metadata["file_path"].(string); !strings.Contains(path, "manager.go") {
		t.Fatalf("expected file_path metadata for hunk miss, got %#v", result.Metadata)
	}
	if _, ok := result.Metadata["suggested_view_offset"]; !ok {
		t.Fatalf("expected suggested_view_offset for hunk miss, got %#v", result.Metadata)
	}
	snippet, _ := result.Metadata["current_snippet"].(string)
	if !strings.Contains(snippet, "func (m *Manager) RegisterGroup(name string, active bool) error {") {
		t.Fatalf("expected current_snippet with live file lines for copy-paste recovery, got %#v", result.Metadata["current_snippet"])
	}
	if start, _ := result.Metadata["current_snippet_start_line"].(int); start <= 0 {
		t.Fatalf("expected current_snippet_start_line > 0, got %#v", result.Metadata["current_snippet_start_line"])
	}
	if !strings.Contains(next, "current_snippet") {
		t.Fatalf("expected next_action to mention current_snippet, got %q", next)
	}
}

func TestApplyPatchTool_HealsBlankRunLengthDrift(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "blank_run.go")
	// File has 3 blank lines between blocks; model invents 4 blanks in the hunk.
	original := strings.Join([]string{
		"package demo",
		"",
		"func Alpha() {",
		"\treturn",
		"}",
		"",
		"",
		"",
		"func Beta() {",
		"\treturn",
		"}",
		"",
	}, "\n")
	requireWriteFile(t, path, original)

	tool := NewApplyPatchTool()
	tool.SetBasePath(root)
	// old side uses 4 blank lines between } and func Beta; file only has 3.
	patch := strings.Join([]string{
		"*** Begin Patch",
		"*** Update File: blank_run.go",
		"@@",
		" func Alpha() {",
		" \treturn",
		" }",
		" ",
		" ",
		" ",
		" ",
		"-func Beta() {",
		"+func BetaUpdated() {",
		" \treturn",
		" }",
		"*** End Patch",
	}, "\n")

	result, err := tool.Execute(context.Background(), map[string]interface{}{"patch": patch})
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected blank-run auto-heal for apply_patch, got error: %v metadata=%#v", result.Error, result.Metadata)
	}
	data, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatalf("read file: %v", readErr)
	}
	// File blank-run length preserved; only the renamed function changes.
	want := strings.Join([]string{
		"package demo",
		"",
		"func Alpha() {",
		"\treturn",
		"}",
		"",
		"",
		"",
		"func BetaUpdated() {",
		"\treturn",
		"}",
		"",
	}, "\n")
	if got := string(data); got != want {
		t.Fatalf("blank-run patch heal mismatch\nwant:\n%q\ngot:\n%q", want, got)
	}
}

func TestApplyPatchTool_ClosestSnippetPrefersMultiLineWindow(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "targets.go")
	original := strings.Join([]string{
		"package demo",
		"",
		"func Alpha() {",
		"\treturn 1",
		"}",
		"",
		"func Beta() {",
		"\treturn 2",
		"}",
		"",
		"func GammaSpecial() {",
		"\treturn helper(3)",
		"}",
		"",
	}, "\n")
	requireWriteFile(t, path, original)

	tool := NewApplyPatchTool()
	tool.SetBasePath(root)
	// Hunk old lines mix a generic early func with a distinctive later block that drifted.
	patch := strings.Join([]string{
		"*** " + "Begin Patch",
		"*** Update File: targets.go",
		"@@",
		"-func Alpha() {",
		"-\treturn 9",
		"-}",
		"-",
		"-func GammaSpecial() {",
		"-\treturn helper(9)",
		"-}",
		"+func Alpha() {",
		"+\treturn 9",
		"+}",
		"+",
		"+func GammaSpecial() {",
		"+\treturn helper(42)",
		"+}",
		"*** " + "End Patch",
	}, "\n")

	result, err := tool.Execute(context.Background(), map[string]interface{}{"patch": patch})
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if result.Success || result.Error == nil {
		t.Fatalf("expected true content drift failure, got %#v", result)
	}
	snippet, _ := result.Metadata["current_snippet"].(string)
	if !strings.Contains(snippet, "GammaSpecial") {
		t.Fatalf("expected multi-line closest near GammaSpecial, got %q meta=%#v", snippet, result.Metadata)
	}
	start, _ := result.Metadata["current_snippet_start_line"].(int)
	if start <= 0 {
		t.Fatalf("expected current_snippet_start_line > 0, got %#v", result.Metadata["current_snippet_start_line"])
	}
	message := result.Error.Error()
	if !strings.Contains(message, "GammaSpecial") {
		t.Fatalf("expected GammaSpecial in diagnostic body, got %q", message)
	}
}

func TestClosestPatchCurrentContext_MultiLineBeatsGenericFirstLine(t *testing.T) {
	actual := []string{
		"package demo",
		"",
		"func Alpha() {",
		"\treturn 1",
		"}",
		"",
		"func GammaSpecial() {",
		"\treturn helper(3)",
		"}",
	}
	expected := []string{
		"func Alpha() {",
		"\treturn 9",
		"}",
		"",
		"func GammaSpecial() {",
		"\treturn helper(9)",
		"}",
	}
	start, closest := closestPatchCurrentContext(actual, expected)
	joined := strings.Join(closest, "\n")
	if !strings.Contains(joined, "GammaSpecial") {
		t.Fatalf("expected GammaSpecial in closest window, start=%d closest=%q", start, joined)
	}
	if start < 1 {
		t.Fatalf("expected positive start line, got %d", start)
	}
}

func TestClosestPatchCurrentContext_AnchorsOnNearIdentifierTypo(t *testing.T) {
	actual := []string{
		"import (",
		"\t\"fmt\"",
		")",
		"",
		"func Other() {}",
		"",
		"func HelloWorld() {",
		"\treturn 1",
		"}",
	}
	// First line completely wrong/generic; later lines nearly match via typo.
	expected := []string{
		"import (",
		"\t\"missing\"",
		")",
		"",
		"func HelloWord() {",
		"\treturn 1",
		"}",
	}
	start, closest := closestPatchCurrentContext(actual, expected)
	joined := strings.Join(closest, "\n")
	if !strings.Contains(joined, "HelloWorld") {
		t.Fatalf("expected HelloWorld in closest window, start=%d closest=%q", start, joined)
	}
	// Recovery should land near the distinctive block, not only on import noise.
	if start < 5 {
		t.Fatalf("expected tightened anchor near HelloWorld block, got start=%d closest=%q", start, joined)
	}
}

func TestApplyPatchTool_NotFoundIncludesNearIdentifierClosest(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "snippet.go")
	original := "package demo\n\nfunc HelloWorld() {\n\treturn 1\n}\n"
	requireWriteFile(t, path, original)

	tool := NewApplyPatchTool()
	tool.SetBasePath(root)
	// Concatenate markers so nested apply_patch payloads stay inert if this
	// test body is itself ever patched with apply_patch.
	patch := strings.Join([]string{
		"*** " + "Begin Patch",
		"*** " + "Update File: snippet.go",
		"@@",
		"-func HelloWord() {",
		"-\treturn 2",
		"-}",
		"+func HelloWorld() {",
		"+\treturn 2",
		"+}",
		"*** " + "End Patch",
	}, "\n")

	result, err := tool.Execute(context.Background(), map[string]interface{}{"patch": patch})
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if result.Success || result.Error == nil {
		t.Fatalf("expected true content drift failure, got %#v", result)
	}
	message := result.Error.Error()
	if !strings.Contains(message, "最接近的当前内容") && !strings.Contains(message, "HelloWorld") {
		t.Fatalf("expected closest guidance with HelloWorld, got %q", message)
	}
	snippet, _ := result.Metadata["current_snippet"].(string)
	if !strings.Contains(snippet, "HelloWorld") {
		t.Fatalf("expected current_snippet with HelloWorld, got %#v", result.Metadata["current_snippet"])
	}
	if code, _ := result.Metadata["error_code"].(string); code != "STALE_CONTEXT" {
		t.Fatalf("expected STALE_CONTEXT error_code, got %#v", result.Metadata)
	}
}

func TestApplyPatchTool_EmptyPatchHasNextAction(t *testing.T) {
	tool := NewApplyPatchTool()
	result, err := tool.Execute(context.Background(), map[string]interface{}{"patch": "   "})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Success || result.Error == nil {
		t.Fatalf("expected empty patch failure, got %#v", result)
	}
	next, _ := result.Metadata["next_action"].(string)
	if !strings.Contains(strings.ToLower(next), "patch") {
		t.Fatalf("expected next_action for empty patch, got %q", next)
	}
}

func TestApplyPatchTool_FallsBackWhenContextMarkerMissing(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "config_tui.go")
	requireWriteFile(t, path, strings.Join([]string{
		"package commands",
		"",
		"func renderStatus() {",
		"\tfmt.Println(\"idle\")",
		"}",
		"",
	}, "\n"))

	tool := NewApplyPatchTool()
	tool.SetBasePath(root)
	// Stale/wrong @@ context (as models often invent) must not hard-fail when
	// the old content still uniquely exists in the file.
	patch := strings.Join([]string{
		"*** Begin Patch",
		"*** Update File: config_tui.go",
		"@@ -539,13 +539,15 @@ func missingSymbolDoesNotExist() {",
		" func renderStatus() {",
		"-\tfmt.Println(\"idle\")",
		"+\tfmt.Println(\"ready\")",
		" }",
		"*** End Patch",
	}, "\n")

	result, err := tool.Execute(context.Background(), map[string]interface{}{"patch": patch})
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected stale @@ context to fall back to old-content match, got error: %v", result.Error)
	}
	assertFileContent(t, path, strings.Join([]string{
		"package commands",
		"",
		"func renderStatus() {",
		"\tfmt.Println(\"ready\")",
		"}",
		"",
	}, "\n"))
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
