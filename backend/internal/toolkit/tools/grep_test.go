package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/toolkit"
)

func TestGrepTool(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "grep-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	testFiles := map[string]string{
		"file1.go":  "package main\n\nfunc main() {\n\tprintln(\"hello\")\n}\n",
		"file2.go":  "package main\n\nfunc helper() {\n\tprintln(\"helper\")\n}\n",
		"file3.txt": "hello world\nhello universe\n",
	}

	for name, content := range testFiles {
		path := filepath.Join(tmpDir, name)
		if err := os.WriteFile(path, []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}

	tests := []struct {
		name      string
		params    map[string]interface{}
		wantFound bool
		wantError bool
	}{
		{
			name: "search for function",
			params: map[string]interface{}{
				"pattern": "func main",
				"path":    tmpDir,
			},
			wantFound: true,
			wantError: false,
		},
		{
			name: "search with include pattern",
			params: map[string]interface{}{
				"pattern": "println",
				"path":    tmpDir,
				"include": "*.go",
			},
			wantFound: true,
			wantError: false,
		},
		{
			name: "literal text search",
			params: map[string]interface{}{
				"pattern":      "hello",
				"path":         tmpDir,
				"literal_text": true,
			},
			wantFound: true,
			wantError: false,
		},
		{
			name: "missing pattern",
			params: map[string]interface{}{
				"path": tmpDir,
			},
			wantFound: false,
			wantError: true,
		},
		{
			name: "no matches",
			params: map[string]interface{}{
				"pattern": "nonexistent_pattern_xyz",
				"path":    tmpDir,
			},
			wantFound: false,
			wantError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tool := NewGrepTool()
			result, err := tool.Execute(context.Background(), tt.params)

			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if tt.wantError {
				if result.Success {
					t.Error("expected error but got success")
				}
				return
			}

			if !result.Success && tt.wantFound {
				t.Errorf("expected to find matches but got failure: %v", result.Error)
			}

			if result.Success {
				if tt.wantFound && strings.Contains(tt.params["pattern"].(string), "hello") && !strings.Contains(result.Content, "hello") {
					t.Fatalf("expected literal_text alias search to find hello, got %q", result.Content)
				}
				if result.Metadata["engine"] == nil || strings.TrimSpace(result.Metadata["engine"].(string)) == "" {
					t.Fatalf("expected engine metadata, got %#v", result.Metadata)
				}
			}
		})
	}
}

func TestGrepTool_PrefersRipgrepWhenAvailable(t *testing.T) {
	tmpDir := t.TempDir()
	tool := NewGrepTool()

	var (
		gotBinary string
		gotDir    string
		gotArgs   []string
	)

	tool.lookPath = func(name string) (string, error) {
		if name != "rg" {
			t.Fatalf("expected lookup for rg, got %q", name)
		}
		return "rg", nil
	}
	tool.runCommand = func(ctx context.Context, binaryPath, workingDir string, args []string) ([]byte, error) {
		gotBinary = binaryPath
		gotDir = workingDir
		gotArgs = append([]string(nil), args...)
		return []byte("main.go:3:func main() {}\n"), nil
	}

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"pattern": "func main",
		"path":    tmpDir,
		"include": "*.go",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected success, got error %v", result.Error)
	}
	if gotBinary != "rg" {
		t.Fatalf("expected rg binary, got %q", gotBinary)
	}
	if gotDir != tmpDir {
		t.Fatalf("expected working dir %q, got %q", tmpDir, gotDir)
	}
	if !strings.Contains(strings.Join(gotArgs, " "), "--glob *.go") {
		t.Fatalf("expected include glob in args, got %v", gotArgs)
	}
	if result.Metadata["engine"] != "rg" {
		t.Fatalf("expected engine=rg, got %#v", result.Metadata["engine"])
	}
	if result.Content != "main.go:3: func main() {}" {
		t.Fatalf("unexpected content: %q", result.Content)
	}
}

func TestGrepTool_FallsBackWhenRipgrepUnavailable(t *testing.T) {
	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "main.go")
	if err := os.WriteFile(filePath, []byte("package main\nfunc main() {}\n"), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}

	tool := NewGrepTool()
	tool.lookPath = func(name string) (string, error) {
		return "", os.ErrNotExist
	}

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"pattern": "func main",
		"path":    tmpDir,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected success, got error %v", result.Error)
	}
	if result.Metadata["engine"] != "builtin" {
		t.Fatalf("expected engine=builtin, got %#v", result.Metadata["engine"])
	}
	if !strings.Contains(result.Content, "main.go:2: func main() {}") {
		t.Fatalf("unexpected content: %q", result.Content)
	}
}

func TestGrepTool_Interface(t *testing.T) {
	tool := NewGrepTool()

	var _ toolkit.Tool = tool

	if tool.Name() != "grep" {
		t.Errorf("expected name 'grep', got '%s'", tool.Name())
	}

	if tool.Description() == "" {
		t.Error("description should not be empty")
	}

	if !tool.CanDirectCall() {
		t.Error("grep tool should support direct call")
	}
}

func TestGrepTool_DescriptionRgAvailable(t *testing.T) {
	tool := NewGrepTool()
	tool.lookPath = func(name string) (string, error) {
		if name == "rg" {
			return "/usr/bin/rg", nil
		}
		return "", os.ErrNotExist
	}

	desc := tool.Description()
	if !strings.Contains(desc, "ripgrep/rg") {
		t.Fatalf("expected description to mention ripgrep/rg when rg is available, got %q", desc)
	}
	if strings.Contains(desc, "内置扫描") {
		t.Fatalf("expected description NOT to mention built-in scanner when rg is available, got %q", desc)
	}
}

func TestGrepTool_DescriptionRgUnavailable(t *testing.T) {
	tool := NewGrepTool()
	tool.lookPath = func(name string) (string, error) {
		return "", os.ErrNotExist
	}

	desc := tool.Description()
	if !strings.Contains(desc, "内置扫描") {
		t.Fatalf("expected description to mention built-in scanner when rg is unavailable, got %q", desc)
	}
	if !strings.Contains(desc, "ripgrep/rg") {
		t.Fatalf("expected description to mention ripgrep/rg installation hint when rg is unavailable, got %q", desc)
	}
}

// --- New parameter tests ---

func TestGrepTool_IgnoreCase(t *testing.T) {
	tmpDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmpDir, "sample.go"), []byte("package main\nfunc HelloWorld() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	tool := NewGrepTool()
	tool.lookPath = func(name string) (string, error) {
		return "", os.ErrNotExist // force builtin
	}

	// Without ignore_case, should not match
	result, _ := tool.Execute(context.Background(), map[string]interface{}{
		"pattern": "helloworld",
		"path":    tmpDir,
	})
	if result.Success && strings.Contains(result.Content, "HelloWorld") {
		t.Fatal("expected no match without ignore_case")
	}

	// With ignore_case, should match
	result, _ = tool.Execute(context.Background(), map[string]interface{}{
		"pattern":     "helloworld",
		"path":        tmpDir,
		"ignore_case": true,
	})
	if !result.Success {
		t.Fatalf("expected match with ignore_case, got: %v", result.Error)
	}
	if !strings.Contains(result.Content, "HelloWorld") {
		t.Fatalf("expected HelloWorld in output, got %q", result.Content)
	}
}

