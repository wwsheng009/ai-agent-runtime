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
		"pattern":      "helloworld",
		"path":         tmpDir,
		"ignore_case":  true,
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
		"pattern":    "main",
		"path":       t.TempDir(),
		"context":    3,
		"word":       true,
		"ignore_case": true,
		"exclude":    "*.test.ts",
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
