package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/toolkit"
)

func TestGlobTool(t *testing.T) {
	// 创建测试目录结构
	tmpDir, err := os.MkdirTemp("", "glob-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	// 创建测试文件
	testFiles := []string{
		"file1.go",
		"file2.go",
		"file3.txt",
		"subdir/file4.go",
		"subdir/file5.txt",
		"subdir/nested/file6.go",
	}

	for _, f := range testFiles {
		path := filepath.Join(tmpDir, f)
		dir := filepath.Dir(path)
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("test"), 0644); err != nil {
			t.Fatal(err)
		}
	}

	tests := []struct {
		name      string
		params    map[string]interface{}
		wantCount int
		wantError bool
		wantPaths []string
	}{
		{
			name: "match all go files",
			params: map[string]interface{}{
				"pattern": "**/*.go",
				"path":    tmpDir,
			},
			wantCount: 4, // file1.go, file2.go, subdir/file4.go, subdir/nested/file6.go
			wantError: false,
			wantPaths: []string{
				"file1.go",
				"file2.go",
				filepath.Join("subdir", "file4.go"),
				filepath.Join("subdir", "nested", "file6.go"),
			},
		},
		{
			name: "match top-level go files",
			params: map[string]interface{}{
				"pattern": "*.go",
				"path":    tmpDir,
			},
			wantCount: 2, // file1.go, file2.go
			wantError: false,
			wantPaths: []string{"file1.go", "file2.go"},
		},
		{
			name: "match exact root file",
			params: map[string]interface{}{
				"pattern": "file3.txt",
				"path":    tmpDir,
			},
			wantCount: 1,
			wantError: false,
			wantPaths: []string{"file3.txt"},
		},
		{
			name: "match single file path",
			params: map[string]interface{}{
				"pattern": "**/*.txt",
				"path":    filepath.Join(tmpDir, "file3.txt"),
			},
			wantCount: 1,
			wantError: false,
			wantPaths: []string{"file3.txt"},
		},
		{
			name: "match txt files",
			params: map[string]interface{}{
				"pattern": "*.txt",
				"path":    tmpDir,
			},
			wantCount: 1, // file3.txt (top level only)
			wantError: false,
			wantPaths: []string{"file3.txt"},
		},
		{
			name: "match docs directories recursively",
			params: map[string]interface{}{
				"pattern": "**/docs",
				"path":    tmpDir,
			},
			wantCount: 0,
			wantError: false,
		},
		{
			name: "match paths under docs recursively",
			params: map[string]interface{}{
				"pattern": "**/docs/**",
				"path":    tmpDir,
			},
			wantCount: 0,
			wantError: false,
		},
		{
			name: "missing pattern",
			params: map[string]interface{}{
				"path": tmpDir,
			},
			wantCount: 0,
			wantError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tool := NewGlobTool()
			if tt.name == "match docs directories recursively" || tt.name == "match paths under docs recursively" {
				requireNoError(t, os.MkdirAll(filepath.Join(tmpDir, "docs", "aicli"), 0755))
				requireNoError(t, os.MkdirAll(filepath.Join(tmpDir, "nested", "docs"), 0755))
				requireNoError(t, os.WriteFile(filepath.Join(tmpDir, "docs", "README.md"), []byte("docs"), 0644))
				requireNoError(t, os.WriteFile(filepath.Join(tmpDir, "docs", "aicli", "README.md"), []byte("aicli"), 0644))
				requireNoError(t, os.WriteFile(filepath.Join(tmpDir, "nested", "docs", "guide.md"), []byte("guide"), 0644))
				if tt.name == "match docs directories recursively" {
					tt.wantCount = 2
					tt.wantPaths = []string{"docs", filepath.Join("nested", "docs")}
				} else {
					tt.wantCount = 6
					tt.wantPaths = []string{
						"docs",
						filepath.Join("docs", "README.md"),
						filepath.Join("docs", "aicli"),
						filepath.Join("docs", "aicli", "README.md"),
						filepath.Join("nested", "docs"),
						filepath.Join("nested", "docs", "guide.md"),
					}
				}
			}
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

			if !result.Success {
				t.Errorf("unexpected failure: %v", result.Error)
				return
			}

			// Check result contains expected count
			files, ok := result.Metadata["files"].([]string)
			if !ok {
				t.Fatalf("expected files metadata, got: %#v", result.Metadata)
			}
			if len(files) != tt.wantCount {
				t.Errorf("expected %d files, got %d: %v", tt.wantCount, len(files), files)
			}
			for _, want := range tt.wantPaths {
				found := false
				for _, got := range files {
					if filepath.Clean(got) == filepath.Clean(want) {
						found = true
						break
					}
				}
				if !found {
					t.Fatalf("expected %q in matches, got %v", want, files)
				}
			}
		})
	}
}

