package tools

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// Windows junction 回归（2026-10-17 tabbit 实例）：
//   - rg 引擎：递归遍历默认不进入 NTFS junction 子树（walkdir 视为 symlink）；
//     Windows 宿主上未显式指定 follow 时必须默认注入 --follow。
//   - builtin 引擎：filepath.Walk 在部分 Go 版本对 junction 的 Lstat 既不带
//     ModeDir 也不带 ModeSymlink（标记 ModeIrregular），同样整棵跳过；
//     walk 阶段必须对 reparse 目录做 Stat 复核后下钻。
//
// 第三方工具常以 junction 把 skill 目录挂进标准安装位
// （%USERPROFILE%\.agents\skills），缺省 grep 会静默丢失整棵子树的匹配。
func TestGrepTool_WindowsJunctionSubtreeSearchedByDefault(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("windows-only: NTFS junction")
	}

	root := t.TempDir()
	target := filepath.Join(root, "real")
	require.NoError(t, os.MkdirAll(target, 0o755))
	require.NoError(t, os.WriteFile(
		filepath.Join(target, "note.md"),
		[]byte("hello receipt marker\n"),
		0o644))
	junction := filepath.Join(root, "link")
	out, err := exec.Command("cmd", "/C", "mklink", "/J", junction, target).CombinedOutput()
	if err != nil {
		t.Skipf("cannot create junction (privileges?): %v: %s", err, string(out))
	}
	t.Cleanup(func() { _ = os.Remove(junction) })

	// Lstat 前置断言：junction 必须呈现「非目录」的 Lstat 形态，否则本机
	// Go/rg 已原生把 junction 视为目录，该回归场景不再成立。
	if info, err := os.Lstat(junction); err == nil && info.IsDir() {
		t.Skip("this Go build already reports junctions as directories; nothing to regress")
	}

	tool := NewGrepTool()

	// 默认（Windows 宿主隐式 follow）：junction 子树必须参与搜索。
	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"pattern": "receipt",
		"path":    root,
	})
	require.NoError(t, err)
	require.True(t, result.Success, "execute must succeed: %v", result.Error)
	content := result.Content
	require.Contains(t, content, "receipt")
	require.True(t,
		strings.Contains(content, "link\\note.md") || strings.Contains(content, "real\\note.md"),
		"expected junction/real subtree match, got: %s", content)

	// 显式 follow=false 保持 rg 缺省语义（junction 可被跳过，不报错即可）。
	result, err = tool.Execute(context.Background(), map[string]interface{}{
		"pattern": "receipt",
		"path":    root,
		"follow":  false,
	})
	require.NoError(t, err)
	require.True(t, result.Success, "explicit follow=false must still succeed: %v", result.Error)
}

// builtin walker 的 reparse 复核是独立单元行为：构造 junction root，
// searchWithWalker 的候选集必须包含 junction 内的文件。
func TestGrepWalker_WindowsJunctionRootSearched(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("windows-only: NTFS junction")
	}

	root := t.TempDir()
	target := filepath.Join(root, "real")
	require.NoError(t, os.MkdirAll(target, 0o755))
	require.NoError(t, os.WriteFile(
		filepath.Join(target, "note.md"),
		[]byte("hello receipt marker\n"),
		0o644))
	junction := filepath.Join(root, "link")
	out, err := exec.Command("cmd", "/C", "mklink", "/J", junction, target).CombinedOutput()
	if err != nil {
		t.Skipf("cannot create junction (privileges?): %v: %s", err, string(out))
	}
	t.Cleanup(func() { _ = os.Remove(junction) })

	if info, err := os.Lstat(junction); err == nil && info.IsDir() {
		t.Skip("this Go build already reports junctions as directories; nothing to regress")
	}

	scopes, err := resolveSearchScopes([]string{root}, []string{root}, root)
	require.NoError(t, err)
	require.NotEmpty(t, scopes)

	opts := &grepOptions{
		pattern:      "receipt",
		basePath:     root,
		searchScopes: scopes,
	}

	tool := NewGrepTool()
	re, err := compileGrepPattern([]string{"receipt"}, false, true, false, false)
	require.NoError(t, err)

	result := tool.searchWithWalker(context.Background(), opts, re)
	require.NotNil(t, result)
	require.True(t, result.Success, "walker must succeed: %v", result.Error)
	content := result.Content
	require.Contains(t, content, "receipt",
		"builtin walker must search junction subtree, got: %s", content)
}
