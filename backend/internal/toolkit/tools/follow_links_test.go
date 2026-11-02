package tools

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func linkedTree(t *testing.T) (root, target string) {
	t.Helper()
	base := t.TempDir()
	target = filepath.Join(base, "target")
	root = filepath.Join(base, "root")
	for _, dir := range []string{target, root} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(target, "one.go"), []byte("package one\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(root, "linked")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	return root, target
}

func TestLsTool_FollowsLinkedDirectory(t *testing.T) {
	root, _ := linkedTree(t)
	defaultFollow := runtime.GOOS == "windows"
	cases := []struct {
		name       string
		setFollow  bool
		follow     bool
		wantFollow bool
	}{
		{name: "default", wantFollow: defaultFollow},
		{name: "explicit true", setFollow: true, follow: true, wantFollow: true},
		{name: "explicit false", setFollow: true, follow: false, wantFollow: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			args := map[string]interface{}{"path": root, "depth": 2}
			if tc.setFollow {
				args["follow"] = tc.follow
			}
			result, err := NewLsTool().Execute(context.Background(), args)
			if err != nil {
				t.Fatal(err)
			}
			if !result.Success {
				t.Fatalf("ls failed: %v", result.Error)
			}
			if got := result.Metadata["follow"]; got != tc.wantFollow {
				t.Fatalf("metadata.follow = %#v, want %v", got, tc.wantFollow)
			}
			if tc.wantFollow {
				if !strings.Contains(result.Content, "📁 linked/") || !strings.Contains(result.Content, "linked/one.go") {
					t.Fatalf("followed ls did not enumerate the linked directory:\n%s", result.Content)
				}
			} else {
				if !strings.Contains(result.Content, "📄 linked") || strings.Contains(result.Content, "linked/one.go") {
					t.Fatalf("follow=false leaked the linked tree:\n%s", result.Content)
				}
			}
		})
	}
}

func TestGlobTool_FollowsLinkedDirectory(t *testing.T) {
	root, _ := linkedTree(t)
	defaultFollow := runtime.GOOS == "windows"
	cases := []struct {
		name       string
		setFollow  bool
		follow     bool
		wantFollow bool
	}{
		{name: "default", wantFollow: defaultFollow},
		{name: "explicit true", setFollow: true, follow: true, wantFollow: true},
		{name: "explicit false", setFollow: true, follow: false, wantFollow: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			args := map[string]interface{}{"path": root, "pattern": "**/*.go"}
			if tc.setFollow {
				args["follow"] = tc.follow
			}
			result, err := NewGlobTool().Execute(context.Background(), args)
			if err != nil {
				t.Fatal(err)
			}
			if !result.Success {
				t.Fatalf("glob failed: %v", result.Error)
			}
			if got := result.Metadata["follow"]; got != tc.wantFollow {
				t.Fatalf("metadata.follow = %#v, want %v", got, tc.wantFollow)
			}
			files, _ := result.Metadata["files"].([]string)
			if tc.wantFollow {
				if len(files) != 1 || !strings.HasSuffix(filepath.ToSlash(files[0]), "linked/one.go") {
					t.Fatalf("followed glob files = %v, want linked/one.go", files)
				}
			} else if len(files) != 0 {
				t.Fatalf("follow=false glob leaked the linked tree: %v", files)
			}
		})
	}
}

// The path argument is the root the caller named. Both tools must read it even
// with follow=false; the flag only decides what happens to links found below.
func TestLsTool_SymlinkedRootIsListedWhenFollowFalse(t *testing.T) {
	root, _ := linkedTree(t)
	link := filepath.Join(root, "linked")

	result, err := NewLsTool().Execute(context.Background(), map[string]interface{}{
		"path":   link,
		"follow": false,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Success {
		t.Fatalf("ls failed: %v", result.Error)
	}
	if got := result.Metadata["follow"]; got != false {
		t.Fatalf("metadata.follow = %#v, want false", got)
	}
	if !strings.Contains(result.Content, "one.go") {
		t.Fatalf("symlinked root was not listed:\n%s", result.Content)
	}
	if strings.Contains(result.Content, "空目录") {
		t.Fatalf("symlinked root must not be reported as an empty directory:\n%s", result.Content)
	}
}

func TestGlobTool_SymlinkedRootIsSearchedWhenFollowFalse(t *testing.T) {
	root, _ := linkedTree(t)
	link := filepath.Join(root, "linked")

	result, err := NewGlobTool().Execute(context.Background(), map[string]interface{}{
		"path":    link,
		"pattern": "**/*.go",
		"follow":  false,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Success {
		t.Fatalf("glob failed: %v", result.Error)
	}
	files, _ := result.Metadata["files"].([]string)
	if len(files) != 1 || !strings.HasSuffix(filepath.ToSlash(files[0]), "linked/one.go") {
		t.Fatalf("symlinked-root glob files = %v, want linked/one.go", files)
	}
}