func TestGrepTool_WordMatch(t *testing.T) {
	tmpDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmpDir, "sample.go"), []byte("package main\nvar err error\nvar errors []string\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	tool := NewGrepTool()
	tool.lookPath = func(name string) (string, error) {
		return "", os.ErrNotExist
	}

	// Without word, should match both "err" and "errors"
	result, _ := tool.Execute(context.Background(), map[string]interface{}{
		"pattern": "err",
		"path":    tmpDir,
	})
	if !result.Success {
		t.Fatalf("expected match, got: %v", result.Error)
	}
	if !strings.Contains(result.Content, "errors") {
		t.Fatalf("expected 'errors' to match without word flag, got %q", result.Content)
	}

	// With word, should match only "err" not "errors"
	result, _ = tool.Execute(context.Background(), map[string]interface{}{
		"pattern": "err",
		"path":    tmpDir,
		"word":    true,
	})
	if !result.Success {
		t.Fatalf("expected match, got: %v", result.Error)
	}
	if strings.Contains(result.Content, "errors") {
		t.Fatalf("expected 'errors' NOT to match with word flag, got %q", result.Content)
	}
	if !strings.Contains(result.Content, "var err error") {
		t.Fatalf("expected 'err' to match with word flag, got %q", result.Content)
	}
}

