package knowledge

import (
	"context"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
)

// .gitignore 过滤的单元测试：钉住常用 git 语义，防止"过滤太宽/太窄"回归。
func TestGitignoreRuleMatches(t *testing.T) {
	t.Parallel()
	cases := []struct {
		pattern string
		path    string
		isDir   bool
		want    bool
	}{
		{"*.log", "a.log", false, true},
		{"*.log", "dir/a.log", false, true},
		{"*.log", "a.logx", false, false},

		// 尾部 '/' 仅匹配目录；无 '/' 的模式任意深度生效。
		{"build/", "build", true, true},
		{"build/", "src/build", true, true},
		{"build/", "build", false, false},

		// 前导 '/' 锚定到 .gitignore 所在目录。
		{"/build", "build", true, true},
		{"/build", "src/build", true, false},

		// 含 '/' 的模式锚定；'*' 不跨 '/'。
		{"foo/bar.go", "foo/bar.go", false, true},
		{"foo/bar.go", "x/foo/bar.go", false, false},

		// '**' 的三种整段形态。
		{"doc/**/*.md", "doc/a.md", false, true},
		{"doc/**/*.md", "doc/x/y/a.md", false, true},
		{"doc/**/*.md", "other/a.md", false, false},
		{"**/temp", "temp", true, true},
		{"**/temp", "a/b/temp", true, true},
		{"a/**", "a/x/y", false, true},
		{"a/**", "a", true, false},

		// 转义与字符类。
		{`\#literal`, "#literal", false, true},
		{`\!bang`, "!bang", false, true},
		{`foo\ `, "foo ", false, true},
		{"foo   ", "foo", false, true},
		{"*.log\r", "a.log", false, true}, // Windows CRLF
		{"[!a]x.go", "bx.go", false, true},
		{"[!a]x.go", "ax.go", false, false},
		{"[a-c]x.go", "bx.go", false, true},
		{"[a-c]x.go", "dx.go", false, false},
		{`[\]]x.go`, "]x.go", false, true},
	}
	for _, tc := range cases {
		rule, ok := parseGitignoreRule(tc.pattern)
		if !ok {
			t.Errorf("parseGitignoreRule(%q) 失败", tc.pattern)
			continue
		}
		if got := rule.matches(tc.path, tc.isDir); got != tc.want {
			t.Errorf("pattern %q matches(%q, isDir=%v) = %v, want %v",
				tc.pattern, tc.path, tc.isDir, got, tc.want)
		}
	}
}

// 嵌套 .gitignore 的优先级：深层文件覆盖上层文件；同文件内后面的行覆盖前面的行。
func TestGitignoreSetNestedPrecedence(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeTree(t, root, ".gitignore", "*.gen.go\n!keep.gen.go\n")
	writeTree(t, root, "sub/.gitignore", "!override.gen.go\n")

	var set gitignoreSet
	if f := loadGitignoreFile(root, ""); f != nil {
		set.push(*f)
	}
	if f := loadGitignoreFile(filepath.Join(root, "sub"), "sub"); f != nil {
		set.push(*f)
	}

	cases := []struct {
		rel  string
		want bool
	}{
		{"a.gen.go", true},
		{"keep.gen.go", false},
		{"sub/x.gen.go", true},
		{"sub/keep.gen.go", false},
		{"sub/override.gen.go", false}, // 深层取反覆盖上层的 *.gen.go
	}
	for _, tc := range cases {
		if got := set.isIgnored(tc.rel, false); got != tc.want {
			t.Errorf("isIgnored(%q) = %v, want %v", tc.rel, got, tc.want)
		}
	}
}

// collectIndexableFiles 的集成口径：.gitignore 生效，同时内置忽略集
// （node_modules）与隐藏目录规则保持不变。
func TestCollectIndexableFilesRespectsGitignore(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeTree(t, root, ".gitignore", strings.Join([]string{
		"# 构建产物与生成文件",
		"generated/*",
		"!generated/keep.go",
		"*.gen.go",
		"logs/",
		"",
	}, "\n"))
	writeTree(t, root, "src/a.go", "package src\n")
	writeTree(t, root, "generated/drop.go", "package generated\n")
	writeTree(t, root, "generated/keep.go", "package generated\n")
	writeTree(t, root, "src/schema.gen.go", "package src\n")
	writeTree(t, root, "logs/trace.go", "package logs\n")
	writeTree(t, root, "node_modules/dep/index.js", "export default 1\n")
	writeTree(t, root, ".hidden/a.go", "package hidden\n")
	writeTree(t, root, "sub/.gitignore", "local.go\n")
	writeTree(t, root, "sub/local.go", "package sub\n")
	writeTree(t, root, "sub/keep.go", "package sub\n")

	cfg := DefaultConfig().WithWorkspace(root)
	files, truncated, err := collectIndexableFiles(cfg)
	if err != nil {
		t.Fatalf("collectIndexableFiles: %v", err)
	}
	if truncated {
		t.Fatal("truncated = true, want false")
	}

	got := make([]string, 0, len(files))
	for _, abs := range files {
		rel, err := filepath.Rel(root, abs)
		if err != nil {
			t.Fatalf("rel(%s): %v", abs, err)
		}
		got = append(got, normalizeRelPath(rel))
	}
	sort.Strings(got)
	want := []string{"generated/keep.go", "src/a.go", "sub/keep.go"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("候选文件 = %v, want %v", got, want)
	}
}