func TestGlobTool_MissingSearchPath(t *testing.T) {
	tool := NewGlobTool()
	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"pattern": "*.go",
		"path":    filepath.Join(t.TempDir(), "missing"),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Success {
		t.Fatalf("expected missing path to fail, got success: %s", result.Content)
	}
	if result.Error == nil || !strings.Contains(result.Error.Error(), "搜索路径不可用") {
		t.Fatalf("expected searchable path error, got: %v", result.Error)
	}
}

func TestGlobTool_MissingSearchPathIncludesCandidateHint(t *testing.T) {
	root := t.TempDir()
	candidate := filepath.Join(root, "project", "settings")
	if err := os.MkdirAll(candidate, 0o755); err != nil {
		t.Fatalf("mkdir candidate tree: %v", err)
	}

	tool := NewGlobTool()
	tool.SetBasePath(root)
	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"pattern": "*.go",
		"path":    "project/setting",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Success {
		t.Fatalf("expected missing path to fail, got success: %s", result.Content)
	}
	if result.Error == nil {
		t.Fatal("expected path error, got nil")
	}
	hint := result.Error.Error()
	if !strings.Contains(hint, "搜索路径不可用") {
		t.Fatalf("expected searchable path error, got: %v", result.Error)
	}
	if !strings.Contains(hint, candidate) {
		t.Fatalf("expected candidate path %q in hint, got %q", candidate, hint)
	}
}

func TestGlobTool_LimitTruncation(t *testing.T) {
	tmpDir := t.TempDir()
	for i := 0; i < 5; i++ {
		name := filepath.Join(tmpDir, "file"+string(rune('0'+i))+".go")
		if err := os.WriteFile(name, []byte("test"), 0644); err != nil {
			t.Fatal(err)
		}
	}

	tool := NewGlobTool()

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"pattern": "*.go",
		"path":    tmpDir,
		"limit":   2,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected success, got error: %v", result.Error)
	}

	files, ok := result.Metadata["files"].([]string)
	if !ok {
		t.Fatalf("expected files metadata, got: %#v", result.Metadata)
	}
	if len(files) != 2 {
		t.Fatalf("expected 2 files after truncation, got %d: %v", len(files), files)
	}
	if truncated, ok := result.Metadata["truncated"].(bool); !ok || !truncated {
		t.Fatalf("expected truncated metadata to be true, got: %#v", result.Metadata["truncated"])
	}
	if limitHit, ok := result.Metadata["limit_hit"].(bool); !ok || !limitHit {
		t.Fatalf("expected limit_hit metadata to be true, got: %#v", result.Metadata["limit_hit"])
	}
	if !strings.Contains(result.Content, "结果已截断") {
		t.Fatalf("expected truncation hint in content, got: %s", result.Content)
	}
	if limit, ok := result.Metadata["limit"].(int); !ok || limit != 2 {
		t.Fatalf("expected limit metadata to be 2, got: %#v", result.Metadata["limit"])
	}
	if returnedCount, ok := result.Metadata["returned_count"].(int); !ok || returnedCount != 2 {
		t.Fatalf("expected returned_count metadata to be 2, got: %#v", result.Metadata["returned_count"])
	}
	if count, ok := result.Metadata["count"].(int); !ok || count != 2 {
		t.Fatalf("expected count metadata to be 2, got: %#v", result.Metadata["count"])
	}
}

func TestGlobTool_InvalidLimit(t *testing.T) {
	tool := NewGlobTool()
	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"pattern": "*.go",
		"path":    t.TempDir(),
		"limit":   0,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Success {
		t.Fatalf("expected invalid limit to fail, got success: %s", result.Content)
	}
	if result.Error == nil || !strings.Contains(result.Error.Error(), "limit 参数必须大于 0") {
		t.Fatalf("expected limit validation error, got: %v", result.Error)
	}
}