func TestGrepTool_ContextLines(t *testing.T) {
	tmpDir := t.TempDir()
	content := "line1\nline2\nline3 TARGET\nline4\nline5\n"
	if err := os.WriteFile(filepath.Join(tmpDir, "test.txt"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	tool := NewGrepTool()
	tool.lookPath = func(name string) (string, error) {
		return "", os.ErrNotExist
	}

	result, _ := tool.Execute(context.Background(), map[string]interface{}{
		"pattern": "TARGET",
		"path":    tmpDir,
		"context": 1,
	})
	if !result.Success {
		t.Fatalf("expected match, got: %v", result.Error)
	}
	// Should include line2 and line4 as context
	if !strings.Contains(result.Content, "line2") || !strings.Contains(result.Content, "line4") {
		t.Fatalf("expected context lines around TARGET, got %q", result.Content)
	}
	// Should mark the match line
	if !strings.Contains(result.Content, ">") {
		t.Fatalf("expected match marker '>' in context output, got %q", result.Content)
	}
}

func TestGrepTool_ModeFiles(t *testing.T) {
	tmpDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmpDir, "a.go"), []byte("package main\nfunc hello() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tmpDir, "b.go"), []byte("package main\n// no hello here\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	tool := NewGrepTool()
	tool.lookPath = func(name string) (string, error) {
		return "", os.ErrNotExist
	}

	result, _ := tool.Execute(context.Background(), map[string]interface{}{
		"pattern": "hello",
		"path":    tmpDir,
		"include": "*.go",
		"mode":    "files",
	})
	if !result.Success {
		t.Fatalf("expected success, got: %v", result.Error)
	}
	// Should only list filenames, not line content
	if !strings.Contains(result.Content, "a.go") {
		t.Fatalf("expected a.go in files mode output, got %q", result.Content)
	}
	if strings.Contains(result.Content, "func hello()") {
		t.Fatalf("expected NO line content in files mode, got %q", result.Content)
	}
}

func TestGrepTool_ModeCount(t *testing.T) {
	tmpDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmpDir, "count.txt"), []byte("hello\nhello\nworld\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	tool := NewGrepTool()
	tool.lookPath = func(name string) (string, error) {
		return "", os.ErrNotExist
	}

	result, _ := tool.Execute(context.Background(), map[string]interface{}{
		"pattern": "hello",
		"path":    tmpDir,
		"mode":    "count",
	})
	if !result.Success {
		t.Fatalf("expected success, got: %v", result.Error)
	}
	if !strings.Contains(result.Content, "count.txt:2") {
		t.Fatalf("expected count.txt:2 in count mode, got %q", result.Content)
	}
}

func TestGrepTool_FileTypeFilter(t *testing.T) {
	tmpDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmpDir, "app.go"), []byte("package main\nfunc main() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tmpDir, "app.py"), []byte("def main():\n    pass\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	tool := NewGrepTool()
	tool.lookPath = func(name string) (string, error) {
		return "", os.ErrNotExist
	}

	// Search with type=go, should only find in .go file
	result, _ := tool.Execute(context.Background(), map[string]interface{}{
		"pattern": "func main",
		"path":    tmpDir,
		"type":    "go",
	})
	if !result.Success {
		t.Fatalf("expected match, got: %v", result.Error)
	}
	if !strings.Contains(result.Content, "app.go") {
		t.Fatalf("expected app.go in output with type=go, got %q", result.Content)
	}
	if strings.Contains(result.Content, "app.py") {
		t.Fatalf("expected NO app.py with type=go, got %q", result.Content)
	}

	// Search with type=py, should not find "func main" pattern
	result, _ = tool.Execute(context.Background(), map[string]interface{}{
		"pattern": "func main",
		"path":    tmpDir,
		"type":    "py",
	})
	if result.Success && strings.Contains(result.Content, "app.go") {
		t.Fatalf("expected NO app.go with type=py, got %q", result.Content)
	}
}

func TestGrepTool_ExcludePattern(t *testing.T) {
	tmpDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmpDir, "app.go"), []byte("package main\nfunc main() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tmpDir, "app_test.go"), []byte("package main\nfunc TestMain() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	tool := NewGrepTool()
	tool.lookPath = func(name string) (string, error) {
		return "", os.ErrNotExist
	}

	result, _ := tool.Execute(context.Background(), map[string]interface{}{
		"pattern": "func",
		"path":    tmpDir,
		"exclude": "*_test.go",
	})
	if !result.Success {
		t.Fatalf("expected match, got: %v", result.Error)
	}
	if !strings.Contains(result.Content, "app.go") {
		t.Fatalf("expected app.go in output, got %q", result.Content)
	}
	if strings.Contains(result.Content, "app_test.go") {
		t.Fatalf("expected NO app_test.go with exclude, got %q", result.Content)
	}
}

func TestGrepTool_MaxDepth(t *testing.T) {
	tmpDir := t.TempDir()
	subDir := filepath.Join(tmpDir, "sub", "deep")
	if err := os.MkdirAll(subDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tmpDir, "top.txt"), []byte("target here\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(subDir, "deep.txt"), []byte("target deep\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	tool := NewGrepTool()
	tool.lookPath = func(name string) (string, error) {
		return "", os.ErrNotExist
	}

	// max_depth=1 should only find top-level files
	result, _ := tool.Execute(context.Background(), map[string]interface{}{
		"pattern":   "target",
		"path":      tmpDir,
		"max_depth": 1,
	})
	if !result.Success {
		t.Fatalf("expected match, got: %v", result.Error)
	}
	if !strings.Contains(result.Content, "top.txt") {
		t.Fatalf("expected top.txt at max_depth=1, got %q", result.Content)
	}
	if strings.Contains(result.Content, "deep.txt") {
		t.Fatalf("expected NO deep.txt at max_depth=1, got %q", result.Content)
	}
}

func TestGrepTool_MaxDepthZeroBuiltin(t *testing.T) {
	tmpDir := t.TempDir()
	subDir := filepath.Join(tmpDir, "sub")
	if err := os.MkdirAll(subDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tmpDir, "top.txt"), []byte("target here\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(subDir, "deep.txt"), []byte("target deep\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	tool := NewGrepTool()
	tool.lookPath = func(name string) (string, error) {
		return "", os.ErrNotExist
	}

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"pattern":   "target",
		"path":      tmpDir,
		"max_depth": 0,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected success, got: %v", result.Error)
	}
	if !strings.Contains(result.Content, "top.txt") {
		t.Fatalf("expected top.txt at max_depth=0, got %q", result.Content)
	}
	if strings.Contains(result.Content, "deep.txt") {
		t.Fatalf("expected NO deep.txt at max_depth=0, got %q", result.Content)
	}
	if result.Metadata["max_depth"] != 0 || result.Metadata["max_depth_explicit"] != true {
		t.Fatalf("expected explicit max_depth metadata for zero, got %#v", result.Metadata)
	}
}

func TestGrepTool_MaxCountZeroBuiltin(t *testing.T) {
	tmpDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmpDir, "sample.txt"), []byte("needle\nneedle\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	tool := NewGrepTool()
	tool.lookPath = func(name string) (string, error) {
		return "", os.ErrNotExist
	}

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"pattern":   "needle",
		"path":      tmpDir,
		"max_count": 0,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected success, got: %v", result.Error)
	}
	if result.Content != "未找到匹配的内容" {
		t.Fatalf("expected explicit max_count=0 to suppress matches, got %q", result.Content)
	}
	if result.Metadata["match_count"] != 0 || result.Metadata["max_count_explicit"] != true {
		t.Fatalf("expected explicit max_count metadata for zero, got %#v", result.Metadata)
	}
}

func TestGrepTool_RgArgsContextAndWord(t *testing.T) {
	tool := NewGrepTool()
	var gotArgs []string

	tool.lookPath = func(name string) (string, error) {
		return "rg", nil
	}
	tool.runCommand = func(ctx context.Context, binaryPath, workingDir string, args []string) ([]byte, error) {
		gotArgs = append([]string(nil), args...)
		return []byte("main.go:3:func main() {}\n"), nil
	}

	_, _ = tool.Execute(context.Background(), map[string]interface{}{
		"pattern":     "main",
		"path":        t.TempDir(),
		"context":     3,
		"word":        true,
		"ignore_case": true,
		"exclude":     "*.test.ts",
	})

	argsStr := strings.Join(gotArgs, " ")

	if !strings.Contains(argsStr, "--context 3") {
		t.Fatalf("expected --context 3 in rg args, got %v", gotArgs)
	}
	if !strings.Contains(argsStr, "--word-regexp") {
		t.Fatalf("expected --word-regexp in rg args, got %v", gotArgs)
	}
	if !strings.Contains(argsStr, "--ignore-case") {
		t.Fatalf("expected --ignore-case in rg args, got %v", gotArgs)
	}
	if !strings.Contains(argsStr, "--glob !*.test.ts") {
		t.Fatalf("expected --glob !*.test.ts in rg args, got %v", gotArgs)
	}
}

func TestGrepTool_RgModeFiles(t *testing.T) {
	tool := NewGrepTool()
	var gotArgs []string

	tool.lookPath = func(name string) (string, error) {
		return "rg", nil
	}
	tool.runCommand = func(ctx context.Context, binaryPath, workingDir string, args []string) ([]byte, error) {
		gotArgs = append([]string(nil), args...)
		return []byte("main.go\nhelper.go\n"), nil
	}

	result, _ := tool.Execute(context.Background(), map[string]interface{}{
		"pattern": "func",
		"path":    t.TempDir(),
		"mode":    "files",
	})
	argsStr := strings.Join(gotArgs, " ")

	if !strings.Contains(argsStr, "--files-with-matches") {
		t.Fatalf("expected --files-with-matches in rg args, got %v", gotArgs)
	}
	if result.Metadata["engine"] != "rg" {
		t.Fatalf("expected engine=rg, got %v", result.Metadata["engine"])
	}
}

func TestGrepTool_RgTypeArg(t *testing.T) {
	tool := NewGrepTool()
	var gotArgs []string

	tool.lookPath = func(name string) (string, error) {
		return "rg", nil
	}
	tool.runCommand = func(ctx context.Context, binaryPath, workingDir string, args []string) ([]byte, error) {
		gotArgs = append([]string(nil), args...)
		return []byte("main.go:1:package main\n"), nil
	}

	_, _ = tool.Execute(context.Background(), map[string]interface{}{
		"pattern": "main",
		"path":    t.TempDir(),
		"type":    "go",
	})
	argsStr := strings.Join(gotArgs, " ")

	if !strings.Contains(argsStr, "--type go") {
		t.Fatalf("expected --type go in rg args, got %v", gotArgs)
	}
}

func TestGrepTool_RgArgsCompatibilityBuiltin(t *testing.T) {
	tmpDir := t.TempDir()
	content := "line1\nline2\nHELLO\nline4\nline5\n"
	if err := os.WriteFile(filepath.Join(tmpDir, "sample.go"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tmpDir, "sample.txt"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	tool := NewGrepTool()
	tool.lookPath = func(name string) (string, error) {
		return "", os.ErrNotExist
	}

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"rg_args": []interface{}{"-g", "*.go", "-i", "-w", "-B", "1", "-A", "1", "hello", tmpDir},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected success, got error %v", result.Error)
	}
	if !strings.Contains(result.Content, "sample.go") {
		t.Fatalf("expected sample.go in output, got %q", result.Content)
	}
	if strings.Contains(result.Content, "sample.txt") {
		t.Fatalf("expected sample.txt to be excluded by -g *.go, got %q", result.Content)
	}
	if !strings.Contains(result.Content, "line2") || !strings.Contains(result.Content, "line4") {
		t.Fatalf("expected before/after context lines in output, got %q", result.Content)
	}
	if result.Metadata["before_context"] != 1 || result.Metadata["after_context"] != 1 {
		t.Fatalf("expected before/after context metadata to be 1, got %#v", result.Metadata)
	}
	if result.Metadata["engine"] != "builtin" {
		t.Fatalf("expected builtin engine, got %#v", result.Metadata["engine"])
	}
}

func TestGrepTool_RgAliasesTranslateToRipgrepArgs(t *testing.T) {
	tool := NewGrepTool()
	var gotArgs []string

	tool.lookPath = func(name string) (string, error) {
		return "rg", nil
	}
	tool.runCommand = func(ctx context.Context, binaryPath, workingDir string, args []string) ([]byte, error) {
		gotArgs = append([]string(nil), args...)
		return []byte("main.go:1:hello\n"), nil
	}

	_, _ = tool.Execute(context.Background(), map[string]interface{}{
		"pattern":            "hello",
		"path":               t.TempDir(),
		"glob":               []interface{}{"*.go", "!*.test.go"},
		"type_not":           "py",
		"files_with_matches": true,
		"fixed_strings":      true,
	})
	argsStr := strings.Join(gotArgs, " ")

	if !strings.Contains(argsStr, "--glob *.go") {
		t.Fatalf("expected --glob *.go in rg args, got %v", gotArgs)
	}
	if !strings.Contains(argsStr, "--glob !*.test.go") {
		t.Fatalf("expected --glob !*.test.go in rg args, got %v", gotArgs)
	}
	if !strings.Contains(argsStr, "--type-not py") {
		t.Fatalf("expected --type-not py in rg args, got %v", gotArgs)
	}
	if !strings.Contains(argsStr, "--files-with-matches") {
		t.Fatalf("expected --files-with-matches in rg args, got %v", gotArgs)
	}
	if !strings.Contains(argsStr, "-F") {
		t.Fatalf("expected -F in rg args, got %v", gotArgs)
	}
}

func TestNormalizeRipgrepOutput_ContextLines(t *testing.T) {
	lines := normalizeRipgrepOutput([]byte("pkg/my-file.go-2-line2\npkg/my-file.go:3:line3 TARGET\npkg/my-file.go-4-line4\n"))
	if len(lines) != 3 {
		t.Fatalf("expected 3 normalized lines, got %d (%v)", len(lines), lines)
	}
	if lines[0] != "pkg/my-file.go:2: line2" {
		t.Fatalf("unexpected normalized context line: %q", lines[0])
	}
	if lines[1] != "pkg/my-file.go:3: line3 TARGET" {
		t.Fatalf("unexpected normalized match line: %q", lines[1])
	}
	if lines[2] != "pkg/my-file.go:4: line4" {
		t.Fatalf("unexpected normalized trailing context line: %q", lines[2])
	}
}

func TestGrepTool_DescriptionMentionsRgArgsCompatibility(t *testing.T) {
	tool := NewGrepTool()
	tool.lookPath = func(name string) (string, error) {
		return "/usr/bin/rg", nil
	}

	desc := tool.Description()
	if !strings.Contains(desc, "rg_args") {
		t.Fatalf("expected description to mention rg_args compatibility, got %q", desc)
	}
	if !strings.Contains(desc, "glob≈-g") {
		t.Fatalf("expected description to mention rg-style option mapping, got %q", desc)
	}
	if !strings.Contains(desc, "iglob≈--iglob") {
		t.Fatalf("expected description to mention iglob compatibility, got %q", desc)
	}
	if !strings.Contains(desc, "glob_case_insensitive≈--glob-case-insensitive") {
		t.Fatalf("expected description to mention glob_case_insensitive compatibility, got %q", desc)
	}
	if !strings.Contains(desc, "pattern_file/pattern_files≈-f/--file") {
		t.Fatalf("expected description to mention pattern_file compatibility, got %q", desc)
	}
	if !strings.Contains(desc, "pcre2≈-P/--pcre2") {
		t.Fatalf("expected description to mention pcre2 compatibility, got %q", desc)
	}
	if !strings.Contains(desc, "multiline≈-U/--multiline") {
		t.Fatalf("expected description to mention multiline compatibility, got %q", desc)
	}
	if !strings.Contains(desc, "replace≈-r/--replace") {
		t.Fatalf("expected description to mention replace compatibility, got %q", desc)
	}
	if !strings.Contains(desc, "-e") {
		t.Fatalf("expected description to mention repeated -e pattern compatibility, got %q", desc)
	}
	if !strings.Contains(desc, "-o") {
		t.Fatalf("expected description to mention only-matching compatibility, got %q", desc)
	}
	if !strings.Contains(desc, "paths") {
		t.Fatalf("expected description to mention multi-path compatibility, got %q", desc)
	}
	if !strings.Contains(desc, "max_filesize") {
		t.Fatalf("expected description to mention max_filesize compatibility, got %q", desc)
	}
	if !strings.Contains(desc, "src/**/*.go") {
		t.Fatalf("expected description to mention path-aware glob examples, got %q", desc)
	}
	if !strings.Contains(desc, "max_depth/max_count 显式 0 语义") {
		t.Fatalf("expected description to mention explicit zero semantics, got %q", desc)
	}
	if !strings.Contains(desc, "patterns.txt") {
		t.Fatalf("expected description to mention pattern file examples, got %q", desc)
	}
	if !strings.Contains(desc, "foo.*bar") {
		t.Fatalf("expected description to mention pcre2 rg_args example, got %q", desc)
	}
	if !strings.Contains(desc, "rg-only") {
		t.Fatalf("expected description to mention rg-only passthrough behavior, got %q", desc)
	}
}

func TestGrepTool_MultiplePatternsBuiltin(t *testing.T) {
	tmpDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmpDir, "a.txt"), []byte("foo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tmpDir, "b.txt"), []byte("bar\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	tool := NewGrepTool()
	tool.lookPath = func(name string) (string, error) {
		return "", os.ErrNotExist
	}

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"patterns": []interface{}{"foo", "bar"},
		"path":     tmpDir,
		"mode":     "files",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected success, got error %v", result.Error)
	}
	if !strings.Contains(result.Content, "a.txt") || !strings.Contains(result.Content, "b.txt") {
		t.Fatalf("expected both a.txt and b.txt in output, got %q", result.Content)
	}
	patterns, ok := result.Metadata["patterns"].([]string)
	if !ok || len(patterns) != 2 {
		t.Fatalf("expected metadata patterns to contain 2 entries, got %#v", result.Metadata["patterns"])
	}
}

func TestGrepTool_LineRegexpAndInvertMatchBuiltin(t *testing.T) {
	tmpDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmpDir, "sample.txt"), []byte("foo\nfoobar\nbar\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	tool := NewGrepTool()
	tool.lookPath = func(name string) (string, error) {
		return "", os.ErrNotExist
	}

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"pattern":     "foo",
		"path":        tmpDir,
		"line_regexp": true,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected success, got error %v", result.Error)
	}
	if !strings.Contains(result.Content, "sample.txt:1: foo") {
		t.Fatalf("expected exact full-line match, got %q", result.Content)
	}
	if strings.Contains(result.Content, "foobar") {
		t.Fatalf("expected line_regexp to exclude foobar, got %q", result.Content)
	}

	result, err = tool.Execute(context.Background(), map[string]interface{}{
		"pattern":      "foo",
		"path":         tmpDir,
		"line_regexp":  true,
		"invert_match": true,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected success, got error %v", result.Error)
	}
	if strings.Contains(result.Content, "sample.txt:1: foo") {
		t.Fatalf("expected invert_match to exclude exact foo line, got %q", result.Content)
	}
	if !strings.Contains(result.Content, "foobar") || !strings.Contains(result.Content, "bar") {
		t.Fatalf("expected invert_match to return non-matching lines, got %q", result.Content)
	}
}

func TestGrepTool_FilesWithoutMatchBuiltin(t *testing.T) {
	tmpDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmpDir, "hit.txt"), []byte("foo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tmpDir, "miss.txt"), []byte("bar\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	tool := NewGrepTool()
	tool.lookPath = func(name string) (string, error) {
		return "", os.ErrNotExist
	}

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"pattern":             "foo",
		"path":                tmpDir,
		"files_without_match": true,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected success, got error %v", result.Error)
	}
	if !strings.Contains(result.Content, "miss.txt") {
		t.Fatalf("expected miss.txt in files_without_match output, got %q", result.Content)
	}
	if strings.Contains(result.Content, "hit.txt") {
		t.Fatalf("expected hit.txt to be excluded from files_without_match output, got %q", result.Content)
	}
}

func TestGrepTool_RgArgsMultiplePatternsAndFlagsTranslateToRipgrep(t *testing.T) {
	tool := NewGrepTool()
	var gotArgs []string

	tool.lookPath = func(name string) (string, error) {
		return "rg", nil
	}
	tool.runCommand = func(ctx context.Context, binaryPath, workingDir string, args []string) ([]byte, error) {
		gotArgs = append([]string(nil), args...)
		return []byte("sample.txt\n"), nil
	}

	result, _ := tool.Execute(context.Background(), map[string]interface{}{
		"rg_args": []interface{}{"-e", "foo", "-e", "bar", "-L", "-x", "-v", t.TempDir()},
	})
	argsStr := strings.Join(gotArgs, " ")

	if !strings.Contains(argsStr, "-e foo") || !strings.Contains(argsStr, "-e bar") {
		t.Fatalf("expected repeated -e patterns in rg args, got %v", gotArgs)
	}
	if !strings.Contains(argsStr, "--files-without-match") {
		t.Fatalf("expected --files-without-match in rg args, got %v", gotArgs)
	}
	if !strings.Contains(argsStr, "--line-regexp") {
		t.Fatalf("expected --line-regexp in rg args, got %v", gotArgs)
	}
	if !strings.Contains(argsStr, "--invert-match") {
		t.Fatalf("expected --invert-match in rg args, got %v", gotArgs)
	}
	if result.Metadata["mode"] != string(grepModeFilesWithout) {
		t.Fatalf("expected mode=files_without, got %#v", result.Metadata["mode"])
	}
	if result.Metadata["line_regexp"] != true || result.Metadata["invert_match"] != true {
		t.Fatalf("expected line_regexp/invert_match metadata, got %#v", result.Metadata)
	}
}

func TestGrepTool_OnlyMatchingBuiltin(t *testing.T) {
	tmpDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmpDir, "sample.txt"), []byte("foo foo\nbar\nfoobar foo\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	tool := NewGrepTool()
	tool.lookPath = func(name string) (string, error) {
		return "", os.ErrNotExist
	}

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"pattern":       "foo",
		"path":          tmpDir,
		"only_matching": true,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected success, got error %v", result.Error)
	}
	lines := strings.Split(strings.TrimSpace(result.Content), "\n")
	if len(lines) != 4 {
		t.Fatalf("expected 4 only-matching results, got %d (%q)", len(lines), result.Content)
	}
	if result.Metadata["only_matching"] != true {
		t.Fatalf("expected only_matching metadata, got %#v", result.Metadata["only_matching"])
	}
}