// knowledge.index.use_gitignore=false 是回滚逃生舱：关闭后只保留内置忽略集。
func TestCollectIndexableFilesGitignoreCanBeDisabled(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeTree(t, root, ".gitignore", "generated/\n")
	writeTree(t, root, "generated/drop.go", "package generated\n")
	writeTree(t, root, "src/a.go", "package src\n")

	cfg := DefaultConfig().WithWorkspace(root)
	off := false
	cfg.Index.UseGitignore = &off

	files, _, err := collectIndexableFiles(cfg)
	if err != nil {
		t.Fatalf("collectIndexableFiles: %v", err)
	}
	got := make([]string, 0, len(files))
	for _, abs := range files {
		rel, _ := filepath.Rel(root, abs)
		got = append(got, normalizeRelPath(rel))
	}
	sort.Strings(got)
	want := []string{"generated/drop.go", "src/a.go"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("关闭 .gitignore 后候选文件 = %v, want %v", got, want)
	}
}

// 项目新增 .gitignore 后，下一次全量索引必须把已入库的被忽略文件软删除
// （否则缓存/构建产物会永久留在知识库里）。
func TestRunIndexSoftDeletesNewlyIgnoredFile(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	root := t.TempDir()
	writeTree(t, root, "gen/out.go", "package gen\n")
	writeTree(t, root, "src/a.go", "package src\n")

	store := newTestStore(t)
	cfg := DefaultConfig().WithWorkspace(root)
	first, err := RunIndex(ctx, store, cfg)
	if err != nil {
		t.Fatalf("RunIndex(first): %v", err)
	}
	if first.Indexed != 2 {
		t.Fatalf("首次索引应写入 2 个文件: %+v", first)
	}

	writeTree(t, root, ".gitignore", "gen/\n")
	second, err := RunIndex(ctx, store, cfg)
	if err != nil {
		t.Fatalf("RunIndex(second): %v", err)
	}
	if second.Deleted != 1 || second.Indexed != 0 {
		t.Fatalf("新增忽略规则后应对账软删: %+v", second)
	}

	wsID, err := store.EnsureWorkspace(ctx, Workspace{RootPath: root})
	if err != nil {
		t.Fatalf("EnsureWorkspace: %v", err)
	}
	rec, ok, err := store.FileByPath(ctx, wsID, "gen/out.go")
	if err != nil || !ok || rec.DeletedAt == 0 {
		t.Fatalf("gen/out.go 应被软删除: ok=%v rec=%+v err=%v", ok, rec, err)
	}
}

// 目录通配 + 取反链的真实形态（本仓库 .gitignore 对 output/ 的处理）：
// "dir/*" 忽略内容、"!dir/" 放行目录、"!dir/**" 取回内容——三段缺一不可，
// 否则源码包会被整目录误杀。
func TestGitignoreSetReincludeUnderDirectoryWildcard(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeTree(t, root, ".gitignore", strings.Join([]string{
		"backend/cmd/aicli/ui/render/output/*",
		"!backend/cmd/aicli/ui/render/output/",
		"!backend/cmd/aicli/ui/render/output/**",
		"artifacts/",
		"",
	}, "\n"))

	var set gitignoreSet
	if f := loadGitignoreFile(root, ""); f != nil {
		set.push(*f)
	}

	cases := []struct {
		rel   string
		isDir bool
		want  bool
	}{
		{"backend/cmd/aicli/ui/render/output", true, false},
		{"backend/cmd/aicli/ui/render/output/render.go", false, false},
		{"artifacts", true, true},
		{"artifacts/scratch.py", false, false}, // 目录规则不直接匹配文件（walk 在目录层剪枝）
	}
	for _, tc := range cases {
		if got := set.isIgnored(tc.rel, tc.isDir); got != tc.want {
			t.Errorf("isIgnored(%q, isDir=%v) = %v, want %v", tc.rel, tc.isDir, got, tc.want)
		}
	}
}