func TestGlobTool_NullLimitIgnored(t *testing.T) {
	tmpDir := t.TempDir()
	for i := 0; i < 3; i++ {
		name := filepath.Join(tmpDir, "file"+string(rune('0'+i))+".go")
		if err := os.WriteFile(name, []byte("test"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	tool := NewGlobTool()
	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"pattern": "*.go",
		"path":    tmpDir,
		"limit":   nil,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected success, got error: %v", result.Error)
	}
	files, ok := result.Metadata["files"].([]string)
	if !ok {
		t.Fatalf("expected files metadata, got: %#v", result.Metadata)
	}
	if len(files) != 3 {
		t.Fatalf("expected 3 files when null limit is ignored, got %d: %v", len(files), files)
	}
}

func TestGlobTool_UsesRipgrepFilesWhenAvailable(t *testing.T) {
	tmpDir := t.TempDir()
	tool := NewGlobTool()

	var gotWorkingDir string
	var gotArgs []string
	tool.lookPath = func(name string) (string, error) {
		if name != "rg" {
			t.Fatalf("expected rg lookup, got %q", name)
		}
		return "rg", nil
	}
	tool.runCommand = func(ctx context.Context, binaryPath, workingDir string, args []string) ([]byte, error) {
		if binaryPath != "rg" {
			t.Fatalf("expected rg binary, got %q", binaryPath)
		}
		gotWorkingDir = workingDir
		gotArgs = append([]string(nil), args...)
		return []byte("subdir\\file.go\nsubdir\\file.txt\nother.go\n"), nil
	}

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"pattern": "**/*.go",
		"path":    tmpDir,
		"limit":   1,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected success, got error: %v", result.Error)
	}
	if gotWorkingDir != tmpDir {
		t.Fatalf("expected rg working dir %q, got %q", tmpDir, gotWorkingDir)
	}
	wantArgs := []string{"--files", "--hidden", "--no-ignore", "--glob", "**/*.go"}
	if strings.Join(gotArgs, "\x00") != strings.Join(wantArgs, "\x00") {
		t.Fatalf("unexpected rg args: got %v want %v", gotArgs, wantArgs)
	}
	files := result.Metadata["files"].([]string)
	if len(files) != 1 || filepath.Clean(files[0]) != filepath.Clean("subdir/file.go") {
		t.Fatalf("expected first matching go file only, got %v", files)
	}
	if engine := result.Metadata["engine"]; engine != "rg" {
		t.Fatalf("expected rg engine metadata, got %#v", engine)
	}
	if truncated, ok := result.Metadata["truncated"].(bool); !ok || !truncated {
		t.Fatalf("expected rg result to be truncated, got %#v", result.Metadata["truncated"])
	}
}

func TestGlobTool_CaseInsensitiveUsesRipgrepIglob(t *testing.T) {
	tmpDir := t.TempDir()
	tool := NewGlobTool()

	var gotArgs []string
	tool.lookPath = func(name string) (string, error) {
		return "rg", nil
	}
	tool.runCommand = func(ctx context.Context, binaryPath, workingDir string, args []string) ([]byte, error) {
		gotArgs = append([]string(nil), args...)
		return []byte("pages\\BotsPage.tsx\n"), nil
	}

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"pattern":          "**/*bot*page*.tsx",
		"path":             tmpDir,
		"case_insensitive": true,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected success, got error: %v", result.Error)
	}
	if strings.Join(gotArgs, "\x00") != strings.Join([]string{"--files", "--hidden", "--no-ignore", "--iglob", "**/*bot*page*.tsx"}, "\x00") {
		t.Fatalf("unexpected rg args: %v", gotArgs)
	}
	files := result.Metadata["files"].([]string)
	if len(files) != 1 || filepath.Clean(files[0]) != filepath.Clean("pages/BotsPage.tsx") {
		t.Fatalf("expected case-insensitive match, got %v", files)
	}
	if caseInsensitive := result.Metadata["case_insensitive"]; caseInsensitive != true {
		t.Fatalf("expected case_insensitive metadata true, got %#v", caseInsensitive)
	}
}

func TestGlobTool_FallsBackToWalkerWhenRipgrepUnavailable(t *testing.T) {
	tmpDir := t.TempDir()
	requireNoError(t, os.MkdirAll(filepath.Join(tmpDir, "subdir"), 0755))
	requireNoError(t, os.WriteFile(filepath.Join(tmpDir, "subdir", "file.go"), []byte("test"), 0644))

	tool := NewGlobTool()
	tool.lookPath = func(name string) (string, error) {
		return "", os.ErrNotExist
	}
	tool.runCommand = func(ctx context.Context, binaryPath, workingDir string, args []string) ([]byte, error) {
		t.Fatalf("rg command should not run when rg is unavailable")
		return nil, nil
	}

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"pattern": "**/*.go",
		"path":    tmpDir,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected success, got error: %v", result.Error)
	}
	files := result.Metadata["files"].([]string)
	if len(files) != 1 || filepath.Clean(files[0]) != filepath.Clean("subdir/file.go") {
		t.Fatalf("expected builtin walker match, got %v", files)
	}
	if engine := result.Metadata["engine"]; engine != "builtin" {
		t.Fatalf("expected builtin engine metadata, got %#v", engine)
	}
}