func TestGrepTool_OnlyMatchingWithInvertMatchBuiltin(t *testing.T) {
	tmpDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmpDir, "sample.txt"), []byte("foo\nbar\nfoobar\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	tool := NewGrepTool()
	tool.lookPath = func(name string) (string, error) {
		return "", os.ErrNotExist
	}

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"pattern":       "foo",
		"path":          tmpDir,
		"only_matching": true,
		"invert_match":  true,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected success, got error %v", result.Error)
	}
	if result.Content != "sample.txt:2: bar" {
		t.Fatalf("expected invert + only_matching builtin fallback to return full non-matching line, got %q", result.Content)
	}
}

func TestGrepTool_SingleFilePathBuiltinUsesFilename(t *testing.T) {
	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "single.txt")
	if err := os.WriteFile(filePath, []byte("needle\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	tool := NewGrepTool()
	tool.lookPath = func(name string) (string, error) {
		return "", os.ErrNotExist
	}

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"pattern": "needle",
		"path":    filePath,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected success, got error %v", result.Error)
	}
	if result.Content != "single.txt:1: needle" {
		t.Fatalf("expected single-file path output to use filename, got %q", result.Content)
	}
}

func TestGrepTool_SingleFilePathRipgrepUsesParentDirAndTarget(t *testing.T) {
	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "single.txt")
	if err := os.WriteFile(filePath, []byte("needle\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	tool := NewGrepTool()
	var gotDir string
	var gotArgs []string
	tool.lookPath = func(name string) (string, error) {
		return "rg", nil
	}
	tool.runCommand = func(ctx context.Context, binaryPath, workingDir string, args []string) ([]byte, error) {
		gotDir = workingDir
		gotArgs = append([]string(nil), args...)
		return []byte("single.txt:1:needle\n"), nil
	}

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"pattern": "needle",
		"path":    filePath,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected success, got error %v", result.Error)
	}
	if gotDir != tmpDir {
		t.Fatalf("expected rg working dir to be file parent %q, got %q", tmpDir, gotDir)
	}
	argsStr := strings.Join(gotArgs, " ")
	if !strings.Contains(argsStr, "single.txt") {
		t.Fatalf("expected rg args to include explicit single-file target, got %v", gotArgs)
	}
	if result.Content != "single.txt:1: needle" {
		t.Fatalf("unexpected normalized content: %q", result.Content)
	}
}

