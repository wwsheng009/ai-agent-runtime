package tools

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/toolkit"
)

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

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

func TestGrepTool_DefaultEngineFallsBackWhenRipgrepUnavailable(t *testing.T) {
	tmpDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmpDir, "sample.txt"), []byte("needle\n"), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}

	cases := []struct {
		name   string
		params map[string]interface{}
	}{
		{
			name: "structured engine",
			params: map[string]interface{}{
				"pattern": "needle",
				"path":    tmpDir,
				"engine":  "default",
			},
		},
		{
			name: "rg_args engine",
			params: map[string]interface{}{
				"rg_args": []interface{}{"--engine", "default", "needle", tmpDir},
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tool := NewGrepTool()
			tool.lookPath = func(name string) (string, error) {
				return "", os.ErrNotExist
			}

			result, err := tool.Execute(context.Background(), tc.params)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !result.Success {
				t.Fatalf("expected success, got error %v", result.Error)
			}
			if result.Metadata["engine"] != "builtin" {
				t.Fatalf("expected builtin engine fallback, got %#v", result.Metadata["engine"])
			}
			if result.Metadata["requires_ripgrep"] != false {
				t.Fatalf("expected default engine to avoid requiring ripgrep, got %#v", result.Metadata["requires_ripgrep"])
			}
			if !strings.Contains(result.Content, "sample.txt:1: needle") {
				t.Fatalf("expected builtin search to find needle, got %q", result.Content)
			}
		})
	}
}