func TestGlobTool_CaseInsensitiveWalkerMatchesStaticPrefixCaseDifferences(t *testing.T) {
	tmpDir := t.TempDir()
	requireNoError(t, os.MkdirAll(filepath.Join(tmpDir, "Src"), 0755))
	requireNoError(t, os.WriteFile(filepath.Join(tmpDir, "Src", "File.GO"), []byte("test"), 0644))

	tool := NewGlobTool()
	tool.lookPath = func(name string) (string, error) {
		return "", os.ErrNotExist
	}

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"pattern":          "src/file.go",
		"path":             tmpDir,
		"case_insensitive": true,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected success, got error: %v", result.Error)
	}
	files := result.Metadata["files"].([]string)
	if len(files) != 1 || filepath.Clean(files[0]) != filepath.Clean("Src/File.GO") {
		t.Fatalf("expected case-insensitive walker match, got %v", files)
	}
}

func TestGlobTool_DirectoryPatternUsesBuiltinWalker(t *testing.T) {
	tmpDir := t.TempDir()
	requireNoError(t, os.MkdirAll(filepath.Join(tmpDir, "docs"), 0755))
	requireNoError(t, os.MkdirAll(filepath.Join(tmpDir, "nested", "docs"), 0755))

	tool := NewGlobTool()
	tool.lookPath = func(name string) (string, error) {
		return "rg", nil
	}
	tool.runCommand = func(ctx context.Context, binaryPath, workingDir string, args []string) ([]byte, error) {
		t.Fatalf("directory glob should use builtin walker")
		return nil, nil
	}

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"pattern": "**/docs",
		"path":    tmpDir,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected success, got error: %v", result.Error)
	}
	files := result.Metadata["files"].([]string)
	if len(files) != 2 {
		t.Fatalf("expected two docs directories, got %v", files)
	}
	if engine := result.Metadata["engine"]; engine != "builtin" {
		t.Fatalf("expected builtin engine metadata, got %#v", engine)
	}
}

func requireNoError(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func TestGlobTool_Interface(t *testing.T) {
	tool := NewGlobTool()

	// Test Tool interface
	var _ toolkit.Tool = tool

	if tool.Name() != "glob" {
		t.Errorf("expected name 'glob', got '%s'", tool.Name())
	}

	if tool.Description() == "" {
		t.Error("description should not be empty")
	}

	if tool.Version() != "1.0.0" {
		t.Errorf("expected version '1.0.0', got '%s'", tool.Version())
	}

	if !tool.CanDirectCall() {
		t.Error("glob tool should support direct call")
	}

	params := tool.Parameters()
	if params == nil {
		t.Error("parameters should not be nil")
	}

	if params["type"] != "object" {
		t.Error("parameters should be of type object")
	}
}

func TestGlobTool_DescriptionGuidesSingleTargetFocus(t *testing.T) {
	tool := NewGlobTool()

	desc := tool.Description()
	if !strings.Contains(desc, "rg --files") || !strings.Contains(desc, "case_insensitive") || strings.Contains(desc, "文件内容") && !strings.Contains(desc, "不搜索文件内容") {
		t.Fatalf("expected glob description to guide fast path and path-only usage, got %q", desc)
	}

	params := tool.Parameters()
	props, ok := params["properties"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected properties in schema, got %#v", params)
	}
	patternSchema, ok := props["pattern"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected pattern schema in properties, got %#v", props)
	}
	patternDesc, _ := patternSchema["description"].(string)
	if !strings.Contains(patternDesc, "不搜索文件内容") || !strings.Contains(patternDesc, "case_insensitive") {
		t.Fatalf("expected pattern description to guide path-only case-insensitive usage, got %q", patternDesc)
	}
	caseSchema, ok := props["case_insensitive"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected case_insensitive schema in properties, got %#v", props)
	}
	caseDesc, _ := caseSchema["description"].(string)
	if !strings.Contains(caseDesc, "大小写") {
		t.Fatalf("expected case_insensitive description, got %q", caseDesc)
	}
}