func TestGrepTool_RgOnlyMatchingTranslatesToRipgrep(t *testing.T) {
	tool := NewGrepTool()
	var gotArgs []string

	tool.lookPath = func(name string) (string, error) {
		return "rg", nil
	}
	tool.runCommand = func(ctx context.Context, binaryPath, workingDir string, args []string) ([]byte, error) {
		gotArgs = append([]string(nil), args...)
		return []byte("sample.txt:1:foo\n"), nil
	}

	_, _ = tool.Execute(context.Background(), map[string]interface{}{
		"pattern":       "foo",
		"path":          t.TempDir(),
		"only_matching": true,
	})
	if !strings.Contains(strings.Join(gotArgs, " "), "--only-matching") {
		t.Fatalf("expected --only-matching in rg args, got %v", gotArgs)
	}
}

func TestGrepTool_MultiPathBuiltin(t *testing.T) {
	rootA := t.TempDir()
	rootB := t.TempDir()
	if err := os.WriteFile(filepath.Join(rootA, "a.txt"), []byte("needle\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(rootB, "b.txt"), []byte("needle\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	tool := NewGrepTool()
	tool.lookPath = func(name string) (string, error) {
		return "", os.ErrNotExist
	}

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"pattern": "needle",
		"paths":   []interface{}{rootA, rootB},
		"mode":    "files",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected success, got error %v", result.Error)
	}
	wantA := filepath.ToSlash(filepath.Join(rootA, "a.txt"))
	wantB := filepath.ToSlash(filepath.Join(rootB, "b.txt"))
	if !strings.Contains(result.Content, wantA) || !strings.Contains(result.Content, wantB) {
		t.Fatalf("expected multi-path output to include %q and %q, got %q", wantA, wantB, result.Content)
	}
	paths, ok := result.Metadata["paths"].([]string)
	if !ok || len(paths) != 2 {
		t.Fatalf("expected metadata paths to contain 2 entries, got %#v", result.Metadata["paths"])
	}
}

func TestGrepTool_RgArgsMultiplePathsBuiltin(t *testing.T) {
	rootA := t.TempDir()
	rootB := t.TempDir()
	if err := os.WriteFile(filepath.Join(rootA, "a.txt"), []byte("needle\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(rootB, "b.txt"), []byte("needle\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	tool := NewGrepTool()
	tool.lookPath = func(name string) (string, error) {
		return "", os.ErrNotExist
	}

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"rg_args": []interface{}{"needle", rootA, rootB},
		"mode":    "files",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected success, got error %v", result.Error)
	}
	if !strings.Contains(result.Content, filepath.ToSlash(filepath.Join(rootA, "a.txt"))) {
		t.Fatalf("expected first rg_args path result, got %q", result.Content)
	}
	if !strings.Contains(result.Content, filepath.ToSlash(filepath.Join(rootB, "b.txt"))) {
		t.Fatalf("expected second rg_args path result, got %q", result.Content)
	}
}

func TestGrepTool_MaxFilesizeBuiltin(t *testing.T) {
	tmpDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmpDir, "small.txt"), []byte("foo"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tmpDir, "large.txt"), []byte(strings.Repeat("foo", 10)), 0o644); err != nil {
		t.Fatal(err)
	}

	tool := NewGrepTool()
	tool.lookPath = func(name string) (string, error) {
		return "", os.ErrNotExist
	}

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"pattern":      "foo",
		"path":         tmpDir,
		"mode":         "files",
		"max_filesize": "5B",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected success, got error %v", result.Error)
	}
	if !strings.Contains(result.Content, "small.txt") {
		t.Fatalf("expected small.txt to remain searchable, got %q", result.Content)
	}
	if strings.Contains(result.Content, "large.txt") {
		t.Fatalf("expected large.txt to be skipped by max_filesize, got %q", result.Content)
	}
}