func TestGrepTool_NoIgnoreAndUnrestrictedBuiltin(t *testing.T) {
	tmpDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(tmpDir, ".git"), 0o755); err != nil {
		t.Fatalf("mkdir .git: %v", err)
	}
	if err := os.WriteFile(filepath.Join(tmpDir, ".hidden.txt"), []byte("needle\n"), 0o644); err != nil {
		t.Fatalf("write hidden file: %v", err)
	}
	if err := os.WriteFile(filepath.Join(tmpDir, ".git", "tracked.go"), []byte("needle\n"), 0o644); err != nil {
		t.Fatalf("write hidden file: %v", err)
	}

	tool := NewGrepTool()
	tool.lookPath = func(name string) (string, error) {
		return "", os.ErrNotExist
	}

	baseResult, err := tool.Execute(context.Background(), map[string]interface{}{
		"pattern": "needle",
		"path":    tmpDir,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.Contains(baseResult.Content, ".hidden.txt") || strings.Contains(baseResult.Content, ".git/tracked.go") {
		t.Fatalf("expected baseline search to skip hidden paths, got %q", baseResult.Content)
	}

	noIgnoreResult, err := tool.Execute(context.Background(), map[string]interface{}{
		"pattern":   "needle",
		"path":      tmpDir,
		"no_ignore": true,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.Contains(noIgnoreResult.Content, ".hidden.txt") || strings.Contains(noIgnoreResult.Content, ".git/tracked.go") {
		t.Fatalf("expected no_ignore to keep hidden filtering, got %q", noIgnoreResult.Content)
	}
	if noIgnoreResult.Metadata["no_ignore"] != true {
		t.Fatalf("expected no_ignore metadata, got %#v", noIgnoreResult.Metadata["no_ignore"])
	}
	if noIgnoreResult.Metadata["unrestricted_level"] != 1 {
		t.Fatalf("expected unrestricted_level=1 for no_ignore, got %#v", noIgnoreResult.Metadata["unrestricted_level"])
	}

	unrestrictedHiddenResult, err := tool.Execute(context.Background(), map[string]interface{}{
		"rg_args": []interface{}{"-uu", "needle", tmpDir},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(unrestrictedHiddenResult.Content, ".hidden.txt:1: needle") || !strings.Contains(unrestrictedHiddenResult.Content, ".git/tracked.go:1: needle") {
		t.Fatalf("expected -uu to search hidden paths, got %q", unrestrictedHiddenResult.Content)
	}
	if unrestrictedHiddenResult.Metadata["unrestricted_level"] != 2 {
		t.Fatalf("expected unrestricted_level=2 for -uu, got %#v", unrestrictedHiddenResult.Metadata["unrestricted_level"])
	}

	unrestrictedResult, err := tool.Execute(context.Background(), map[string]interface{}{
		"rg_args": []interface{}{"-uuu", "needle", tmpDir},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(unrestrictedResult.Content, ".hidden.txt:1: needle") || !strings.Contains(unrestrictedResult.Content, ".git/tracked.go:1: needle") {
		t.Fatalf("expected -uuu to search .git directory, got %q", unrestrictedResult.Content)
	}
	if unrestrictedResult.Metadata["no_ignore"] != true {
		t.Fatalf("expected unrestricted metadata to imply no_ignore, got %#v", unrestrictedResult.Metadata["no_ignore"])
	}
	if unrestrictedResult.Metadata["unrestricted_level"] != 3 {
		t.Fatalf("expected unrestricted_level=3 for -uuu, got %#v", unrestrictedResult.Metadata["unrestricted_level"])
	}
}

func TestGrepTool_HiddenAndNoHiddenBuiltin(t *testing.T) {
	tmpDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmpDir, "visible.txt"), []byte("needle\n"), 0o644); err != nil {
		t.Fatalf("write visible file: %v", err)
	}
	if err := os.WriteFile(filepath.Join(tmpDir, ".hidden.txt"), []byte("needle\n"), 0o644); err != nil {
		t.Fatalf("write hidden file: %v", err)
	}

	tool := NewGrepTool()
	tool.lookPath = func(name string) (string, error) {
		return "", os.ErrNotExist
	}

	baseResult, err := tool.Execute(context.Background(), map[string]interface{}{
		"pattern": "needle",
		"path":    tmpDir,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.Contains(baseResult.Content, ".hidden.txt") {
		t.Fatalf("expected default search to skip hidden file, got %q", baseResult.Content)
	}

	hiddenResult, err := tool.Execute(context.Background(), map[string]interface{}{
		"pattern": "needle",
		"path":    tmpDir,
		"hidden":  true,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(hiddenResult.Content, ".hidden.txt:1: needle") {
		t.Fatalf("expected hidden=true to include hidden file, got %q", hiddenResult.Content)
	}

	noHiddenResult, err := tool.Execute(context.Background(), map[string]interface{}{
		"pattern":   "needle",
		"path":      tmpDir,
		"hidden":    true,
		"no_hidden": true,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.Contains(noHiddenResult.Content, ".hidden.txt") {
		t.Fatalf("expected no_hidden to win over hidden=true, got %q", noHiddenResult.Content)
	}

	unrestrictedNoHiddenResult, err := tool.Execute(context.Background(), map[string]interface{}{
		"pattern":            "needle",
		"path":               tmpDir,
		"unrestricted_level": 2,
		"no_hidden":          true,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.Contains(unrestrictedNoHiddenResult.Content, ".hidden.txt") {
		t.Fatalf("expected no_hidden to win over unrestricted_level=2, got %q", unrestrictedNoHiddenResult.Content)
	}
}

func TestGrepTool_NodeModulesAreNotSpecialHiddenBuiltin(t *testing.T) {
	tmpDir := t.TempDir()
	nodeModules := filepath.Join(tmpDir, "node_modules", "pkg")
	if err := os.MkdirAll(nodeModules, 0o755); err != nil {
		t.Fatalf("mkdir node_modules: %v", err)
	}
	if err := os.WriteFile(filepath.Join(nodeModules, "index.txt"), []byte("needle\n"), 0o644); err != nil {
		t.Fatalf("write node_modules file: %v", err)
	}

	tool := NewGrepTool()
	tool.lookPath = func(name string) (string, error) {
		return "", os.ErrNotExist
	}

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"pattern": "needle",
		"path":    tmpDir,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(result.Content, "node_modules/pkg/index.txt:1: needle") {
		t.Fatalf("expected node_modules to be searchable by default, got %q", result.Content)
	}
}

func TestGrepTool_NoConfigAndOneFileSystemCompatibility(t *testing.T) {
	tool := NewGrepTool()
	var gotArgs []string

	tool.lookPath = func(name string) (string, error) {
		return "rg", nil
	}
	tool.runCommand = func(ctx context.Context, binaryPath, workingDir string, args []string) ([]byte, error) {
		gotArgs = append([]string(nil), args...)
		return []byte("sample.txt:1:needle\n"), nil
	}

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"pattern":         "needle",
		"path":            t.TempDir(),
		"no_config":       true,
		"one_file_system": true,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected success, got error %v", result.Error)
	}
	argsStr := strings.Join(gotArgs, " ")
	if !strings.Contains(argsStr, "--no-config") {
		t.Fatalf("expected --no-config in rg args, got %v", gotArgs)
	}
	if !strings.Contains(argsStr, "--one-file-system") {
		t.Fatalf("expected --one-file-system in rg args, got %v", gotArgs)
	}
	if result.Metadata["no_config"] != true {
		t.Fatalf("expected no_config metadata, got %#v", result.Metadata["no_config"])
	}
	if result.Metadata["one_file_system"] != true {
		t.Fatalf("expected one_file_system metadata, got %#v", result.Metadata["one_file_system"])
	}
}

func TestGrepTool_OneFileSystemBuiltinSkipsCrossDeviceDirectories(t *testing.T) {
	tmpDir := t.TempDir()
	subDir := filepath.Join(tmpDir, "sub")
	if err := os.MkdirAll(subDir, 0o755); err != nil {
		t.Fatalf("mkdir sub dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(tmpDir, "root.txt"), []byte("needle\n"), 0o644); err != nil {
		t.Fatalf("write root file: %v", err)
	}
	if err := os.WriteFile(filepath.Join(subDir, "sub.txt"), []byte("needle\n"), 0o644); err != nil {
		t.Fatalf("write sub file: %v", err)
	}

	origFSID := fileSystemIdentityFn
	defer func() { fileSystemIdentityFn = origFSID }()
	subPrefix := filepath.ToSlash(subDir)
	fileSystemIdentityFn = func(path string) (string, error) {
		cleaned := filepath.ToSlash(filepath.Clean(path))
		if strings.HasPrefix(cleaned, subPrefix) {
			return "other-fs", nil
		}
		return "root-fs", nil
	}

	tool := NewGrepTool()
	tool.lookPath = func(name string) (string, error) {
		return "", os.ErrNotExist
	}

	baseResult, err := tool.Execute(context.Background(), map[string]interface{}{
		"pattern": "needle",
		"path":    tmpDir,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(baseResult.Content, "sub/sub.txt:1: needle") {
		t.Fatalf("expected baseline search to include subdir file, got %q", baseResult.Content)
	}

	oneFSResult, err := tool.Execute(context.Background(), map[string]interface{}{
		"pattern":         "needle",
		"path":            tmpDir,
		"one_file_system": true,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.Contains(oneFSResult.Content, "sub/sub.txt") {
		t.Fatalf("expected one_file_system to skip cross-device subdir, got %q", oneFSResult.Content)
	}
	if !strings.Contains(oneFSResult.Content, "root.txt:1: needle") {
		t.Fatalf("expected root file to remain searchable, got %q", oneFSResult.Content)
	}

	directFile := filepath.Join(tmpDir, "external.txt")
	if err := os.WriteFile(directFile, []byte("needle\n"), 0o644); err != nil {
		t.Fatalf("write direct file: %v", err)
	}
	fileSystemIdentityFn = func(path string) (string, error) {
		cleaned := filepath.ToSlash(filepath.Clean(path))
		switch cleaned {
		case filepath.ToSlash(directFile):
			return "other-fs", nil
		default:
			return "root-fs", nil
		}
	}
	directResult, err := tool.Execute(context.Background(), map[string]interface{}{
		"pattern":         "needle",
		"path":            directFile,
		"one_file_system": true,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(directResult.Content, "external.txt:1: needle") {
		t.Fatalf("expected explicit file path to remain searchable across filesystem boundary, got %q", directResult.Content)
	}

	linkFile := filepath.Join(tmpDir, "link.txt")
	if err := os.Symlink(directFile, linkFile); err == nil {
		fileSystemIdentityFn = func(path string) (string, error) {
			cleaned := filepath.ToSlash(filepath.Clean(path))
			switch cleaned {
			case filepath.ToSlash(linkFile):
				return "other-fs", nil
			default:
				return "root-fs", nil
			}
		}
		linkResult, err := tool.Execute(context.Background(), map[string]interface{}{
			"pattern":         "needle",
			"path":            linkFile,
			"one_file_system": true,
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !strings.Contains(linkResult.Content, "link.txt:1: needle") {
			t.Fatalf("expected explicit symlink path to remain searchable across filesystem boundary, got %q", linkResult.Content)
		}
	} else {
		t.Logf("skipping symlink boundary check: %v", err)
	}
}

func TestGrepTool_BuiltinIgnoreFileAppliesPatterns(t *testing.T) {
	tmpDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmpDir, "keep.txt"), []byte("needle\n"), 0o644); err != nil {
		t.Fatalf("write keep file: %v", err)
	}
	if err := os.WriteFile(filepath.Join(tmpDir, "skip.log"), []byte("needle\n"), 0o644); err != nil {
		t.Fatalf("write skip file: %v", err)
	}
	if err := os.WriteFile(filepath.Join(tmpDir, ".rgignore"), []byte("*.log\n"), 0o644); err != nil {
		t.Fatalf("write ignore file: %v", err)
	}

	tool := NewGrepTool()
	tool.lookPath = func(name string) (string, error) {
		return "", os.ErrNotExist
	}

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"pattern":     "needle",
		"path":        tmpDir,
		"ignore_file": ".rgignore",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected success, got error %v", result.Error)
	}
	if !strings.Contains(result.Content, "keep.txt:1: needle") {
		t.Fatalf("expected keep.txt to match, got %q", result.Content)
	}
	if strings.Contains(result.Content, "skip.log") {
		t.Fatalf("expected skip.log to be ignored by ignore_file, got %q", result.Content)
	}
	if got, ok := result.Metadata["ignore_files"].([]string); !ok || len(got) != 1 || got[0] != ".rgignore" {
		t.Fatalf("expected ignore_files metadata, got %#v", result.Metadata["ignore_files"])
	}
}

func TestGrepTool_IgnoreHierarchyFlagsBuiltin(t *testing.T) {
	outerDir := t.TempDir()
	rootDir := filepath.Join(outerDir, "project")
	if err := os.MkdirAll(rootDir, 0o755); err != nil {
		t.Fatalf("mkdir root dir: %v", err)
	}
	subDir := filepath.Join(rootDir, "sub")
	if err := os.MkdirAll(subDir, 0o755); err != nil {
		t.Fatalf("mkdir sub dir: %v", err)
	}

	tempHome := t.TempDir()
	t.Setenv("HOME", tempHome)
	t.Setenv("USERPROFILE", tempHome)
	if err := os.MkdirAll(filepath.Join(tempHome, ".config", "git"), 0o755); err != nil {
		t.Fatalf("mkdir global ignore dir: %v", err)
	}

	files := map[string]string{
		filepath.Join(rootDir, "keep.txt"):   "needle\n",
		filepath.Join(rootDir, "dot.txt"):    "needle\n",
		filepath.Join(rootDir, "vcs.txt"):    "needle\n",
		filepath.Join(rootDir, "global.txt"): "needle\n",
		filepath.Join(rootDir, "parent.txt"): "needle\n",
		filepath.Join(subDir, "nested.txt"):  "needle\n",
	}
	for filePath, content := range files {
		if err := os.WriteFile(filePath, []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", filePath, err)
		}
	}
	if err := os.WriteFile(filepath.Join(outerDir, ".rgignore"), []byte("parent.txt\n"), 0o644); err != nil {
		t.Fatalf("write parent ignore: %v", err)
	}
	if err := os.WriteFile(filepath.Join(rootDir, ".ignore"), []byte("dot.txt\n"), 0o644); err != nil {
		t.Fatalf("write dot ignore: %v", err)
	}
	if err := os.WriteFile(filepath.Join(rootDir, ".gitignore"), []byte("vcs.txt\n"), 0o644); err != nil {
		t.Fatalf("write vcs ignore: %v", err)
	}
	if err := os.WriteFile(filepath.Join(subDir, ".gitignore"), []byte("nested.txt\n"), 0o644); err != nil {
		t.Fatalf("write nested vcs ignore: %v", err)
	}
	if err := os.WriteFile(filepath.Join(tempHome, ".config", "git", "ignore"), []byte("global.txt\n"), 0o644); err != nil {
		t.Fatalf("write global ignore: %v", err)
	}

	tool := NewGrepTool()
	tool.lookPath = func(name string) (string, error) {
		return "", os.ErrNotExist
	}

	baseResult, err := tool.Execute(context.Background(), map[string]interface{}{
		"pattern": "needle",
		"path":    rootDir,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for _, want := range []string{"keep.txt:1: needle"} {
		if !strings.Contains(baseResult.Content, want) {
			t.Fatalf("expected base search to keep %q, got %q", want, baseResult.Content)
		}
	}
	for _, ignored := range []string{"parent.txt", "dot.txt", "vcs.txt", "global.txt", "sub/nested.txt"} {
		if strings.Contains(baseResult.Content, ignored) {
			t.Fatalf("expected %q to be ignored by default, got %q", ignored, baseResult.Content)
		}
	}

	tests := []struct {
		name      string
		params    map[string]interface{}
		wantMatch string
	}{
		{name: "parent", params: map[string]interface{}{"no_ignore_parent": true}, wantMatch: "parent.txt:1: needle"},
		{name: "dot", params: map[string]interface{}{"no_ignore_dot": true}, wantMatch: "dot.txt:1: needle"},
		{name: "vcs", params: map[string]interface{}{"no_ignore_vcs": true}, wantMatch: "vcs.txt:1: needle"},
		{name: "nested vcs", params: map[string]interface{}{"no_ignore_vcs": true}, wantMatch: "sub/nested.txt:1: needle"},
		{name: "global", params: map[string]interface{}{"no_ignore_global": true}, wantMatch: "global.txt:1: needle"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			params := map[string]interface{}{
				"pattern": "needle",
				"path":    rootDir,
			}
			for k, v := range tt.params {
				params[k] = v
			}
			result, err := tool.Execute(context.Background(), params)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !strings.Contains(result.Content, tt.wantMatch) {
				t.Fatalf("expected %s to be searchable, got %q", tt.wantMatch, result.Content)
			}
		})
	}
}

func TestGrepTool_NoMessagesSuppressesMissingPatternFileError(t *testing.T) {
	tmpDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmpDir, "keep.txt"), []byte("needle\n"), 0o644); err != nil {
		t.Fatalf("write keep file: %v", err)
	}

	tool := NewGrepTool()
	tool.lookPath = func(name string) (string, error) {
		return "", os.ErrNotExist
	}

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"pattern":      "needle",
		"path":         tmpDir,
		"pattern_file": "missing-patterns.txt",
		"no_messages":  true,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected success with no_messages, got error %v", result.Error)
	}
	if !strings.Contains(result.Content, "keep.txt:1: needle") {
		t.Fatalf("expected search to continue despite missing pattern_file, got %q", result.Content)
	}
	if result.Metadata["no_messages"] != true {
		t.Fatalf("expected no_messages metadata, got %#v", result.Metadata["no_messages"])
	}
}

func TestGrepTool_PathNotFoundIncludesCandidateHint(t *testing.T) {
	root := t.TempDir()
	candidateDir := filepath.Join(root, "project", "settings")
	if err := os.MkdirAll(candidateDir, 0o755); err != nil {
		t.Fatalf("mkdir candidate dir: %v", err)
	}
	candidatePattern := filepath.Join(candidateDir, "patterns.txt")
	if err := os.WriteFile(candidatePattern, []byte("needle\n"), 0o644); err != nil {
		t.Fatalf("write candidate pattern file: %v", err)
	}

	tests := []struct {
		name        string
		params      map[string]interface{}
		wantContain string
	}{
		{
			name: "missing search path",
			params: map[string]interface{}{
				"pattern": "needle",
				"path":    "project/setting",
			},
			wantContain: candidateDir,
		},
		{
			name: "missing pattern file",
			params: map[string]interface{}{
				"pattern":      "needle",
				"path":         root,
				"pattern_file": "project/setting/patterns.txt",
			},
			wantContain: candidatePattern,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tool := NewGrepTool()
			tool.SetBasePath(root)
			tool.lookPath = func(name string) (string, error) {
				return "", os.ErrNotExist
			}

			result, err := tool.Execute(context.Background(), tt.params)
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
			if !strings.Contains(hint, "不存在") {
				t.Fatalf("expected missing path message, got %q", hint)
			}
			if !strings.Contains(hint, tt.wantContain) {
				t.Fatalf("expected candidate path %q in hint, got %q", tt.wantContain, hint)
			}
		})
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
	if !strings.Contains(desc, "内置扫描") || !strings.Contains(desc, "工具定义保持静态") {
		t.Fatalf("expected stable description to mention builtin fallback and static tool definition, got %q", desc)
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
	if !strings.Contains(desc, "工具定义保持静态") {
		t.Fatalf("expected description to be stable across rg availability, got %q", desc)
	}
}

func TestGrepTool_DescriptionStableAcrossRgAvailability(t *testing.T) {
	withRg := NewGrepTool()
	withRg.lookPath = func(name string) (string, error) {
		return "/usr/bin/rg", nil
	}
	withoutRg := NewGrepTool()
	withoutRg.lookPath = func(name string) (string, error) {
		return "", os.ErrNotExist
	}

	if withRg.Description() != withoutRg.Description() {
		t.Fatalf("expected grep tool description to stay stable across rg availability")
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

func TestGrepTool_ContextSeparatorBuiltin(t *testing.T) {
	tmpDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmpDir, "a.txt"), []byte("one\nTARGET\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tmpDir, "b.txt"), []byte("two\nTARGET\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	tool := NewGrepTool()
	tool.lookPath = func(name string) (string, error) {
		return "", os.ErrNotExist
	}

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"pattern":           "TARGET",
		"path":              tmpDir,
		"context":           1,
		"context_separator": "~~~",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected success, got error %v", result.Error)
	}
	if !strings.Contains(result.Content, "~~~") {
		t.Fatalf("expected custom context separator in output, got %q", result.Content)
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

func TestGrepTool_RgArgsContextSeparatorTranslateToRipgrep(t *testing.T) {
	tool := NewGrepTool()
	var gotArgs []string

	tool.lookPath = func(name string) (string, error) {
		return "rg", nil
	}
	tool.runCommand = func(ctx context.Context, binaryPath, workingDir string, args []string) ([]byte, error) {
		gotArgs = append([]string(nil), args...)
		return []byte("main.go:1: foo\n"), nil
	}

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"pattern": "foo",
		"path":    t.TempDir(),
		"context": 1,
		"rg_args": []interface{}{"--context-separator", "~~~"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected success, got error %v", result.Error)
	}
	argsStr := strings.Join(gotArgs, " ")
	if !strings.Contains(argsStr, "--context-separator ~~~") {
		t.Fatalf("expected context separator to translate to rg args, got %v", gotArgs)
	}
	if result.Metadata["context_separator"] != "~~~" {
		t.Fatalf("expected context_separator metadata, got %#v", result.Metadata["context_separator"])
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
	if !strings.Contains(desc, "ignore_file≈--ignore-file") {
		t.Fatalf("expected description to mention ignore_file compatibility, got %q", desc)
	}
	if !strings.Contains(desc, "ignore_file_case_insensitive≈--ignore-file-case-insensitive") {
		t.Fatalf("expected description to mention ignore_file_case_insensitive compatibility, got %q", desc)
	}
	if !strings.Contains(desc, "no_ignore_files≈--no-ignore-files") {
		t.Fatalf("expected description to mention no_ignore_files compatibility, got %q", desc)
	}
	if !strings.Contains(desc, "pcre2≈-P/--pcre2") {
		t.Fatalf("expected description to mention pcre2 compatibility, got %q", desc)
	}
	if !strings.Contains(desc, "engine≈--engine") {
		t.Fatalf("expected description to mention engine compatibility, got %q", desc)
	}
	if !strings.Contains(desc, "multiline_dotall≈--multiline-dotall") {
		t.Fatalf("expected description to mention multiline_dotall compatibility, got %q", desc)
	}
	if !strings.Contains(desc, "passthru≈--passthru") {
		t.Fatalf("expected description to mention passthru compatibility, got %q", desc)
	}
	if !strings.Contains(desc, "auto_hybrid_regex≈--auto-hybrid-regex") {
		t.Fatalf("expected description to mention auto_hybrid_regex compatibility, got %q", desc)
	}
	if !strings.Contains(desc, "multiline≈-U/--multiline") {
		t.Fatalf("expected description to mention multiline compatibility, got %q", desc)
	}
	if !strings.Contains(desc, "replace≈-r/--replace") {
		t.Fatalf("expected description to mention replace compatibility, got %q", desc)
	}
	if !strings.Contains(desc, "column≈--column") {
		t.Fatalf("expected description to mention column compatibility, got %q", desc)
	}
	if !strings.Contains(desc, "trim≈--trim") {
		t.Fatalf("expected description to mention trim compatibility, got %q", desc)
	}
	if !strings.Contains(desc, "pretty≈--pretty") {
		t.Fatalf("expected description to mention pretty compatibility, got %q", desc)
	}
	if !strings.Contains(desc, "line_buffered≈--line-buffered") {
		t.Fatalf("expected description to mention line_buffered compatibility, got %q", desc)
	}
	if !strings.Contains(desc, "block_buffered≈--block-buffered") {
		t.Fatalf("expected description to mention block_buffered compatibility, got %q", desc)
	}
	if !strings.Contains(desc, "null/null_data≈--null/--null-data") {
		t.Fatalf("expected description to mention null/null_data compatibility, got %q", desc)
	}
	if !strings.Contains(desc, "no_ignore_parent/vcs/global/dot≈--no-ignore-parent/--no-ignore-vcs/--no-ignore-global/--no-ignore-dot") {
		t.Fatalf("expected description to mention no_ignore_* compatibility, got %q", desc)
	}
	if !strings.Contains(desc, "-u/-uu/-uuu/--unrestricted") {
		t.Fatalf("expected description to mention unrestricted compatibility, got %q", desc)
	}
	if !strings.Contains(desc, "no_config≈--no-config") {
		t.Fatalf("expected description to mention no_config compatibility, got %q", desc)
	}
	if !strings.Contains(desc, "one_file_system≈--one-file-system") {
		t.Fatalf("expected description to mention one_file_system compatibility, got %q", desc)
	}
	if !strings.Contains(desc, "no_messages≈--no-messages") {
		t.Fatalf("expected description to mention no_messages compatibility, got %q", desc)
	}
	if !strings.Contains(desc, "field_context_separator≈--field-context-separator") {
		t.Fatalf("expected description to mention field_context_separator compatibility, got %q", desc)
	}
	if !strings.Contains(desc, "path_separator≈--path-separator") {
		t.Fatalf("expected description to mention path_separator compatibility, got %q", desc)
	}
	if !strings.Contains(desc, "context_separator≈--context-separator") {
		t.Fatalf("expected description to mention context_separator compatibility, got %q", desc)
	}
	if !strings.Contains(desc, "max_columns≈-M/--max-columns") {
		t.Fatalf("expected description to mention max_columns compatibility, got %q", desc)
	}
	if !strings.Contains(desc, "max_columns_preview≈--max-columns-preview") {
		t.Fatalf("expected description to mention max_columns_preview compatibility, got %q", desc)
	}
	if !strings.Contains(desc, "count_matches≈--count-matches") {
		t.Fatalf("expected description to mention count_matches compatibility, got %q", desc)
	}
	if !strings.Contains(desc, "stats≈--stats") {
		t.Fatalf("expected description to mention stats compatibility, got %q", desc)
	}
	if !strings.Contains(desc, "json≈--json") {
		t.Fatalf("expected description to mention json compatibility, got %q", desc)
	}
	if !strings.Contains(desc, "follow≈-L/--follow") {
		t.Fatalf("expected description to mention follow compatibility, got %q", desc)
	}
	if !strings.Contains(desc, "sort/sortr≈--sort/--sortr") {
		t.Fatalf("expected description to mention sort compatibility, got %q", desc)
	}
	if !strings.Contains(desc, "sort_files≈--sort-files") {
		t.Fatalf("expected description to mention sort-files compatibility, got %q", desc)
	}
	if !strings.Contains(desc, "type_add/type_clear≈--type-add/--type-clear") {
		t.Fatalf("expected description to mention type_add/type_clear compatibility, got %q", desc)
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
	if !strings.Contains(desc, "path:line[:column]: content") {
		t.Fatalf("expected description to mention normalized output shape, got %q", desc)
	}
	if !strings.Contains(desc, "--files-without-match") {
		t.Fatalf("expected description to mention long-form files_without_match compatibility, got %q", desc)
	}
	if !strings.Contains(desc, "rg-only") {
		t.Fatalf("expected description to mention rg-only passthrough behavior, got %q", desc)
	}
}

func TestGrepTool_ParametersGuideSingleTargetFocus(t *testing.T) {
	tool := NewGrepTool()
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
	if !strings.Contains(patternDesc, "拆分") || !strings.Contains(patternDesc, "每次只聚焦一个目标") {
		t.Fatalf("expected pattern description to guide single-target focus, got %q", patternDesc)
	}

	rgArgsSchema, ok := props["rg_args"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected rg_args schema in properties, got %#v", props)
	}
	rgArgsDesc, _ := rgArgsSchema["description"].(string)
	if !strings.Contains(rgArgsDesc, "拆分") || !strings.Contains(rgArgsDesc, "每次聚焦一个目标") {
		t.Fatalf("expected rg_args description to guide single-target focus, got %q", rgArgsDesc)
	}
	if !strings.Contains(rgArgsDesc, "结构化参数优先于 rg_args") {
		t.Fatalf("expected rg_args description to mention structured priority, got %q", rgArgsDesc)
	}
}

func TestGrepTool_ParametersAreOpenAiCompatible(t *testing.T) {
	tool := NewGrepTool()
	params := tool.Parameters()

	if params["additionalProperties"] != false {
		t.Fatalf("expected top-level additionalProperties=false, got %#v", params["additionalProperties"])
	}
	if _, ok := params["anyOf"]; ok {
		t.Fatalf("expected top-level anyOf to be removed for codex compatibility, got %#v", params["anyOf"])
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
	if result.Metadata["pattern_count"] != 2 || result.Metadata["pattern_source"] != "multiple_patterns" {
		t.Fatalf("expected enhanced pattern metadata for multiple patterns, got %#v", result.Metadata)
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
		"rg_args": []interface{}{"-e", "foo", "-e", "bar", "--files-without-match", "-x", "-v", t.TempDir()},
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

func TestGrepTool_CountMatchesBuiltin(t *testing.T) {
	tmpDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmpDir, "sample.txt"), []byte("foo foo\nbar\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	tool := NewGrepTool()
	tool.lookPath = func(name string) (string, error) {
		return "", os.ErrNotExist
	}

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"pattern":       "foo",
		"path":          tmpDir,
		"count_matches": true,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected success, got error %v", result.Error)
	}
	if result.Content != "sample.txt:2" {
		t.Fatalf("expected count_matches to count two occurrences, got %q", result.Content)
	}
	if result.Metadata["count_matches"] != true || result.Metadata["mode"] != string(grepModeCount) {
		t.Fatalf("expected count_matches metadata in count mode, got %#v", result.Metadata)
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
	if result.Metadata["search_scope_count"] != 2 || result.Metadata["search_scope_kind"] != "multi_path" || result.Metadata["normalized_output"] != true {
		t.Fatalf("expected enhanced multi-path metadata, got %#v", result.Metadata)
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

func TestGrepTool_NullMaxFilesizeIgnored(t *testing.T) {
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
		"max_filesize": nil,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected success, got error %v", result.Error)
	}
	if !strings.Contains(result.Content, "small.txt") {
		t.Fatalf("expected small.txt in result, got %q", result.Content)
	}
	if !strings.Contains(result.Content, "large.txt") {
		t.Fatalf("expected large.txt in result when null max_filesize is ignored, got %q", result.Content)
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
	if result.Metadata["case_mode"] != "smart_case" || result.Metadata["pattern_source"] != "pattern_file" {
		t.Fatalf("expected enhanced smart_case/pattern_source metadata, got %#v", result.Metadata)
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

func TestGrepTool_RgArgsUnrestrictedDropsDefaultIgnoreGlobs(t *testing.T) {
	tool := NewGrepTool()
	var gotArgs []string

	tool.lookPath = func(name string) (string, error) {
		return "rg", nil
	}
	tool.runCommand = func(ctx context.Context, binaryPath, workingDir string, args []string) ([]byte, error) {
		gotArgs = append([]string(nil), args...)
		return []byte("sample.txt:1:needle\n"), nil
	}

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"rg_args": []interface{}{"-uuu", "needle", t.TempDir()},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected success, got error %v", result.Error)
	}
	argsStr := strings.Join(gotArgs, " ")
	if strings.Contains(argsStr, "--glob !.git/**") || strings.Contains(argsStr, "--glob !node_modules/**") {
		t.Fatalf("expected unrestricted search to avoid project-specific ignore globs, got %v", gotArgs)
	}
	if result.Metadata["unrestricted_level"] != 3 {
		t.Fatalf("expected unrestricted_level=3, got %#v", result.Metadata["unrestricted_level"])
	}
	if result.Metadata["no_ignore"] != true {
		t.Fatalf("expected no_ignore metadata for unrestricted search, got %#v", result.Metadata["no_ignore"])
	}
}

func TestGrepTool_RgArgsHiddenAndUnrestrictedTranslateToRipgrep(t *testing.T) {
	tmpDir := t.TempDir()
	tool := NewGrepTool()

	var gotArgs []string
	tool.lookPath = func(name string) (string, error) {
		return "rg", nil
	}
	tool.runCommand = func(ctx context.Context, binaryPath, workingDir string, args []string) ([]byte, error) {
		gotArgs = append([]string(nil), args...)
		return []byte("sample.txt:1:needle\n"), nil
	}

	check := func(name string, params map[string]interface{}, wantHidden bool, wantNoHidden bool) {
		t.Helper()
		gotArgs = nil
		result, err := tool.Execute(context.Background(), params)
		if err != nil {
			t.Fatalf("%s: unexpected error: %v", name, err)
		}
		if !result.Success {
			t.Fatalf("%s: expected success, got %v", name, result.Error)
		}
		argsStr := strings.Join(gotArgs, " ")
		if wantHidden && !strings.Contains(argsStr, "--hidden") {
			t.Fatalf("%s: expected --hidden in rg args, got %v", name, gotArgs)
		}
		if !wantHidden && strings.Contains(argsStr, "--hidden") {
			t.Fatalf("%s: expected no --hidden in rg args, got %v", name, gotArgs)
		}
		if wantNoHidden && !strings.Contains(argsStr, "--no-hidden") {
			t.Fatalf("%s: expected --no-hidden in rg args, got %v", name, gotArgs)
		}
		if !wantNoHidden && strings.Contains(argsStr, "--no-hidden") {
			t.Fatalf("%s: expected no --no-hidden in rg args, got %v", name, gotArgs)
		}
	}

	check("unrestricted_level=1", map[string]interface{}{
		"rg_args": []interface{}{"-u", "needle", tmpDir},
	}, false, false)
	check("unrestricted_level=2", map[string]interface{}{
		"rg_args": []interface{}{"-uu", "needle", tmpDir},
	}, true, false)
	check("unrestricted_level=3", map[string]interface{}{
		"rg_args": []interface{}{"-uuu", "needle", tmpDir},
	}, true, false)
	check("hidden", map[string]interface{}{
		"rg_args": []interface{}{"--hidden", "needle", tmpDir},
	}, true, false)
	check("no_hidden", map[string]interface{}{
		"rg_args": []interface{}{"--no-hidden", "needle", tmpDir},
	}, false, true)
	check("hidden_then_no_hidden", map[string]interface{}{
		"rg_args": []interface{}{"--hidden", "--no-hidden", "needle", tmpDir},
	}, false, true)
	check("no_hidden_then_hidden", map[string]interface{}{
		"rg_args": []interface{}{"--no-hidden", "--hidden", "needle", tmpDir},
	}, false, true)
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

func TestGrepTool_ColumnAndTrimBuiltin(t *testing.T) {
	tmpDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmpDir, "sample.txt"), []byte("  foo bar\n    baz foo\nqux\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	tool := NewGrepTool()
	tool.lookPath = func(name string) (string, error) {
		return "", os.ErrNotExist
	}

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"pattern": "foo",
		"path":    tmpDir,
		"column":  true,
		"trim":    true,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected success, got error %v", result.Error)
	}
	if !strings.Contains(result.Content, "sample.txt:1:3: foo bar") {
		t.Fatalf("expected trimmed column output for first line, got %q", result.Content)
	}
	if !strings.Contains(result.Content, "sample.txt:2:9: baz foo") {
		t.Fatalf("expected trimmed column output for second line, got %q", result.Content)
	}
	if result.Metadata["column"] != true || result.Metadata["trim"] != true || result.Metadata["normalized_output"] != true {
		t.Fatalf("expected column/trim/normalized_output metadata, got %#v", result.Metadata)
	}
}

func TestGrepTool_OnlyMatchingColumnBuiltin(t *testing.T) {
	tmpDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmpDir, "sample.txt"), []byte("  foo foo\nfoobar foo\n"), 0o644); err != nil {
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
		"column":        true,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected success, got error %v", result.Error)
	}
	lines := strings.Split(strings.TrimSpace(result.Content), "\n")
	want := []string{
		"sample.txt:1:3: foo",
		"sample.txt:1:7: foo",
		"sample.txt:2:1: foo",
		"sample.txt:2:8: foo",
	}
	if len(lines) != len(want) {
		t.Fatalf("expected %d only-matching column lines, got %d (%q)", len(want), len(lines), result.Content)
	}
	for i, line := range lines {
		if line != want[i] {
			t.Fatalf("unexpected only-matching column line %d: want %q, got %q", i, want[i], line)
		}
	}
}

func TestNormalizeRipgrepOutput_ColumnLines(t *testing.T) {
	lines := normalizeRipgrepOutput([]byte("pkg/main.go:3:12:foo bar\npkg/main.go-4-context line\n"))
	if len(lines) != 2 {
		t.Fatalf("expected 2 normalized lines, got %d (%v)", len(lines), lines)
	}
	if lines[0] != "pkg/main.go:3:12: foo bar" {
		t.Fatalf("unexpected normalized column match line: %q", lines[0])
	}
	if lines[1] != "pkg/main.go:4: context line" {
		t.Fatalf("unexpected normalized context line: %q", lines[1])
	}
}

func TestGrepTool_RgArgsColumnTrimAndSortTranslateToRipgrep(t *testing.T) {
	tool := NewGrepTool()
	var gotArgs []string

	tool.lookPath = func(name string) (string, error) {
		return "rg", nil
	}
	tool.runCommand = func(ctx context.Context, binaryPath, workingDir string, args []string) ([]byte, error) {
		gotArgs = append([]string(nil), args...)
		return []byte("sample.txt:1:3:foo\n"), nil
	}

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"rg_args": []interface{}{"--column", "--trim", "--sort", "path", "foo", t.TempDir()},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected success, got error %v", result.Error)
	}
	argsStr := strings.Join(gotArgs, " ")
	for _, want := range []string{"--column", "--trim", "--sort path"} {
		if !strings.Contains(argsStr, want) {
			t.Fatalf("expected %q in rg args, got %v", want, gotArgs)
		}
	}
	if result.Content != "sample.txt:1:3: foo" {
		t.Fatalf("unexpected normalized ripgrep column content: %q", result.Content)
	}
	if result.Metadata["column"] != true || result.Metadata["trim"] != true || result.Metadata["sort"] != "path" {
		t.Fatalf("expected column/trim/sort metadata, got %#v", result.Metadata)
	}
}

func TestGrepTool_RgArgsCountMatchesTranslateToRipgrep(t *testing.T) {
	tool := NewGrepTool()
	var gotArgs []string

	tool.lookPath = func(name string) (string, error) {
		return "rg", nil
	}
	tool.runCommand = func(ctx context.Context, binaryPath, workingDir string, args []string) ([]byte, error) {
		gotArgs = append([]string(nil), args...)
		return []byte("sample.txt:2\n"), nil
	}

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"rg_args": []interface{}{"--count-matches", "foo", t.TempDir()},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected success, got error %v", result.Error)
	}
	if !strings.Contains(strings.Join(gotArgs, " "), "--count-matches") {
		t.Fatalf("expected --count-matches in rg args, got %v", gotArgs)
	}
	if result.Metadata["count_matches"] != true || result.Metadata["mode"] != string(grepModeCount) {
		t.Fatalf("expected count_matches metadata, got %#v", result.Metadata)
	}
}

func TestGrepTool_StatsBuiltin(t *testing.T) {
	tmpDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmpDir, "sample.txt"), []byte("foo foo\nbar\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	tool := NewGrepTool()
	tool.lookPath = func(name string) (string, error) {
		return "", os.ErrNotExist
	}

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"pattern": "foo",
		"path":    tmpDir,
		"stats":   true,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected success, got error %v", result.Error)
	}
	if !strings.Contains(result.Content, "-- stats --") || !strings.Contains(result.Content, "matches: 2") {
		t.Fatalf("expected normalized stats summary in output, got %q", result.Content)
	}
	stats, ok := result.Metadata["stats"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected structured stats metadata, got %#v", result.Metadata["stats"])
	}
	if stats["matches"] != 2 || stats["matched_lines"] != 1 || stats["files_searched"] != 1 {
		t.Fatalf("unexpected stats metadata: %#v", stats)
	}
	if result.Metadata["stats_requested"] != true {
		t.Fatalf("expected stats_requested metadata, got %#v", result.Metadata["stats_requested"])
	}
}

func TestGrepTool_RgStatsNoMatchesStillReturnsSummary(t *testing.T) {
	tool := NewGrepTool()
	tool.lookPath = func(name string) (string, error) {
		return "rg", nil
	}
	tool.runCommand = func(ctx context.Context, binaryPath, workingDir string, args []string) ([]byte, error) {
		var cmd *exec.Cmd
		if runtime.GOOS == "windows" {
			cmd = exec.CommandContext(ctx, "cmd", "/c", "(echo. & echo 0 matches & echo 0 matched lines & echo 0 files contained matches & echo 1 files searched & echo 0 bytes printed & echo 4 bytes searched & echo 0.000100 seconds spent searching & echo 0.001000 seconds total & exit /b 1)")
		} else {
			cmd = exec.CommandContext(ctx, "sh", "-c", "printf '\\n0 matches\\n0 matched lines\\n0 files contained matches\\n1 files searched\\n0 bytes printed\\n4 bytes searched\\n0.000100 seconds spent searching\\n0.001000 seconds total\\n'; exit 1")
		}
		return cmd.CombinedOutput()
	}

	tmpDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmpDir, "sample.txt"), []byte("foo\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"pattern": "nope",
		"path":    tmpDir,
		"stats":   true,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected success with stats summary even on no matches, got error %v", result.Error)
	}
	if !strings.Contains(result.Content, "未找到匹配的内容") || !strings.Contains(result.Content, "matches: 0") {
		t.Fatalf("expected no-match output plus stats summary, got %q", result.Content)
	}
	if !strings.Contains(result.Content, "bytes printed:") {
		t.Fatalf("expected rg-style bytes printed line in stats summary, got %q", result.Content)
	}
}

func TestGrepTool_JSONRequiresRipgrepWhenUnavailable(t *testing.T) {
	tmpDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmpDir, "sample.txt"), []byte("foo\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	tool := NewGrepTool()
	tool.lookPath = func(name string) (string, error) {
		return "", os.ErrNotExist
	}

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"pattern": "foo",
		"path":    tmpDir,
		"json":    true,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Success {
		t.Fatalf("expected json output to fail cleanly without rg")
	}
	if !strings.Contains(result.Error.Error(), "仅 ripgrep/rg 支持") {
		t.Fatalf("expected clear rg-only json error, got %v", result.Error)
	}
	if !strings.Contains(result.Error.Error(), "json/--json") {
		t.Fatalf("expected json requirement to appear in error, got %v", result.Error)
	}
}

func TestGrepTool_JSONPassthroughPreservesRawSummaryAndMetadata(t *testing.T) {
	tool := NewGrepTool()
	var gotArgs []string

	tool.lookPath = func(name string) (string, error) {
		return "rg", nil
	}
	tool.runCommand = func(ctx context.Context, binaryPath, workingDir string, args []string) ([]byte, error) {
		gotArgs = append([]string(nil), args...)
		return []byte("{\"type\":\"summary\",\"data\":{\"elapsed_total\":{\"human\":\"0.001000s\",\"nanos\":1000000,\"secs\":0},\"stats\":{\"bytes_printed\":0,\"bytes_searched\":14,\"elapsed\":{\"human\":\"0.000100s\",\"nanos\":100000,\"secs\":0},\"matched_lines\":1,\"matches\":2,\"searches\":1,\"searches_with_match\":1}}}\n"), nil
	}

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"pattern": "foo",
		"path":    t.TempDir(),
		"json":    true,
		"stats":   true,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected success, got error %v", result.Error)
	}
	if !strings.Contains(strings.Join(gotArgs, " "), "--json") || !strings.Contains(strings.Join(gotArgs, " "), "--stats") {
		t.Fatalf("expected --json/--stats in ripgrep args, got %v", gotArgs)
	}
	if strings.Contains(result.Content, "-- stats --") {
		t.Fatalf("expected json mode to avoid custom stats footer, got %q", result.Content)
	}
	if result.Content == "" || !strings.Contains(result.Content, "\"type\":\"summary\"") {
		t.Fatalf("expected raw rg json summary content, got %q", result.Content)
	}
	if result.Metadata["json_output_requested"] != true || result.Metadata["normalized_output"] != false {
		t.Fatalf("expected json output metadata to be marked correctly, got %#v", result.Metadata)
	}
	if result.Metadata["output_format"] != "rg_passthrough" {
		t.Fatalf("expected rg_passthrough output_format, got %#v", result.Metadata["output_format"])
	}
	stats, ok := result.Metadata["stats"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected structured stats metadata from json summary, got %#v", result.Metadata["stats"])
	}
	if stats["matches"] != 2 || stats["matched_lines"] != 1 || stats["files_searched"] != 1 || stats["files_with_matches"] != 1 {
		t.Fatalf("unexpected json stats metadata: %#v", stats)
	}
	switch got := stats["bytes_printed"].(type) {
	case int:
		if got != 0 {
			t.Fatalf("expected bytes_printed in json stats metadata, got %#v", stats["bytes_printed"])
		}
	case int64:
		if got != 0 {
			t.Fatalf("expected bytes_printed in json stats metadata, got %#v", stats["bytes_printed"])
		}
	case float64:
		if got != 0 {
			t.Fatalf("expected bytes_printed in json stats metadata, got %#v", stats["bytes_printed"])
		}
	default:
		t.Fatalf("unexpected bytes_printed type in json stats metadata: %#v", stats["bytes_printed"])
	}
}

func TestGrepTool_JSONMultiPathRewritesRelativeJSONPaths(t *testing.T) {
	rootDir := t.TempDir()
	leftDir := filepath.Join(rootDir, "left")
	rightDir := filepath.Join(rootDir, "right")
	if err := os.MkdirAll(leftDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(rightDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(leftDir, "a.txt"), []byte("foo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(rightDir, "b.txt"), []byte("foo\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	tool := NewGrepTool()
	tool.lookPath = func(name string) (string, error) {
		return "rg", nil
	}
	tool.runCommand = func(ctx context.Context, binaryPath, workingDir string, args []string) ([]byte, error) {
		switch workingDir {
		case leftDir:
			return []byte("{\"type\":\"match\",\"data\":{\"path\":{\"text\":\"a.txt\"},\"lines\":{\"text\":\"foo\\n\"},\"line_number\":1,\"absolute_offset\":0,\"submatches\":[{\"match\":{\"text\":\"foo\"},\"start\":0,\"end\":3}]}}\n{\"type\":\"summary\",\"data\":{\"elapsed_total\":{\"human\":\"0.001000s\",\"nanos\":1000000,\"secs\":0},\"stats\":{\"bytes_printed\":120,\"bytes_searched\":4,\"elapsed\":{\"human\":\"0.000100s\",\"nanos\":100000,\"secs\":0},\"matched_lines\":1,\"matches\":1,\"searches\":1,\"searches_with_match\":1}}}\n"), nil
		case rightDir:
			return []byte("{\"type\":\"match\",\"data\":{\"path\":{\"text\":\"b.txt\"},\"lines\":{\"text\":\"foo\\n\"},\"line_number\":1,\"absolute_offset\":0,\"submatches\":[{\"match\":{\"text\":\"foo\"},\"start\":0,\"end\":3}]}}\n{\"type\":\"summary\",\"data\":{\"elapsed_total\":{\"human\":\"0.001000s\",\"nanos\":1000000,\"secs\":0},\"stats\":{\"bytes_printed\":120,\"bytes_searched\":4,\"elapsed\":{\"human\":\"0.000100s\",\"nanos\":100000,\"secs\":0},\"matched_lines\":1,\"matches\":1,\"searches\":1,\"searches_with_match\":1}}}\n"), nil
		default:
			t.Fatalf("unexpected working dir %q", workingDir)
			return nil, nil
		}
	}

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"pattern": "foo",
		"paths":   []interface{}{leftDir, rightDir},
		"json":    true,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected success, got error %v", result.Error)
	}

	lines := strings.Split(strings.TrimSpace(result.Content), "\n")
	if len(lines) < 3 {
		t.Fatalf("expected multiple json output lines, got %q", result.Content)
	}

	var first map[string]interface{}
	if err := json.Unmarshal([]byte(lines[0]), &first); err != nil {
		t.Fatalf("expected first line to remain valid json, got %q: %v", lines[0], err)
	}
	data, _ := first["data"].(map[string]interface{})
	pathMap, _ := data["path"].(map[string]interface{})
	if got := pathMap["text"]; got != filepath.ToSlash(filepath.Join(leftDir, "a.txt")) {
		t.Fatalf("expected first scoped json path to be rewritten, got %#v", got)
	}

	var third map[string]interface{}
	if err := json.Unmarshal([]byte(lines[2]), &third); err != nil {
		t.Fatalf("expected third line to remain valid json, got %q: %v", lines[2], err)
	}
	thirdData, _ := third["data"].(map[string]interface{})
	thirdPathMap, _ := thirdData["path"].(map[string]interface{})
	if got := thirdPathMap["text"]; got != filepath.ToSlash(filepath.Join(rightDir, "b.txt")) {
		t.Fatalf("expected second scoped json path to be rewritten, got %#v", got)
	}

	stats, ok := result.Metadata["stats"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected aggregated stats metadata, got %#v", result.Metadata["stats"])
	}
	if stats["matches"] != 2 || stats["matched_lines"] != 2 || stats["files_searched"] != 2 || stats["files_with_matches"] != 2 {
		t.Fatalf("unexpected aggregated json stats metadata: %#v", stats)
	}
}

func TestGrepTool_JSONEventFlowRewritesBeginContextEndPaths(t *testing.T) {
	rootDir := t.TempDir()
	leftDir := filepath.Join(rootDir, "left")
	rightDir := filepath.Join(rootDir, "right")
	if err := os.MkdirAll(leftDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(rightDir, 0o755); err != nil {
		t.Fatal(err)
	}

	tool := NewGrepTool()
	tool.lookPath = func(name string) (string, error) {
		return "rg", nil
	}
	tool.runCommand = func(ctx context.Context, binaryPath, workingDir string, args []string) ([]byte, error) {
		switch workingDir {
		case leftDir:
			return []byte("{\"type\":\"begin\",\"data\":{\"path\":{\"text\":\"a.txt\"}}}\n{\"type\":\"context\",\"data\":{\"path\":{\"text\":\"a.txt\"},\"lines\":{\"text\":\"ctx\\n\"},\"line_number\":1}}\n{\"type\":\"end\",\"data\":{\"path\":{\"text\":\"a.txt\"}}}\n{\"type\":\"summary\",\"data\":{\"elapsed_total\":{\"human\":\"0.001000s\",\"nanos\":1000000,\"secs\":0},\"stats\":{\"bytes_printed\":0,\"bytes_searched\":4,\"elapsed\":{\"human\":\"0.000100s\",\"nanos\":100000,\"secs\":0},\"matched_lines\":1,\"matches\":1,\"searches\":1,\"searches_with_match\":1}}}\n"), nil
		case rightDir:
			return []byte("{\"type\":\"begin\",\"data\":{\"path\":{\"text\":\"b.txt\"}}}\n{\"type\":\"match\",\"data\":{\"path\":{\"text\":\"b.txt\"},\"lines\":{\"text\":\"foo\\n\"},\"line_number\":1,\"absolute_offset\":0,\"submatches\":[{\"match\":{\"text\":\"foo\"},\"start\":0,\"end\":3}]}}\n{\"type\":\"end\",\"data\":{\"path\":{\"text\":\"b.txt\"}}}\n{\"type\":\"summary\",\"data\":{\"elapsed_total\":{\"human\":\"0.001000s\",\"nanos\":1000000,\"secs\":0},\"stats\":{\"bytes_printed\":120,\"bytes_searched\":4,\"elapsed\":{\"human\":\"0.000100s\",\"nanos\":100000,\"secs\":0},\"matched_lines\":1,\"matches\":1,\"searches\":1,\"searches_with_match\":1}}}\n"), nil
		default:
			t.Fatalf("unexpected working dir %q", workingDir)
			return nil, nil
		}
	}

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"pattern": "foo",
		"paths":   []interface{}{leftDir, rightDir},
		"json":    true,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected success, got error %v", result.Error)
	}

	lines := strings.Split(strings.TrimSpace(result.Content), "\n")
	if len(lines) < 8 {
		t.Fatalf("expected full json event stream, got %q", result.Content)
	}

	var first map[string]interface{}
	if err := json.Unmarshal([]byte(lines[0]), &first); err != nil {
		t.Fatalf("expected first line to remain valid json, got %q: %v", lines[0], err)
	}
	firstData, _ := first["data"].(map[string]interface{})
	firstPath, _ := firstData["path"].(map[string]interface{})
	if got := firstPath["text"]; got != filepath.ToSlash(filepath.Join(leftDir, "a.txt")) {
		t.Fatalf("expected begin path to be rewritten, got %#v", got)
	}

	var second map[string]interface{}
	if err := json.Unmarshal([]byte(lines[1]), &second); err != nil {
		t.Fatalf("expected second line to remain valid json, got %q: %v", lines[1], err)
	}
	secondData, _ := second["data"].(map[string]interface{})
	secondPath, _ := secondData["path"].(map[string]interface{})
	if got := secondPath["text"]; got != filepath.ToSlash(filepath.Join(leftDir, "a.txt")) {
		t.Fatalf("expected context path to be rewritten, got %#v", got)
	}

	var fifth map[string]interface{}
	if err := json.Unmarshal([]byte(lines[4]), &fifth); err != nil {
		t.Fatalf("expected fifth line to remain valid json, got %q: %v", lines[4], err)
	}
	fifthData, _ := fifth["data"].(map[string]interface{})
	fifthPath, _ := fifthData["path"].(map[string]interface{})
	if got := fifthPath["text"]; got != filepath.ToSlash(filepath.Join(rightDir, "b.txt")) {
		t.Fatalf("expected right-side begin path to be rewritten, got %#v", got)
	}

	var sixth map[string]interface{}
	if err := json.Unmarshal([]byte(lines[5]), &sixth); err != nil {
		t.Fatalf("expected sixth line to remain valid json, got %q: %v", lines[5], err)
	}
	sixthData, _ := sixth["data"].(map[string]interface{})
	sixthPath, _ := sixthData["path"].(map[string]interface{})
	if got := sixthPath["text"]; got != filepath.ToSlash(filepath.Join(rightDir, "b.txt")) {
		t.Fatalf("expected match path to be rewritten, got %#v", got)
	}

	stats, ok := result.Metadata["stats"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected aggregated stats metadata, got %#v", result.Metadata["stats"])
	}
	if stats["matches"] != 2 || stats["matched_lines"] != 2 || stats["files_searched"] != 2 {
		t.Fatalf("unexpected aggregated json stats metadata: %#v", stats)
	}
}

func TestGrepTool_StructuredRgOnlyFlagMatrixTranslateToRipgrep(t *testing.T) {
	tool := NewGrepTool()
	var gotArgs []string

	tool.lookPath = func(name string) (string, error) {
		return "rg", nil
	}
	tool.runCommand = func(ctx context.Context, binaryPath, workingDir string, args []string) ([]byte, error) {
		gotArgs = append([]string(nil), args...)
		return []byte("sample.txt:1:foo\n"), nil
	}

	tmpDir := t.TempDir()
	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"pattern":           "foo",
		"path":              tmpDir,
		"pcre2":             true,
		"engine":            "pcre2",
		"multiline":         true,
		"multiline_dotall":  true,
		"replace":           "bar",
		"passthru":          true,
		"crlf":              true,
		"auto_hybrid_regex": true,
		"type_add":          []interface{}{"foo:*.foo"},
		"type_clear":        "foo",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected success, got error %v", result.Error)
	}
	argsStr := strings.Join(gotArgs, " ")
	for _, want := range []string{"--pcre2", "--engine pcre2", "--multiline", "--multiline-dotall", "--replace bar", "--passthru", "--crlf", "--auto-hybrid-regex"} {
		if !strings.Contains(argsStr, want) {
			t.Fatalf("expected %q in rg args, got %v", want, gotArgs)
		}
	}
	for _, want := range []string{"--type-add foo:*.foo", "--type-clear foo"} {
		if !strings.Contains(argsStr, want) {
			t.Fatalf("expected %q in rg args, got %v", want, gotArgs)
		}
	}
	if result.Metadata["requires_ripgrep"] != true {
		t.Fatalf("expected structured rg-only flags to require rg, got %#v", result.Metadata["requires_ripgrep"])
	}
	if !strings.Contains(strings.Join(result.Metadata["rg_only_args"].([]string), " "), "--pcre2") {
		t.Fatalf("expected rg_only_args metadata to record rg-only passthrough args, got %#v", result.Metadata["rg_only_args"])
	}
	if got, ok := result.Metadata["type_add"].([]string); !ok || len(got) != 1 || got[0] != "foo:*.foo" {
		t.Fatalf("expected type_add metadata to capture structured value, got %#v", result.Metadata["type_add"])
	}
	if got, ok := result.Metadata["type_clear"].([]string); !ok || len(got) != 1 || got[0] != "foo" {
		t.Fatalf("expected type_clear metadata to capture structured value, got %#v", result.Metadata["type_clear"])
	}
}

func TestGrepTool_RgArgsTypeAddAndClearTranslateToRipgrep(t *testing.T) {
	tmpDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmpDir, "sample.foo"), []byte("needle\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	tool := NewGrepTool()
	var gotArgs []string

	tool.lookPath = func(name string) (string, error) {
		return "rg", nil
	}
	tool.runCommand = func(ctx context.Context, binaryPath, workingDir string, args []string) ([]byte, error) {
		gotArgs = append([]string(nil), args...)
		return []byte("sample.foo:1:needle\n"), nil
	}

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"rg_args": []interface{}{"--type-add", "foo:*.foo", "--type-clear", "foo", "needle", tmpDir},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected success, got error %v", result.Error)
	}
	argsStr := strings.Join(gotArgs, " ")
	for _, want := range []string{"--type-add foo:*.foo", "--type-clear foo"} {
		if !strings.Contains(argsStr, want) {
			t.Fatalf("expected %q in rg args, got %v", want, gotArgs)
		}
	}
	if got, ok := result.Metadata["type_add"].([]string); !ok || len(got) != 1 || got[0] != "foo:*.foo" {
		t.Fatalf("expected type_add metadata from rg_args, got %#v", result.Metadata["type_add"])
	}
	if got, ok := result.Metadata["type_clear"].([]string); !ok || len(got) != 1 || got[0] != "foo" {
		t.Fatalf("expected type_clear metadata from rg_args, got %#v", result.Metadata["type_clear"])
	}
}

func TestGrepTool_IgnoreFileAndNoIgnoreFlagsTranslateToRipgrep(t *testing.T) {
	tmpDir := t.TempDir()
	ignoreOne := filepath.Join(tmpDir, ".gitignore")
	ignoreTwo := filepath.Join(tmpDir, ".rgignore")
	if err := os.WriteFile(ignoreOne, []byte("ignored.txt\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(ignoreTwo, []byte("*.tmp\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	tool := NewGrepTool()
	var gotArgs []string

	tool.lookPath = func(name string) (string, error) {
		return "rg", nil
	}
	tool.runCommand = func(ctx context.Context, binaryPath, workingDir string, args []string) ([]byte, error) {
		gotArgs = append([]string(nil), args...)
		return []byte("sample.txt:1:needle\n"), nil
	}

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"pattern":                      "needle",
		"path":                         tmpDir,
		"ignore_file":                  []interface{}{ignoreOne, ignoreTwo},
		"ignore_file_case_insensitive": true,
		"no_ignore_files":              true,
		"no_ignore_parent":             true,
		"no_ignore_vcs":                true,
		"no_ignore_global":             true,
		"no_ignore_dot":                true,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected success, got error %v", result.Error)
	}
	argsStr := strings.Join(gotArgs, " ")
	for _, want := range []string{
		"--no-ignore-files",
		"--no-ignore-parent",
		"--no-ignore-vcs",
		"--no-ignore-global",
		"--no-ignore-dot",
	} {
		if !strings.Contains(argsStr, want) {
			t.Fatalf("expected %q in rg args, got %v", want, gotArgs)
		}
	}
	if strings.Contains(argsStr, "--ignore-file "+ignoreOne) || strings.Contains(argsStr, "--ignore-file "+ignoreTwo) {
		t.Fatalf("expected explicit ignore_file values to be suppressed by no_ignore_files, got %v", gotArgs)
	}
	if got, ok := result.Metadata["ignore_files"].([]string); !ok || len(got) != 2 {
		t.Fatalf("expected ignore_files metadata, got %#v", result.Metadata["ignore_files"])
	}
	if result.Metadata["ignore_file_case_insensitive"] != false {
		t.Fatalf("expected ignore_file_case_insensitive to be suppressed by no_ignore_files, got %#v", result.Metadata["ignore_file_case_insensitive"])
	}
	if result.Metadata["no_ignore_files"] != true || result.Metadata["no_ignore_parent"] != true || result.Metadata["no_ignore_vcs"] != true || result.Metadata["no_ignore_global"] != true || result.Metadata["no_ignore_dot"] != true {
		t.Fatalf("expected no_ignore_* metadata, got %#v", result.Metadata)
	}
}

func TestGrepTool_IgnoreFileCaseInsensitiveStructuredParamsOverrideRgArgs(t *testing.T) {
	tmpDir := t.TempDir()
	tool := NewGrepTool()

	var gotArgs []string
	tool.lookPath = func(name string) (string, error) {
		return "rg", nil
	}
	tool.runCommand = func(ctx context.Context, binaryPath, workingDir string, args []string) ([]byte, error) {
		gotArgs = append([]string(nil), args...)
		return []byte("sample.txt:1:needle\n"), nil
	}

	cases := []struct {
		name     string
		params   map[string]interface{}
		wantFlag bool
	}{
		{
			name: "structured false suppresses rg_args true",
			params: map[string]interface{}{
				"rg_args":                      []interface{}{"--ignore-file-case-insensitive", "needle", tmpDir},
				"ignore_file_case_insensitive": false,
			},
			wantFlag: false,
		},
		{
			name: "structured true adds flag over rg_args false",
			params: map[string]interface{}{
				"rg_args":                      []interface{}{"needle", tmpDir},
				"ignore_file_case_insensitive": true,
			},
			wantFlag: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotArgs = nil
			result, err := tool.Execute(context.Background(), tc.params)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !result.Success {
				t.Fatalf("expected success, got error %v", result.Error)
			}
			argsStr := strings.Join(gotArgs, " ")
			if tc.wantFlag && !strings.Contains(argsStr, "--ignore-file-case-insensitive") {
				t.Fatalf("expected --ignore-file-case-insensitive in rg args, got %v", gotArgs)
			}
			if !tc.wantFlag && strings.Contains(argsStr, "--ignore-file-case-insensitive") {
				t.Fatalf("expected structured param to suppress --ignore-file-case-insensitive, got %v", gotArgs)
			}
		})
	}
}

func TestGrepTool_StructuredIgnoreHiddenAndUnrestrictedParamsOverrideRgArgs(t *testing.T) {
	tmpDir := t.TempDir()
	tool := NewGrepTool()

	var gotArgs []string
	tool.lookPath = func(name string) (string, error) {
		return "rg", nil
	}
	tool.runCommand = func(ctx context.Context, binaryPath, workingDir string, args []string) ([]byte, error) {
		gotArgs = append([]string(nil), args...)
		return []byte("sample.txt:1:needle\n"), nil
	}

	cases := []struct {
		name           string
		params         map[string]interface{}
		wantContains   []string
		wantNotContain []string
	}{
		{
			name: "hidden true suppresses rg_args no-hidden",
			params: map[string]interface{}{
				"rg_args": []interface{}{"--no-hidden", "needle", tmpDir},
				"hidden":  true,
			},
			wantContains:   []string{"--hidden"},
			wantNotContain: []string{"--no-hidden"},
		},
		{
			name: "hidden false suppresses rg_args hidden",
			params: map[string]interface{}{
				"rg_args": []interface{}{"--hidden", "needle", tmpDir},
				"hidden":  false,
			},
			wantNotContain: []string{"--hidden"},
		},
		{
			name: "no_hidden true suppresses rg_args hidden",
			params: map[string]interface{}{
				"rg_args":   []interface{}{"--hidden", "needle", tmpDir},
				"no_hidden": true,
			},
			wantContains:   []string{"--no-hidden"},
			wantNotContain: []string{"--hidden"},
		},
		{
			name: "no_ignore false suppresses rg_args no-ignore",
			params: map[string]interface{}{
				"rg_args":   []interface{}{"-u", "needle", tmpDir},
				"no_ignore": false,
			},
			wantNotContain: []string{"--no-ignore"},
		},
		{
			name: "unrestricted_level zero suppresses rg_args unrestricted flags",
			params: map[string]interface{}{
				"rg_args":            []interface{}{"-uu", "needle", tmpDir},
				"unrestricted_level": 0,
			},
			wantNotContain: []string{"--no-ignore", "--hidden", "--no-hidden"},
		},
		{
			name: "ignore scope false suppresses rg_args no-ignore-parent family",
			params: map[string]interface{}{
				"rg_args":          []interface{}{"--no-ignore-parent", "--no-ignore-vcs", "--no-ignore-global", "--no-ignore-dot", "needle", tmpDir},
				"no_ignore_parent": false,
				"no_ignore_vcs":    false,
				"no_ignore_global": false,
				"no_ignore_dot":    false,
			},
			wantNotContain: []string{"--no-ignore-parent", "--no-ignore-vcs", "--no-ignore-global", "--no-ignore-dot"},
		},
		{
			name: "no_ignore_files false suppresses rg_args no-ignore-files",
			params: map[string]interface{}{
				"rg_args":         []interface{}{"--no-ignore-files", "needle", tmpDir},
				"no_ignore_files": false,
				"ignore_file":     "custom.ignore",
			},
			wantNotContain: []string{"--no-ignore-files"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotArgs = nil
			result, err := tool.Execute(context.Background(), tc.params)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !result.Success {
				t.Fatalf("expected success, got error %v", result.Error)
			}
			argsStr := strings.Join(gotArgs, " ")
			for _, want := range tc.wantContains {
				if !strings.Contains(argsStr, want) {
					t.Fatalf("expected %q in rg args, got %v", want, gotArgs)
				}
			}
			for _, notWant := range tc.wantNotContain {
				if strings.Contains(argsStr, notWant) {
					t.Fatalf("expected %q to be filtered from rg args, got %v", notWant, gotArgs)
				}
			}
		})
	}
}

func TestGrepTool_StructuredNoIgnoreScopeFlagsTranslateToRipgrep(t *testing.T) {
	tmpDir := t.TempDir()
	tool := NewGrepTool()

	var gotArgs []string
	tool.lookPath = func(name string) (string, error) {
		return "rg", nil
	}
	tool.runCommand = func(ctx context.Context, binaryPath, workingDir string, args []string) ([]byte, error) {
		gotArgs = append([]string(nil), args...)
		return []byte("sample.txt:1:needle\n"), nil
	}

	cases := []struct {
		name      string
		params    map[string]interface{}
		wantFlag  string
		wantCount int
		metaKey   string
	}{
		{
			name: "no_ignore_parent true emits rg flag once",
			params: map[string]interface{}{
				"rg_args":          []interface{}{"--no-ignore-parent"},
				"no_ignore_parent": true,
				"pattern":          "needle",
				"path":             tmpDir,
			},
			wantFlag:  "--no-ignore-parent",
			wantCount: 1,
			metaKey:   "no_ignore_parent",
		},
		{
			name: "no_ignore_vcs true emits rg flag once",
			params: map[string]interface{}{
				"rg_args":       []interface{}{"--no-ignore-vcs"},
				"no_ignore_vcs": true,
				"pattern":       "needle",
				"path":          tmpDir,
			},
			wantFlag:  "--no-ignore-vcs",
			wantCount: 1,
			metaKey:   "no_ignore_vcs",
		},
		{
			name: "no_ignore_global true emits rg flag once",
			params: map[string]interface{}{
				"rg_args":          []interface{}{"--no-ignore-global"},
				"no_ignore_global": true,
				"pattern":          "needle",
				"path":             tmpDir,
			},
			wantFlag:  "--no-ignore-global",
			wantCount: 1,
			metaKey:   "no_ignore_global",
		},
		{
			name: "no_ignore_dot true emits rg flag once",
			params: map[string]interface{}{
				"rg_args":       []interface{}{"--no-ignore-dot"},
				"no_ignore_dot": true,
				"pattern":       "needle",
				"path":          tmpDir,
			},
			wantFlag:  "--no-ignore-dot",
			wantCount: 1,
			metaKey:   "no_ignore_dot",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotArgs = nil
			result, err := tool.Execute(context.Background(), tc.params)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !result.Success {
				t.Fatalf("expected success, got error %v", result.Error)
			}
			argsStr := strings.Join(gotArgs, " ")
			if strings.Count(argsStr, tc.wantFlag) != tc.wantCount {
				t.Fatalf("expected %s to appear %d time(s), got %v", tc.wantFlag, tc.wantCount, gotArgs)
			}
			if result.Metadata[tc.metaKey] != true {
				t.Fatalf("expected %s metadata to be true, got %#v", tc.metaKey, result.Metadata[tc.metaKey])
			}
		})
	}
}

func TestGrepTool_StructuredDisplayAndExecutionFlagsOverrideRgArgs(t *testing.T) {
	tmpDir := t.TempDir()
	tool := NewGrepTool()

	var gotArgs []string
	tool.lookPath = func(name string) (string, error) {
		return "rg", nil
	}
	tool.runCommand = func(ctx context.Context, binaryPath, workingDir string, args []string) ([]byte, error) {
		gotArgs = append([]string(nil), args...)
		return []byte("sample.txt:1:needle\n"), nil
	}

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"pattern":                "needle",
		"path":                   tmpDir,
		"rg_args":                []interface{}{"--stats", "--json", "--follow", "--no-config", "--one-file-system", "--no-messages", "--max-columns-preview", "--no-max-columns-preview", "--column", "--trim", "--pretty", "--line-buffered", "--no-line-buffered", "--block-buffered", "--no-block-buffered", "--null", "--null-data", "--glob-case-insensitive", "-F", "-i", "-s", "-S", "-w", "-x", "-v", "-o", "--count-matches", "--max-columns", "20", "--max-depth", "1"},
		"stats":                  false,
		"json":                   false,
		"follow":                 false,
		"no_config":              false,
		"one_file_system":        false,
		"no_messages":            false,
		"max_columns_preview":    false,
		"no_max_columns_preview": false,
		"column":                 false,
		"trim":                   false,
		"pretty":                 false,
		"line_buffered":          false,
		"no_line_buffered":       false,
		"block_buffered":         false,
		"no_block_buffered":      false,
		"null":                   false,
		"null_data":              false,
		"glob_case_insensitive":  false,
		"literal":                false,
		"ignore_case":            false,
		"case_sensitive":         false,
		"smart_case":             false,
		"word_regexp":            false,
		"line_regexp":            false,
		"invert_match":           false,
		"only_matching":          false,
		"count_matches":          false,
		"max_columns":            0,
		"max_depth":              0,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected success, got error %v", result.Error)
	}
	hasFlag := func(flag string) bool {
		for _, arg := range gotArgs {
			if arg == flag || strings.HasPrefix(arg, flag+"=") {
				return true
			}
		}
		return false
	}
	for _, notWant := range []string{
		"--stats", "--json", "--follow", "--no-config", "--one-file-system", "--no-messages",
		"--max-columns-preview", "--no-max-columns-preview", "--column", "--trim", "--pretty",
		"--line-buffered", "--no-line-buffered", "--block-buffered", "--no-block-buffered",
		"--null", "--null-data", "--glob-case-insensitive", "-F", "-i", "-s", "-S", "-w", "-x", "-v", "-o", "--count-matches",
	} {
		if hasFlag(notWant) {
			t.Fatalf("expected %q to be filtered from rg args, got %v", notWant, gotArgs)
		}
	}
}

func TestGrepTool_StructuredBufferedAndNullFlagsRecordAndSuppressRgArgs(t *testing.T) {
	tmpDir := t.TempDir()
	tool := NewGrepTool()

	var gotArgs []string
	tool.lookPath = func(name string) (string, error) {
		return "rg", nil
	}
	tool.runCommand = func(ctx context.Context, binaryPath, workingDir string, args []string) ([]byte, error) {
		gotArgs = append([]string(nil), args...)
		return []byte("sample.txt:1:needle\n"), nil
	}

	cases := []struct {
		name           string
		params         map[string]interface{}
		wantNotContain []string
		metaKey        string
	}{
		{
			name: "line_buffered true suppresses rg_args no-line-buffered",
			params: map[string]interface{}{
				"rg_args":       []interface{}{"--no-line-buffered"},
				"line_buffered": true,
				"pattern":       "needle",
				"path":          tmpDir,
			},
			wantNotContain: []string{"--no-line-buffered"},
			metaKey:        "line_buffered",
		},
		{
			name: "block_buffered true suppresses rg_args no-block-buffered",
			params: map[string]interface{}{
				"rg_args":        []interface{}{"--no-block-buffered"},
				"block_buffered": true,
				"pattern":        "needle",
				"path":           tmpDir,
			},
			wantNotContain: []string{"--no-block-buffered"},
			metaKey:        "block_buffered",
		},
		{
			name: "null true emits rg flag once",
			params: map[string]interface{}{
				"rg_args": []interface{}{"--null"},
				"null":    true,
				"pattern": "needle",
				"path":    tmpDir,
			},
			metaKey: "null",
		},
		{
			name: "null_data true emits rg flag once",
			params: map[string]interface{}{
				"rg_args":   []interface{}{"--null-data"},
				"null_data": true,
				"pattern":   "needle",
				"path":      tmpDir,
			},
			metaKey: "null_data",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotArgs = nil
			result, err := tool.Execute(context.Background(), tc.params)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !result.Success {
				t.Fatalf("expected success, got error %v", result.Error)
			}
			argsStr := strings.Join(gotArgs, " ")
			for _, notWant := range tc.wantNotContain {
				if strings.Contains(argsStr, notWant) {
					t.Fatalf("expected %q to be filtered, got %v", notWant, gotArgs)
				}
			}
			if result.Metadata[tc.metaKey] != true {
				t.Fatalf("expected %s metadata to be true, got %#v", tc.metaKey, result.Metadata[tc.metaKey])
			}
		})
	}
}

func TestGrepTool_StructuredMaxColumnsPreviewFlagsOverrideRgArgs(t *testing.T) {
	tmpDir := t.TempDir()
	tool := NewGrepTool()

	var gotArgs []string
	tool.lookPath = func(name string) (string, error) {
		return "rg", nil
	}
	tool.runCommand = func(ctx context.Context, binaryPath, workingDir string, args []string) ([]byte, error) {
		gotArgs = append([]string(nil), args...)
		return []byte("sample.txt:1:needle\n"), nil
	}

	cases := []struct {
		name           string
		params         map[string]interface{}
		wantContains   []string
		wantNotContain []string
	}{
		{
			name: "max_columns_preview true suppresses rg_args no-max-columns-preview",
			params: map[string]interface{}{
				"rg_args":             []interface{}{"--no-max-columns-preview"},
				"pattern":             "needle",
				"path":                tmpDir,
				"max_columns_preview": true,
			},
			wantContains:   []string{"--max-columns-preview"},
			wantNotContain: []string{"--no-max-columns-preview"},
		},
		{
			name: "no_max_columns_preview true suppresses rg_args max-columns-preview",
			params: map[string]interface{}{
				"rg_args":                []interface{}{"--max-columns-preview"},
				"pattern":                "needle",
				"path":                   tmpDir,
				"no_max_columns_preview": true,
			},
			wantContains:   []string{"--no-max-columns-preview"},
			wantNotContain: []string{"--max-columns-preview"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotArgs = nil
			result, err := tool.Execute(context.Background(), tc.params)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !result.Success {
				t.Fatalf("expected success, got error %v", result.Error)
			}
			argsStr := strings.Join(gotArgs, " ")
			for _, want := range tc.wantContains {
				if !strings.Contains(argsStr, want) {
					t.Fatalf("expected %q in rg args, got %v", want, gotArgs)
				}
			}
			for _, notWant := range tc.wantNotContain {
				if strings.Contains(argsStr, notWant) {
					t.Fatalf("expected %q to be filtered from rg args, got %v", notWant, gotArgs)
				}
			}
			if tc.name == "max_columns_preview true suppresses rg_args no-max-columns-preview" {
				if result.Metadata["max_columns_preview"] != true || result.Metadata["no_max_columns_preview"] != false {
					t.Fatalf("expected max_columns_preview metadata to be true and no_max_columns_preview false, got %#v", result.Metadata)
				}
			}
			if tc.name == "no_max_columns_preview true suppresses rg_args max-columns-preview" {
				if result.Metadata["max_columns_preview"] != false || result.Metadata["no_max_columns_preview"] != true {
					t.Fatalf("expected no_max_columns_preview metadata to be true and max_columns_preview false, got %#v", result.Metadata)
				}
			}
		})
	}
}

func TestGrepTool_StructuredPriorityOverridesRemainingRgArgsConflicts(t *testing.T) {
	tmpDir := t.TempDir()
	tool := NewGrepTool()

	var gotArgs []string
	tool.lookPath = func(name string) (string, error) {
		return "rg", nil
	}
	tool.runCommand = func(ctx context.Context, binaryPath, workingDir string, args []string) ([]byte, error) {
		gotArgs = append([]string(nil), args...)
		return []byte("sample.txt:1:needle\n"), nil
	}

	cases := []struct {
		name           string
		params         map[string]interface{}
		wantContains   []string
		wantNotContain []string
	}{
		{
			name: "structured sort suppresses rg_args sort family",
			params: map[string]interface{}{
				"pattern": "needle",
				"path":    tmpDir,
				"rg_args": []interface{}{"--sort", "modified", "--sort-files", "--no-sort-files"},
				"sort":    "path",
			},
			wantContains:   []string{"--sort path"},
			wantNotContain: []string{"--sort modified", "--sort-files", "--no-sort-files"},
		},
		{
			name: "structured mode suppresses rg_args count family",
			params: map[string]interface{}{
				"pattern": "needle",
				"path":    tmpDir,
				"rg_args": []interface{}{"--count", "--files-with-matches", "--files-without-match"},
				"mode":    "files",
			},
			wantContains:   []string{"--files-with-matches"},
			wantNotContain: []string{"--count", "--files-without-match"},
		},
		{
			name: "structured type flags suppress rg_args type family",
			params: map[string]interface{}{
				"pattern":    "needle",
				"path":       tmpDir,
				"rg_args":    []interface{}{"--type-add", "foo:*.foo", "--type-clear", "foo"},
				"type_add":   []interface{}{"bar:*.bar"},
				"type_clear": []interface{}{"bar"},
			},
			wantContains:   []string{"--type-add bar:*.bar", "--type-clear bar"},
			wantNotContain: []string{"--type-add foo:*.foo", "--type-clear foo"},
		},
		{
			name: "structured type flags replace rg_args type family",
			params: map[string]interface{}{
				"pattern":    "needle",
				"path":       tmpDir,
				"rg_args":    []interface{}{"--type-add", "foo:*.foo", "--type-clear", "foo"},
				"type_add":   []interface{}{"bar:*.bar"},
				"type_clear": []interface{}{"bar"},
			},
			wantContains:   []string{"--type-add bar:*.bar", "--type-clear bar"},
			wantNotContain: []string{"foo:*.foo", "foo"},
		},
		{
			name: "structured size and depth suppress rg_args numeric family",
			params: map[string]interface{}{
				"pattern":      "needle",
				"path":         tmpDir,
				"rg_args":      []interface{}{"--max-depth", "1", "--max-count", "2", "--max-filesize", "1K"},
				"max_depth":    2,
				"max_count":    3,
				"max_filesize": "2K",
			},
			wantContains:   []string{"--max-depth 2", "--max-count 3", "--max-filesize 2K"},
			wantNotContain: []string{"--max-depth 1", "--max-count 2", "--max-filesize 1K"},
		},
		{
			name: "structured max columns preview family overrides rg_args opposite flag",
			params: map[string]interface{}{
				"pattern":             "needle",
				"path":                tmpDir,
				"rg_args":             []interface{}{"--no-max-columns-preview"},
				"max_columns_preview": true,
			},
			wantContains:   []string{"--max-columns-preview"},
			wantNotContain: []string{"--no-max-columns-preview"},
		},
		{
			name: "structured no max columns preview family overrides rg_args opposite flag",
			params: map[string]interface{}{
				"pattern":                "needle",
				"path":                   tmpDir,
				"rg_args":                []interface{}{"--max-columns-preview"},
				"no_max_columns_preview": true,
			},
			wantContains:   []string{"--no-max-columns-preview"},
			wantNotContain: []string{"--max-columns-preview"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotArgs = nil
			result, err := tool.Execute(context.Background(), tc.params)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !result.Success {
				t.Fatalf("expected success, got error %v", result.Error)
			}
			argsStr := strings.Join(gotArgs, " ")
			for _, want := range tc.wantContains {
				if !strings.Contains(argsStr, want) {
					t.Fatalf("expected %q in rg args, got %v", want, gotArgs)
				}
			}
			for _, notWant := range tc.wantNotContain {
				if strings.Contains(argsStr, notWant) {
					t.Fatalf("expected %q to be filtered from rg args, got %v", notWant, gotArgs)
				}
			}
		})
	}
}

func TestGrepTool_NoIgnoreFilesOverridesExplicitIgnoreFileBuiltinAndRipgrep(t *testing.T) {
	tmpDir := t.TempDir()
	ignorePath := filepath.Join(tmpDir, "custom.ignore")
	if err := os.WriteFile(ignorePath, []byte("ignored.txt\n"), 0o644); err != nil {
		t.Fatalf("write ignore file: %v", err)
	}
	if err := os.WriteFile(filepath.Join(tmpDir, "ignored.txt"), []byte("needle\n"), 0o644); err != nil {
		t.Fatalf("write ignored file: %v", err)
	}
	if err := os.WriteFile(filepath.Join(tmpDir, "kept.txt"), []byte("needle\n"), 0o644); err != nil {
		t.Fatalf("write kept file: %v", err)
	}

	t.Run("builtin", func(t *testing.T) {
		tool := NewGrepTool()
		tool.lookPath = func(name string) (string, error) {
			return "", os.ErrNotExist
		}

		withIgnore, err := tool.Execute(context.Background(), map[string]interface{}{
			"pattern":     "needle",
			"path":        tmpDir,
			"ignore_file": "custom.ignore",
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !strings.Contains(withIgnore.Content, "kept.txt:1: needle") || strings.Contains(withIgnore.Content, "ignored.txt") {
			t.Fatalf("expected ignore_file to hide ignored.txt, got %q", withIgnore.Content)
		}

		noIgnoreFiles, err := tool.Execute(context.Background(), map[string]interface{}{
			"pattern":         "needle",
			"path":            tmpDir,
			"ignore_file":     "custom.ignore",
			"no_ignore_files": true,
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !strings.Contains(noIgnoreFiles.Content, "ignored.txt:1: needle") || !strings.Contains(noIgnoreFiles.Content, "kept.txt:1: needle") {
			t.Fatalf("expected no_ignore_files to override explicit ignore_file, got %q", noIgnoreFiles.Content)
		}
	})

	t.Run("rg_args", func(t *testing.T) {
		tool := NewGrepTool()
		var gotArgs []string
		tool.lookPath = func(name string) (string, error) {
			return "rg", nil
		}
		tool.runCommand = func(ctx context.Context, binaryPath, workingDir string, args []string) ([]byte, error) {
			gotArgs = append([]string(nil), args...)
			return []byte("kept.txt:1:needle\n"), nil
		}

		result, err := tool.Execute(context.Background(), map[string]interface{}{
			"rg_args":         []interface{}{"--no-ignore-files", "--ignore-file-case-insensitive", "--ignore-file", ignorePath, "needle", tmpDir},
			"no_ignore_files": true,
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !result.Success {
			t.Fatalf("expected success, got error %v", result.Error)
		}
		argsStr := strings.Join(gotArgs, " ")
		if !strings.Contains(argsStr, "--no-ignore-files") {
			t.Fatalf("expected --no-ignore-files in rg args, got %v", gotArgs)
		}
		if strings.Contains(argsStr, "--ignore-file") {
			t.Fatalf("expected explicit ignore-file to be suppressed when no_ignore_files is set, got %v", gotArgs)
		}
		if strings.Contains(argsStr, "--ignore-file-case-insensitive") {
			t.Fatalf("expected ignore-file case-insensitive flag to be suppressed when no_ignore_files is set, got %v", gotArgs)
		}
	})
}

func TestGrepTool_IgnoreFileCaseInsensitiveOnlyAppliesToExplicitIgnoreFile(t *testing.T) {
	tmpDir := t.TempDir()
	explicitIgnore := filepath.Join(tmpDir, "custom.ignore")
	if err := os.WriteFile(explicitIgnore, []byte("EXPLICIT-ONLY.TXT\n"), 0o644); err != nil {
		t.Fatalf("write explicit ignore: %v", err)
	}
	if err := os.WriteFile(filepath.Join(tmpDir, "explicit-only.txt"), []byte("needle\n"), 0o644); err != nil {
		t.Fatalf("write explicit target: %v", err)
	}
	if err := os.WriteFile(filepath.Join(tmpDir, "local-git-only.txt"), []byte("needle\n"), 0o644); err != nil {
		t.Fatalf("write dot target: %v", err)
	}
	if err := os.WriteFile(filepath.Join(tmpDir, ".gitignore"), []byte("LOCAL-GIT-ONLY.TXT\n"), 0o644); err != nil {
		t.Fatalf("write gitignore: %v", err)
	}

	tool := NewGrepTool()
	tool.lookPath = func(name string) (string, error) {
		return "", os.ErrNotExist
	}

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"pattern":                      "needle",
		"path":                         tmpDir,
		"ignore_file":                  "custom.ignore",
		"ignore_file_case_insensitive": true,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.Contains(result.Content, "explicit-only.txt:1: needle") {
		t.Fatalf("expected explicit ignore_file case-insensitive match to hide explicit-only.txt, got %q", result.Content)
	}
	if !strings.Contains(result.Content, "local-git-only.txt:1: needle") {
		t.Fatalf("expected local .gitignore to remain case-sensitive and not hide local-git-only.txt, got %q", result.Content)
	}
}

func TestGrepTool_RgArgsMaxColumnsTranslateToRipgrep(t *testing.T) {
	tool := NewGrepTool()
	var gotArgs []string

	tool.lookPath = func(name string) (string, error) {
		return "rg", nil
	}
	tool.runCommand = func(ctx context.Context, binaryPath, workingDir string, args []string) ([]byte, error) {
		gotArgs = append([]string(nil), args...)
		return []byte("sample.txt:1:[Omitted long matching line]\n"), nil
	}

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"pattern":                "foo",
		"path":                   t.TempDir(),
		"max_columns":            20,
		"max_columns_preview":    true,
		"no_max_columns_preview": false,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected success, got error %v", result.Error)
	}
	argsStr := strings.Join(gotArgs, " ")
	for _, want := range []string{"--max-columns 20", "--max-columns-preview"} {
		if !strings.Contains(argsStr, want) {
			t.Fatalf("expected %q in rg args, got %v", want, gotArgs)
		}
	}
	if result.Metadata["max_columns"] != 20 || result.Metadata["max_columns_preview"] != true {
		t.Fatalf("expected max_columns metadata, got %#v", result.Metadata)
	}
}

func TestGrepTool_BuiltinMaxColumnsOmitAndPreview(t *testing.T) {
	tmpDir := t.TempDir()
	longLine := "foo" + strings.Repeat("x", 40) + "bar"
	if err := os.WriteFile(filepath.Join(tmpDir, "sample.txt"), []byte(longLine+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	tool := NewGrepTool()
	tool.lookPath = func(name string) (string, error) {
		return "", os.ErrNotExist
	}

	omitted, err := tool.Execute(context.Background(), map[string]interface{}{
		"pattern":     "foo",
		"path":        tmpDir,
		"max_columns": 20,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !omitted.Success {
		t.Fatalf("expected success, got error %v", omitted.Error)
	}
	if !strings.Contains(omitted.Content, "[Omitted long matching line]") {
		t.Fatalf("expected omitted long line output, got %q", omitted.Content)
	}

	preview, err := tool.Execute(context.Background(), map[string]interface{}{
		"pattern":             "foo",
		"path":                tmpDir,
		"max_columns":         20,
		"max_columns_preview": true,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !preview.Success {
		t.Fatalf("expected success, got error %v", preview.Error)
	}
	if !strings.Contains(preview.Content, "[... omitted end of long line]") {
		t.Fatalf("expected preview long line output, got %q", preview.Content)
	}
}

func TestGrepTool_RgArgsFollowTranslateToRipgrep(t *testing.T) {
	tool := NewGrepTool()
	var gotArgs []string

	tool.lookPath = func(name string) (string, error) {
		return "rg", nil
	}
	tool.runCommand = func(ctx context.Context, binaryPath, workingDir string, args []string) ([]byte, error) {
		gotArgs = append([]string(nil), args...)
		return []byte("sample.txt:1:foo\n"), nil
	}

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"rg_args": []interface{}{"-L", "foo", t.TempDir()},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected success, got error %v", result.Error)
	}
	if !strings.Contains(strings.Join(gotArgs, " "), "--follow") {
		t.Fatalf("expected -L to translate to --follow, got %v", gotArgs)
	}
	if result.Metadata["follow"] != true || result.Metadata["requires_ripgrep"] != true {
		t.Fatalf("expected follow metadata to be true and require rg, got %#v", result.Metadata)
	}
}

func TestGrepTool_RgArgsFollowAndOneFileSystemTranslateToRipgrep(t *testing.T) {
	tool := NewGrepTool()
	var gotArgs []string

	tool.lookPath = func(name string) (string, error) {
		return "rg", nil
	}
	tool.runCommand = func(ctx context.Context, binaryPath, workingDir string, args []string) ([]byte, error) {
		gotArgs = append([]string(nil), args...)
		return []byte("sample.txt:1:foo\n"), nil
	}

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"rg_args": []interface{}{"-L", "--one-file-system", "foo", t.TempDir()},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected success, got error %v", result.Error)
	}
	argsStr := strings.Join(gotArgs, " ")
	for _, want := range []string{"--follow", "--one-file-system"} {
		if !strings.Contains(argsStr, want) {
			t.Fatalf("expected %q in rg args, got %v", want, gotArgs)
		}
	}
	if result.Metadata["follow"] != true || result.Metadata["one_file_system"] != true || result.Metadata["requires_ripgrep"] != true {
		t.Fatalf("expected follow/one_file_system metadata to require rg, got %#v", result.Metadata)
	}
}

func TestGrepTool_FollowRequiresRipgrepWhenUnavailable(t *testing.T) {
	tmpDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmpDir, "sample.txt"), []byte("foo\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	tool := NewGrepTool()
	tool.lookPath = func(name string) (string, error) {
		return "", os.ErrNotExist
	}

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"pattern":         "foo",
		"path":            tmpDir,
		"follow":          true,
		"one_file_system": true,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Success {
		t.Fatalf("expected follow to fail cleanly without rg")
	}
	if !strings.Contains(result.Error.Error(), "仅 ripgrep/rg 支持") {
		t.Fatalf("expected clear rg-only follow error, got %v", result.Error)
	}
	if !strings.Contains(result.Error.Error(), "follow") {
		t.Fatalf("expected follow requirement to appear in error, got %v", result.Error)
	}
	if !strings.Contains(result.Error.Error(), "one_file_system") {
		t.Fatalf("expected combined one_file_system requirement to appear in error, got %v", result.Error)
	}
}

func TestGrepTool_StructuredRgOnlyFlagsTranslateToRipgrep(t *testing.T) {
	tmpDir := t.TempDir()
	tool := NewGrepTool()

	var gotArgs []string
	tool.lookPath = func(name string) (string, error) {
		return "rg", nil
	}
	tool.runCommand = func(ctx context.Context, binaryPath, workingDir string, args []string) ([]byte, error) {
		gotArgs = append([]string(nil), args...)
		if containsString(args, "--json") {
			return []byte("{\"type\":\"summary\",\"data\":{\"elapsed_total\":{\"human\":\"0.001000s\",\"nanos\":1000000,\"secs\":0},\"stats\":{\"bytes_printed\":0,\"bytes_searched\":14,\"elapsed\":{\"human\":\"0.000100s\",\"nanos\":100000,\"secs\":0},\"matched_lines\":1,\"matches\":2,\"searches\":1,\"searches_with_match\":1}}}\n"), nil
		}
		return []byte("sample.txt:1:needle\n"), nil
	}

	cases := []struct {
		name       string
		params     map[string]interface{}
		wantArgs   []string
		assertions func(t *testing.T, result *toolkit.ToolResult)
	}{
		{
			name: "stats true emits --stats",
			params: map[string]interface{}{
				"pattern": "needle",
				"path":    tmpDir,
				"stats":   true,
			},
			wantArgs: []string{"--stats"},
			assertions: func(t *testing.T, result *toolkit.ToolResult) {
				t.Helper()
				if result.Metadata["stats_requested"] != true {
					t.Fatalf("expected stats_requested metadata to be true, got %#v", result.Metadata["stats_requested"])
				}
			},
		},
		{
			name: "json true emits --json and keeps rg passthrough metadata",
			params: map[string]interface{}{
				"pattern": "needle",
				"path":    tmpDir,
				"json":    true,
			},
			wantArgs: []string{"--json"},
			assertions: func(t *testing.T, result *toolkit.ToolResult) {
				t.Helper()
				if result.Metadata["json_output_requested"] != true || result.Metadata["normalized_output"] != false {
					t.Fatalf("expected json output metadata to be marked correctly, got %#v", result.Metadata)
				}
				if result.Metadata["output_format"] != "rg_passthrough" {
					t.Fatalf("expected rg_passthrough output_format, got %#v", result.Metadata["output_format"])
				}
				if result.Metadata["requires_ripgrep"] != true {
					t.Fatalf("expected json mode to require ripgrep, got %#v", result.Metadata["requires_ripgrep"])
				}
				if !strings.Contains(result.Content, "\"type\":\"summary\"") {
					t.Fatalf("expected raw rg json summary content, got %q", result.Content)
				}
			},
		},
		{
			name: "follow true emits --follow",
			params: map[string]interface{}{
				"pattern": "needle",
				"path":    tmpDir,
				"follow":  true,
			},
			wantArgs: []string{"--follow"},
			assertions: func(t *testing.T, result *toolkit.ToolResult) {
				t.Helper()
				if result.Metadata["follow"] != true || result.Metadata["requires_ripgrep"] != true {
					t.Fatalf("expected follow metadata to be true and require rg, got %#v", result.Metadata)
				}
			},
		},
		{
			name: "follow plus one_file_system emits both rg-only flags",
			params: map[string]interface{}{
				"pattern":         "needle",
				"path":            tmpDir,
				"follow":          true,
				"one_file_system": true,
			},
			wantArgs: []string{"--follow", "--one-file-system"},
			assertions: func(t *testing.T, result *toolkit.ToolResult) {
				t.Helper()
				if result.Metadata["follow"] != true || result.Metadata["one_file_system"] != true || result.Metadata["requires_ripgrep"] != true {
					t.Fatalf("expected follow/one_file_system metadata to require rg, got %#v", result.Metadata)
				}
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotArgs = nil
			result, err := tool.Execute(context.Background(), tc.params)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !result.Success {
				t.Fatalf("expected success, got error %v", result.Error)
			}
			argsStr := strings.Join(gotArgs, " ")
			for _, want := range tc.wantArgs {
				if !strings.Contains(argsStr, want) {
					t.Fatalf("expected %q in rg args, got %v", want, gotArgs)
				}
			}
			if tc.assertions != nil {
				tc.assertions(t, result)
			}
		})
	}
}

func TestGrepTool_RgArgsColorConsumesNextValueBuiltin(t *testing.T) {
	tmpDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmpDir, "sample.txt"), []byte("foo\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	tool := NewGrepTool()
	tool.lookPath = func(name string) (string, error) {
		return "", os.ErrNotExist
	}

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"rg_args": []interface{}{"--color", "never", "foo", tmpDir},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected success, got error %v", result.Error)
	}
	if !strings.Contains(result.Content, "sample.txt:1: foo") {
		t.Fatalf("expected --color never to be consumed as no-op and still match foo, got %q", result.Content)
	}
	ignored, ok := result.Metadata["ignored_rg_args"].([]string)
	if !ok || len(ignored) == 0 || ignored[0] != "--color=never" {
		t.Fatalf("expected ignored_rg_args metadata to record --color=never, got %#v", result.Metadata["ignored_rg_args"])
	}
}

func TestGrepTool_RgArgsDisplayNoOpFlagsBuiltin(t *testing.T) {
	tmpDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmpDir, "sample.txt"), []byte("foo\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	tool := NewGrepTool()
	tool.lookPath = func(name string) (string, error) {
		return "", os.ErrNotExist
	}

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"rg_args": []interface{}{
			"-N",
			"--no-filename",
			"--pretty",
			"--line-buffered",
			"--no-line-buffered",
			"--block-buffered",
			"--no-block-buffered",
			"--null",
			"--null-data",
			"--field-context-separator", ":::",
			"--path-separator", "/",
			"foo",
			tmpDir,
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected success, got error %v", result.Error)
	}
	if result.Content != "sample.txt:1: foo" {
		t.Fatalf("expected display no-op flags to preserve normalized output skeleton, got %q", result.Content)
	}
	ignored, ok := result.Metadata["ignored_rg_args"].([]string)
	if !ok || len(ignored) < 10 {
		t.Fatalf("expected ignored_rg_args metadata for display flags, got %#v", result.Metadata["ignored_rg_args"])
	}
	for _, want := range []string{"--block-buffered", "--no-block-buffered", "--null", "--null-data", "--field-context-separator=:::", "--path-separator=/"} {
		if !containsString(ignored, want) {
			t.Fatalf("expected ignored_rg_args to include %s, got %#v", want, ignored)
		}
	}
}

func TestGrepTool_StructuredPresentationFlagsIgnoredMetadata(t *testing.T) {
	tmpDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmpDir, "sample.txt"), []byte("foo\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	tool := NewGrepTool()
	tool.lookPath = func(name string) (string, error) {
		return "", os.ErrNotExist
	}

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"pattern":                 "foo",
		"path":                    tmpDir,
		"line_number":             true,
		"heading":                 true,
		"no_heading":              true,
		"with_filename":           true,
		"no_filename":             true,
		"no_line_number":          true,
		"color":                   "always",
		"pretty":                  true,
		"line_buffered":           true,
		"no_line_buffered":        true,
		"block_buffered":          true,
		"no_block_buffered":       true,
		"null":                    true,
		"null_data":               true,
		"field_context_separator": ":::",
		"path_separator":          "/",
		"text":                    true,
		"binary":                  true,
		"hidden":                  true,
		"no_ignore":               true,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected success, got error %v", result.Error)
	}
	if result.Content != "sample.txt:1: foo" {
		t.Fatalf("expected structured presentation flags to preserve normalized output, got %q", result.Content)
	}
	ignored, ok := result.Metadata["ignored_presentation"].([]string)
	if !ok || len(ignored) < 13 {
		t.Fatalf("expected ignored_presentation metadata, got %#v", result.Metadata["ignored_presentation"])
	}
	for _, want := range []string{"block_buffered", "no_block_buffered", "null", "null_data", "field_context_separator=:::", "path_separator=/"} {
		if !containsString(ignored, want) {
			t.Fatalf("expected ignored_presentation to include %s, got %#v", want, ignored)
		}
	}
}

func TestGrepTool_SortFilesStructuredBuiltin(t *testing.T) {
	tmpDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmpDir, "b.txt"), []byte("needle\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tmpDir, "a.txt"), []byte("needle\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	tool := NewGrepTool()
	tool.lookPath = func(name string) (string, error) {
		return "", os.ErrNotExist
	}

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"pattern":    "needle",
		"path":       tmpDir,
		"mode":       "files",
		"sort_files": true,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected success, got error %v", result.Error)
	}
	lines := strings.Split(strings.TrimSpace(result.Content), "\n")
	if len(lines) != 2 || lines[0] != "a.txt" || lines[1] != "b.txt" {
		t.Fatalf("expected sort_files to force path ordering [a.txt b.txt], got %v", lines)
	}
	if result.Metadata["sort_files"] != true || result.Metadata["sort"] != "path" {
		t.Fatalf("expected sort_files metadata, got %#v", result.Metadata)
	}
}

func TestGrepTool_RgArgsNoSortFilesOverridesSortFiles(t *testing.T) {
	tool := NewGrepTool()
	var gotArgs []string

	tool.lookPath = func(name string) (string, error) {
		return "rg", nil
	}
	tool.runCommand = func(ctx context.Context, binaryPath, workingDir string, args []string) ([]byte, error) {
		gotArgs = append([]string(nil), args...)
		return []byte("sample.txt:1:foo\n"), nil
	}

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"rg_args": []interface{}{"--sort-files", "--no-sort-files", "foo", t.TempDir()},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected success, got error %v", result.Error)
	}
	argsStr := strings.Join(gotArgs, " ")
	if strings.Contains(argsStr, "--sort path") || strings.Contains(argsStr, "--sortr ") {
		t.Fatalf("expected --no-sort-files to clear explicit sort flags, got %v", gotArgs)
	}
	if result.Metadata["sort"] != "" || result.Metadata["sort_files"] != false {
		t.Fatalf("expected cleared sort metadata, got %#v", result.Metadata)
	}
}

func TestGrepTool_SortReversePathBuiltin(t *testing.T) {
	tmpDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmpDir, "a.txt"), []byte("needle\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tmpDir, "b.txt"), []byte("needle\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	tool := NewGrepTool()
	tool.lookPath = func(name string) (string, error) {
		return "", os.ErrNotExist
	}

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"pattern": "needle",
		"path":    tmpDir,
		"mode":    "files",
		"sortr":   "path",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Fatalf("expected success, got error %v", result.Error)
	}
	lines := strings.Split(strings.TrimSpace(result.Content), "\n")
	if len(lines) != 2 {
		t.Fatalf("expected 2 files in reverse path order, got %d (%q)", len(lines), result.Content)
	}
	if lines[0] != "b.txt" || lines[1] != "a.txt" {
		t.Fatalf("expected reverse path ordering [b.txt a.txt], got %v", lines)
	}
	if result.Metadata["sortr"] != "path" {
		t.Fatalf("expected sortr metadata to be path, got %#v", result.Metadata["sortr"])
	}
}

func TestGrepTool_StructuredSortFamilyTranslateToRipgrep(t *testing.T) {
	tmpDir := t.TempDir()
	tool := NewGrepTool()

	var gotArgs []string
	tool.lookPath = func(name string) (string, error) {
		return "rg", nil
	}
	tool.runCommand = func(ctx context.Context, binaryPath, workingDir string, args []string) ([]byte, error) {
		gotArgs = append([]string(nil), args...)
		return []byte("sample.txt:1:needle\n"), nil
	}

	cases := []struct {
		name       string
		params     map[string]interface{}
		wantArgs   []string
		assertions func(t *testing.T, result *toolkit.ToolResult)
	}{
		{
			name: "sort path emits --sort path",
			params: map[string]interface{}{
				"pattern": "needle",
				"path":    tmpDir,
				"sort":    "path",
			},
			wantArgs: []string{"--sort path"},
			assertions: func(t *testing.T, result *toolkit.ToolResult) {
				t.Helper()
				gotSort, _ := result.Metadata["sort"].(string)
				gotSortr, _ := result.Metadata["sortr"].(string)
				gotSortFiles, _ := result.Metadata["sort_files"].(bool)
				if gotSort != "path" || gotSortr != "" || !gotSortFiles {
					t.Fatalf("expected sort metadata to align with path ordering, got %#v", result.Metadata)
				}
			},
		},
		{
			name: "sortr path emits --sortr path",
			params: map[string]interface{}{
				"pattern": "needle",
				"path":    tmpDir,
				"sortr":   "path",
			},
			wantArgs: []string{"--sortr path"},
			assertions: func(t *testing.T, result *toolkit.ToolResult) {
				t.Helper()
				gotSort, _ := result.Metadata["sort"].(string)
				gotSortr, _ := result.Metadata["sortr"].(string)
				gotSortFiles, _ := result.Metadata["sort_files"].(bool)
				if gotSort != "" || gotSortr != "path" || gotSortFiles {
					t.Fatalf("expected sortr metadata to align with reverse path ordering, got %#v", result.Metadata)
				}
			},
		},
		{
			name: "sort_files true emits path ordering",
			params: map[string]interface{}{
				"pattern":    "needle",
				"path":       tmpDir,
				"sort_files": true,
			},
			wantArgs: []string{"--sort path"},
			assertions: func(t *testing.T, result *toolkit.ToolResult) {
				t.Helper()
				gotSort, _ := result.Metadata["sort"].(string)
				gotSortFiles, _ := result.Metadata["sort_files"].(bool)
				if gotSort != "path" || !gotSortFiles {
					t.Fatalf("expected sort_files metadata to align with path ordering, got %#v", result.Metadata)
				}
			},
		},
		{
			name: "no_sort_files clears structured path ordering",
			params: map[string]interface{}{
				"pattern":       "needle",
				"path":          tmpDir,
				"sort_files":    true,
				"no_sort_files": true,
				"sort":          "path",
			},
			assertions: func(t *testing.T, result *toolkit.ToolResult) {
				t.Helper()
				gotSort, _ := result.Metadata["sort"].(string)
				gotSortr, _ := result.Metadata["sortr"].(string)
				gotSortFiles, _ := result.Metadata["sort_files"].(bool)
				if gotSort != "" || gotSortr != "" || gotSortFiles {
					t.Fatalf("expected no_sort_files to clear sort family metadata, got %#v", result.Metadata)
				}
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotArgs = nil
			result, err := tool.Execute(context.Background(), tc.params)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !result.Success {
				t.Fatalf("expected success, got error %v", result.Error)
			}
			argsStr := strings.Join(gotArgs, " ")
			for _, want := range tc.wantArgs {
				if !strings.Contains(argsStr, want) {
					t.Fatalf("expected %q in rg args, got %v", want, gotArgs)
				}
			}
			if tc.name == "no_sort_files clears structured path ordering" {
				if strings.Contains(argsStr, "--sort") || strings.Contains(argsStr, "--sortr") {
					t.Fatalf("expected no_sort_files to remove sort flags, got %v", gotArgs)
				}
			}
			if tc.assertions != nil {
				tc.assertions(t, result)
			}
		})
	}
}
