package aiclipaths

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestProjectScopeSlugNormalizesPaths(t *testing.T) {
	root := t.TempDir()
	slug := ProjectScopeSlug(root)
	if slug == "" {
		t.Fatalf("临时目录应生成 slug: %q", root)
	}
	if strings.ContainsAny(slug, `/\: `) {
		t.Fatalf("slug 不得包含路径分隔符或空格: %q", slug)
	}

	// 同一路径的大小写/分隔符变体应得到相同 slug（Windows 下大小写不敏感）。
	upper := ProjectScopeSlug(strings.ToUpper(filepath.ToSlash(root)))
	if upper != slug {
		t.Fatalf("大小写变体应归一化: %q vs %q", upper, slug)
	}
	if ProjectScopeSlug("") != "" {
		t.Fatal("空路径应返回空 slug")
	}
}

func TestLocalMCPConfigPathIsStableAndHomeScoped(t *testing.T) {
	home := filepath.Join(t.TempDir(), "home")
	isolateHome(t, home)
	project := t.TempDir()

	path, err := LocalMCPConfigPath(project)
	if err != nil {
		t.Fatalf("LocalMCPConfigPath: %v", err)
	}
	want := filepath.Join(home, ".aicli", "projects", ProjectScopeSlug(project), DefaultMCPConfigFileName)
	if filepath.Clean(path) != filepath.Clean(want) {
		t.Fatalf("local 路径 = %q, want %q", path, want)
	}

	again, err := LocalMCPConfigPath(project)
	if err != nil || again != path {
		t.Fatalf("同一项目应得到稳定路径: %q vs %q (%v)", again, path, err)
	}
	if other, _ := LocalMCPConfigPath(t.TempDir()); other == path {
		t.Fatal("不同项目不应共用同一路径")
	}
}

// slug 退化为空（如纯符号路径）时必须用哈希兜底，避免所有项目共用一个目录。
func TestLocalMCPConfigPathFallsBackToHash(t *testing.T) {
	home := filepath.Join(t.TempDir(), "home")
	isolateHome(t, home)

	path, err := LocalMCPConfigPath("///")
	if err != nil {
		t.Fatalf("LocalMCPConfigPath: %v", err)
	}
	dir := filepath.Base(filepath.Dir(path))
	if !strings.HasPrefix(dir, "project-") {
		t.Fatalf("应使用哈希兜底目录: %q", dir)
	}
	if _, err := os.Stat(filepath.Dir(path)); err == nil {
		t.Fatal("纯计算路径函数不应创建目录")
	}
}