func TestGrepTool_RgMaxFilesizeTranslatesAndCountAggregates(t *testing.T) {
	rootA := t.TempDir()
	rootB := t.TempDir()
	tool := NewGrepTool()
	var gotDirs []string
	var gotArgs [][]string
	call := 0

	tool.lookPath = func(name string) (string, error) {
		return "rg", nil
	}
	tool.runCommand = func(ctx context.Context, binaryPath, workingDir string, args []string) ([]byte, error) {
		gotDirs = append(gotDirs, workingDir)
		gotArgs = append(gotArgs, append([]string(nil), args...))
		call++
		if call == 1 {
			return []byte("a.txt:2\n"), nil
		}
		return []byte("b.txt:3\n"), nil
	}

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"pattern":      "foo",
		"paths":        []interface{}{rootA, rootB},
		"mode":         "count",
		"max_filesize": "1K",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected success, got error %v", result.Error)
	}
	if len(gotDirs) != 2 || gotDirs[0] != rootA || gotDirs[1] != rootB {
		t.Fatalf("expected two rg calls with per-scope working dirs, got %#v", gotDirs)
	}
	for _, args := range gotArgs {
		if !strings.Contains(strings.Join(args, " "), "--max-filesize 1K") {
			t.Fatalf("expected --max-filesize 1K in rg args, got %v", args)
		}
	}
	if result.Metadata["match_count"] != 5 {
		t.Fatalf("expected aggregated match_count=5 for count mode, got %#v", result.Metadata["match_count"])
	}
}

func TestGrepTool_PathAwareGlobsBuiltin(t *testing.T) {
	tmpDir := t.TempDir()
	mustWrite := func(rel string) {
		target := filepath.Join(tmpDir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(target, []byte("needle\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	mustWrite("pkg/main.go")
	mustWrite("pkg/nested/deep.go")
	mustWrite("vendor/skip.go")

	tool := NewGrepTool()
	tool.lookPath = func(name string) (string, error) {
		return "", os.ErrNotExist
	}

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"pattern": "needle",
		"path":    tmpDir,
		"mode":    "files",
		"glob":    []interface{}{"pkg/*.go"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected success, got error %v", result.Error)
	}
	if !strings.Contains(result.Content, "pkg/main.go") {
		t.Fatalf("expected pkg/main.go to match pkg/*.go, got %q", result.Content)
	}
	if strings.Contains(result.Content, "pkg/nested/deep.go") {
		t.Fatalf("expected pkg/*.go not to match nested file, got %q", result.Content)
	}

	result, err = tool.Execute(context.Background(), map[string]interface{}{
		"pattern": "needle",
		"path":    tmpDir,
		"mode":    "files",
		"glob":    []interface{}{"**/*.go", "!vendor/**"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected success, got error %v", result.Error)
	}
	if !strings.Contains(result.Content, "pkg/main.go") || !strings.Contains(result.Content, "pkg/nested/deep.go") {
		t.Fatalf("expected **/*.go to include pkg files, got %q", result.Content)
	}
	if strings.Contains(result.Content, "vendor/skip.go") {
		t.Fatalf("expected !vendor/** to exclude vendor file, got %q", result.Content)
	}
}

func TestGrepTool_RgArgsIglobBuiltinCaseInsensitive(t *testing.T) {
	tmpDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmpDir, "UPPER.GO"), []byte("needle\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	tool := NewGrepTool()
	tool.lookPath = func(name string) (string, error) {
		return "", os.ErrNotExist
	}

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"rg_args": []interface{}{"--iglob", "*.go", "needle", tmpDir},
		"mode":    "files",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected success, got error %v", result.Error)
	}
	if !strings.Contains(result.Content, "UPPER.GO") {
		t.Fatalf("expected --iglob *.go to match uppercase extension, got %q", result.Content)
	}
}

func TestGrepTool_GlobCaseInsensitiveBuiltin(t *testing.T) {
	tmpDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmpDir, "UPPER.GO"), []byte("needle\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	tool := NewGrepTool()
	tool.lookPath = func(name string) (string, error) {
		return "", os.ErrNotExist
	}

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"pattern":               "needle",
		"path":                  tmpDir,
		"mode":                  "files",
		"glob":                  []interface{}{"*.go"},
		"glob_case_insensitive": true,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected success, got error %v", result.Error)
	}
	if !strings.Contains(result.Content, "UPPER.GO") {
		t.Fatalf("expected glob_case_insensitive to match uppercase extension, got %q", result.Content)
	}
	if result.Metadata["glob_case_insensitive"] != true {
		t.Fatalf("expected glob_case_insensitive metadata, got %#v", result.Metadata["glob_case_insensitive"])
	}
}

func TestGrepTool_RgArgsGlobCaseInsensitiveBuiltin(t *testing.T) {
	tmpDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmpDir, "UPPER.GO"), []byte("needle\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	tool := NewGrepTool()
	tool.lookPath = func(name string) (string, error) {
		return "", os.ErrNotExist
	}

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"rg_args": []interface{}{"--glob-case-insensitive", "-g", "*.go", "needle", tmpDir},
		"mode":    "files",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected success, got error %v", result.Error)
	}
	if !strings.Contains(result.Content, "UPPER.GO") {
		t.Fatalf("expected rg_args --glob-case-insensitive to match uppercase extension, got %q", result.Content)
	}
}

func TestGrepTool_RgArgsIglobTranslatesToRipgrep(t *testing.T) {
	tool := NewGrepTool()
	var gotArgs []string

	tool.lookPath = func(name string) (string, error) {
		return "rg", nil
	}
	tool.runCommand = func(ctx context.Context, binaryPath, workingDir string, args []string) ([]byte, error) {
		gotArgs = append([]string(nil), args...)
		return []byte("sample.GO\n"), nil
	}

	_, _ = tool.Execute(context.Background(), map[string]interface{}{
		"rg_args": []interface{}{"--iglob", "*.go", "needle", t.TempDir()},
		"mode":    "files",
	})
	if !strings.Contains(strings.Join(gotArgs, " "), "--iglob *.go") {
		t.Fatalf("expected --iglob *.go in rg args, got %v", gotArgs)
	}
}

func TestGrepTool_GlobCaseInsensitiveTranslatesToRipgrep(t *testing.T) {
	tool := NewGrepTool()
	var gotArgs []string

	tool.lookPath = func(name string) (string, error) {
		return "rg", nil
	}
	tool.runCommand = func(ctx context.Context, binaryPath, workingDir string, args []string) ([]byte, error) {
		gotArgs = append([]string(nil), args...)
		return []byte("sample.GO\n"), nil
	}

	_, _ = tool.Execute(context.Background(), map[string]interface{}{
		"pattern":               "needle",
		"path":                  t.TempDir(),
		"mode":                  "files",
		"glob":                  []interface{}{"*.go"},
		"glob_case_insensitive": true,
	})
	argsStr := strings.Join(gotArgs, " ")
	if !strings.Contains(argsStr, "--glob-case-insensitive") {
		t.Fatalf("expected --glob-case-insensitive in rg args, got %v", gotArgs)
	}
	if !strings.Contains(argsStr, "--glob *.go") {
		t.Fatalf("expected --glob *.go in rg args, got %v", gotArgs)
	}
}

func TestGrepTool_RgZeroLimitsTranslateToRipgrep(t *testing.T) {
	tool := NewGrepTool()
	var gotArgs []string

	tool.lookPath = func(name string) (string, error) {
		return "rg", nil
	}
	tool.runCommand = func(ctx context.Context, binaryPath, workingDir string, args []string) ([]byte, error) {
		gotArgs = append([]string(nil), args...)
		return []byte(""), nil
	}

	_, _ = tool.Execute(context.Background(), map[string]interface{}{
		"pattern":   "needle",
		"path":      t.TempDir(),
		"max_depth": 0,
		"max_count": 0,
	})
	argsStr := strings.Join(gotArgs, " ")
	if !strings.Contains(argsStr, "--max-depth 0") {
		t.Fatalf("expected --max-depth 0 in rg args, got %v", gotArgs)
	}
	if !strings.Contains(argsStr, "--max-count 0") {
		t.Fatalf("expected --max-count 0 in rg args, got %v", gotArgs)
	}
}

func TestGrepTool_PatternFileBuiltin(t *testing.T) {
	tmpDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmpDir, "sample.txt"), []byte("foo\nbar\nbaz\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	patternFile := filepath.Join(tmpDir, "patterns.txt")
	if err := os.WriteFile(patternFile, []byte("foo\nbar\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	tool := NewGrepTool()
	tool.lookPath = func(name string) (string, error) {
		return "", os.ErrNotExist
	}

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"pattern_file": patternFile,
		"path":         tmpDir,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected success, got error %v", result.Error)
	}
	if !strings.Contains(result.Content, "sample.txt:1: foo") || !strings.Contains(result.Content, "sample.txt:2: bar") {
		t.Fatalf("expected pattern_file to match foo and bar, got %q", result.Content)
	}
	files, ok := result.Metadata["pattern_files"].([]string)
	if !ok || len(files) != 1 || files[0] != patternFile {
		t.Fatalf("expected pattern_files metadata to include patternFile, got %#v", result.Metadata["pattern_files"])
	}
}

func TestGrepTool_EmptyPatternFileBuiltinReturnsNoMatches(t *testing.T) {
	tmpDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmpDir, "sample.txt"), []byte("foo\nbar\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	patternFile := filepath.Join(tmpDir, "empty.txt")
	if err := os.WriteFile(patternFile, []byte{}, 0o644); err != nil {
		t.Fatal(err)
	}

	tool := NewGrepTool()
	tool.lookPath = func(name string) (string, error) {
		return "", os.ErrNotExist
	}

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"pattern_file": patternFile,
		"path":         tmpDir,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected success, got error %v", result.Error)
	}
	if result.Content != "未找到匹配的内容" {
		t.Fatalf("expected empty pattern_file to behave like rg and return no matches, got %q", result.Content)
	}
	if result.Metadata["match_count"] != 0 {
		t.Fatalf("expected empty pattern_file match_count=0, got %#v", result.Metadata["match_count"])
	}
}

func TestGrepTool_SmartCasePatternFileBuiltin(t *testing.T) {
	tmpDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmpDir, "sample.txt"), []byte("Hello\nhello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	patternFile := filepath.Join(tmpDir, "patterns.txt")
	if err := os.WriteFile(patternFile, []byte("Hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	tool := NewGrepTool()
	tool.lookPath = func(name string) (string, error) {
		return "", os.ErrNotExist
	}

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"pattern_file": patternFile,
		"path":         tmpDir,
		"smart_case":   true,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected success, got error %v", result.Error)
	}
	if !strings.Contains(result.Content, "sample.txt:1: Hello") {
		t.Fatalf("expected smart_case pattern file to match Hello, got %q", result.Content)
	}
	if strings.Contains(result.Content, "sample.txt:2: hello") {
		t.Fatalf("expected smart_case pattern file to stay case-sensitive for uppercase pattern, got %q", result.Content)
	}
	if result.Metadata["smart_case"] != true || result.Metadata["ignore_case"] != false {
		t.Fatalf("expected smart_case metadata and effective ignore_case=false, got %#v", result.Metadata)
	}
}

func TestGrepTool_PatternFilesBuiltinBlankLineMatchesAll(t *testing.T) {
	tmpDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmpDir, "sample.txt"), []byte("foo\nbar\nbaz\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	patternFileA := filepath.Join(tmpDir, "a.txt")
	patternFileB := filepath.Join(tmpDir, "b.txt")
	if err := os.WriteFile(patternFileA, []byte("foo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(patternFileB, []byte("\nbar\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	tool := NewGrepTool()
	tool.lookPath = func(name string) (string, error) {
		return "", os.ErrNotExist
	}

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"pattern_files": []interface{}{patternFileA, patternFileB},
		"path":          tmpDir,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected success, got error %v", result.Error)
	}
	if !strings.Contains(result.Content, "sample.txt:3: baz") {
		t.Fatalf("expected blank line in pattern file to match all lines like rg, got %q", result.Content)
	}
	patterns, ok := result.Metadata["patterns"].([]string)
	if !ok || len(patterns) < 3 {
		t.Fatalf("expected loaded patterns metadata, got %#v", result.Metadata["patterns"])
	}
}

func TestGrepTool_RgArgsPatternFileBuiltin(t *testing.T) {
	tmpDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmpDir, "sample.txt"), []byte("foo\nbar\nbaz\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	patternFile := filepath.Join(tmpDir, "patterns.txt")
	if err := os.WriteFile(patternFile, []byte("bar\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	tool := NewGrepTool()
	tool.lookPath = func(name string) (string, error) {
		return "", os.ErrNotExist
	}

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"rg_args": []interface{}{"-f", patternFile, tmpDir},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected success, got error %v", result.Error)
	}
	if !strings.Contains(result.Content, "sample.txt:2: bar") {
		t.Fatalf("expected -f pattern file to match bar, got %q", result.Content)
	}
}

func TestGrepTool_RgPatternFileTranslatesToRipgrep(t *testing.T) {
	tmpDir := t.TempDir()
	patternFile := filepath.Join(tmpDir, "patterns.txt")
	if err := os.WriteFile(patternFile, []byte("foo\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	tool := NewGrepTool()
	var gotArgs []string
	tool.lookPath = func(name string) (string, error) {
		return "rg", nil
	}
	tool.runCommand = func(ctx context.Context, binaryPath, workingDir string, args []string) ([]byte, error) {
		gotArgs = append([]string(nil), args...)
		return []byte("sample.txt:1:foo\n"), nil
	}

	_, _ = tool.Execute(context.Background(), map[string]interface{}{
		"pattern_file": patternFile,
		"path":         tmpDir,
	})
	argsStr := strings.Join(gotArgs, " ")
	if !strings.Contains(argsStr, "-f "+patternFile) {
		t.Fatalf("expected -f patternFile in rg args, got %v", gotArgs)
	}
}

func TestGrepTool_RgPcre2TranslatesToRipgrep(t *testing.T) {
	tool := NewGrepTool()
	var gotArgs []string

	tool.lookPath = func(name string) (string, error) {
		return "rg", nil
	}
	tool.runCommand = func(ctx context.Context, binaryPath, workingDir string, args []string) ([]byte, error) {
		gotArgs = append([]string(nil), args...)
		return []byte("sample.txt:1:foo\n"), nil
	}

	_, _ = tool.Execute(context.Background(), map[string]interface{}{
		"rg_args": []interface{}{"-P", "foo", t.TempDir()},
	})
	if !strings.Contains(strings.Join(gotArgs, " "), "--pcre2") {
		t.Fatalf("expected --pcre2 in rg args, got %v", gotArgs)
	}
}

func TestGrepTool_RgPcre2RequiresRipgrep(t *testing.T) {
	tmpDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmpDir, "sample.txt"), []byte("foo\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	tool := NewGrepTool()
	tool.lookPath = func(name string) (string, error) {
		return "", os.ErrNotExist
	}

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"rg_args": []interface{}{"-P", "foo", tmpDir},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Success {
		t.Fatalf("expected rg-only pcre2 mode to fail cleanly without rg")
	}
	if !strings.Contains(result.Error.Error(), "仅 ripgrep/rg 支持") {
		t.Fatalf("expected clear rg-only error, got %v", result.Error)
	}
}

func TestGrepTool_RgPcre2RipgrepErrorDoesNotFallback(t *testing.T) {
	tool := NewGrepTool()
	tool.lookPath = func(name string) (string, error) {
		return "rg", nil
	}
	tool.runCommand = func(ctx context.Context, binaryPath, workingDir string, args []string) ([]byte, error) {
		return nil, os.ErrInvalid
	}

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"rg_args": []interface{}{"-P", "foo", t.TempDir()},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Success {
		t.Fatalf("expected rg-only pcre2 runtime failure to surface instead of fallback success")
	}
	if !strings.Contains(result.Error.Error(), "ripgrep/rg 执行失败") {
		t.Fatalf("expected ripgrep execution error, got %v", result.Error)
	}
}

func TestGrepTool_RgOnlyArgsTranslateToRipgrep(t *testing.T) {
	tool := NewGrepTool()
	var gotArgs []string

	tool.lookPath = func(name string) (string, error) {
		return "rg", nil
	}
	tool.runCommand = func(ctx context.Context, binaryPath, workingDir string, args []string) ([]byte, error) {
		gotArgs = append([]string(nil), args...)
		return []byte("sample.txt:1:X\n"), nil
	}

	_, _ = tool.Execute(context.Background(), map[string]interface{}{
		"rg_args": []interface{}{"-U", "--multiline-dotall", "--passthru", "--crlf", "-r", "X", "foo", t.TempDir()},
	})
	argsStr := strings.Join(gotArgs, " ")
	for _, want := range []string{"--multiline", "--multiline-dotall", "--passthru", "--crlf", "--replace X"} {
		if !strings.Contains(argsStr, want) {
			t.Fatalf("expected %q in rg args, got %v", want, gotArgs)
		}
	}
}

func TestGrepTool_RgOnlyArgsRequireRipgrep(t *testing.T) {
	tmpDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmpDir, "sample.txt"), []byte("foo\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	tool := NewGrepTool()
	tool.lookPath = func(name string) (string, error) {
		return "", os.ErrNotExist
	}

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"rg_args": []interface{}{"-U", "-r", "X", "foo", tmpDir},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Success {
		t.Fatalf("expected rg-only multiline/replace request to fail cleanly without rg")
	}
	if !strings.Contains(result.Error.Error(), "仅 ripgrep/rg 支持") {
		t.Fatalf("expected clear rg-only error, got %v", result.Error)
	}
}

func TestGrepTool_RgArgsCombinedShortFlagsBuiltin(t *testing.T) {
	tmpDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmpDir, "sample.GO"), []byte("HELLO\nbye\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tmpDir, "sample.txt"), []byte("HELLO\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	tool := NewGrepTool()
	tool.lookPath = func(name string) (string, error) {
		return "", os.ErrNotExist
	}

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"rg_args": []interface{}{"-iwg*.GO", "hello", tmpDir},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected success, got error %v", result.Error)
	}
	if !strings.Contains(result.Content, "sample.GO:1: HELLO") {
		t.Fatalf("expected combined short flags to match sample.GO, got %q", result.Content)
	}
	if strings.Contains(result.Content, "sample.txt") {
		t.Fatalf("expected -g*.GO to exclude sample.txt, got %q", result.Content)
	}
}
