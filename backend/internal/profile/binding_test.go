package profile

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// FR-14 第一阶段：项目绑定的**只读发现**。
//
// 本文件只钉两件事：
//   - 指针文件的形状/ref 安全校验（拒绝绝对路径、穿越、多段名、嵌入对象……）；
//   - 目标必须落在**本工作区**项目层，缺失时如实报错而不是回退到 user/default。
//
// 绑定绝不激活 profile——发现层不产生任何 default/session 副作用，因此这里也不
// 断言"生效面"；生效面由既有 resolver 与 /profile 显式切换负责（见 profiles API 测试）。

// writeProjectBindingFixture 在 <workspace>/.aicli/profile 落一个指针文件。
func writeProjectBindingFixture(t *testing.T, workspace, content string) string {
	t.Helper()
	path := filepath.Join(workspace, ".aicli", "profile")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	return path
}

// projectProfilePath 在 <workspace>/.aicli/profiles/<name> 落一个最小可用 profile。
func projectProfilePath(t *testing.T, workspace, name string) string {
	t.Helper()
	root := filepath.Join(workspace, ".aicli", "profiles", name)
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", root, err)
	}
	content := "profile:\n  name: " + name + "\n"
	if err := os.WriteFile(filepath.Join(root, "profile.yaml"), []byte(content), 0o644); err != nil {
		t.Fatalf("write profile.yaml: %v", err)
	}
	return root
}

func TestLoadProjectProfileBindingAbsent(t *testing.T) {
	workspace := t.TempDir()

	binding, err := LoadProjectProfileBinding(workspace)
	if err != nil {
		t.Fatalf("缺失绑定文件不是错误，got %v", err)
	}
	if binding.Present || binding.Valid {
		t.Fatalf("无绑定文件必须 present=false valid=false，got %+v", binding)
	}
	if binding.Workspace != filepath.Clean(workspace) {
		t.Fatalf("Workspace = %q, want %q", binding.Workspace, filepath.Clean(workspace))
	}
	if binding.Path != filepath.Join(filepath.Clean(workspace), ".aicli", "profile") {
		t.Fatalf("Path = %q", binding.Path)
	}
	if binding.Layer != "project" || binding.Source != "project_binding" {
		t.Fatalf("来源标注不正确：%+v", binding)
	}
	if binding.Error != "" {
		t.Fatalf("无绑定文件不得制造错误：%q", binding.Error)
	}

	// 只有 `.aicli/profile` 是指针文件：旁边的 profile.yaml 与 profiles/ 目录
	// 都不构成绑定（同目录多口径会让"绑定在哪"变成猜谜）。
	if err := os.MkdirAll(filepath.Join(workspace, ".aicli"), 0o755); err != nil {
		t.Fatalf("mkdir .aicli: %v", err)
	}
	if err := os.WriteFile(filepath.Join(workspace, ".aicli", "profile.yaml"), []byte("profile: coding\n"), 0o644); err != nil {
		t.Fatalf("write sibling profile.yaml: %v", err)
	}
	other, err := LoadProjectProfileBinding(workspace)
	if err != nil {
		t.Fatalf("LoadProjectProfileBinding: %v", err)
	}
	if other.Present {
		t.Fatalf(".aicli/profile.yaml 不是绑定文件，got %+v", other)
	}
}

func TestLoadProjectProfileBindingRequiresUsableWorkspace(t *testing.T) {
	for _, workspace := range []string{"", "   "} {
		if _, err := LoadProjectProfileBinding(workspace); !errors.Is(err, ErrInvalidProjectProfileBindingWorkspace) {
			t.Fatalf("空 workspace 必须返回哨兵错误，got %v", err)
		}
	}
	if _, err := LoadProjectProfileBinding(filepath.Join(t.TempDir(), "missing")); !errors.Is(err, ErrInvalidProjectProfileBindingWorkspace) {
		t.Fatalf("不存在的工作区必须返回哨兵错误，got %v", err)
	}
	file := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatalf("write %s: %v", file, err)
	}
	if _, err := LoadProjectProfileBinding(file); !errors.Is(err, ErrInvalidProjectProfileBindingWorkspace) {
		t.Fatalf("工作区是文件时必须返回哨兵错误，got %v", err)
	}
}

func TestLoadProjectProfileBindingValid(t *testing.T) {
	workspace := t.TempDir()
	root := projectProfilePath(t, workspace, "coding")
	writeProjectBindingFixture(t, workspace, "profile: coding\n")

	binding, err := LoadProjectProfileBinding(workspace)
	if err != nil {
		t.Fatalf("LoadProjectProfileBinding: %v", err)
	}
	if !binding.Present || !binding.Valid {
		t.Fatalf("合法绑定必须 present+valid：%+v", binding)
	}
	if binding.Ref != "coding" {
		t.Fatalf("Ref = %q, want coding", binding.Ref)
	}
	if binding.Root != root {
		t.Fatalf("Root = %q, want %q", binding.Root, root)
	}
	// 允许多余空白：ref 前后空格 TrimSpace，但内部空白仍交给 ValidateProfileName 拒绝。
	writeProjectBindingFixture(t, workspace, "profile: \"  coding  \"\n")
	trimmed, err := LoadProjectProfileBinding(workspace)
	if err != nil {
		t.Fatalf("LoadProjectProfileBinding: %v", err)
	}
	if !trimmed.Valid || trimmed.Ref != "coding" {
		t.Fatalf("ref 两侧空白必须被清理：%+v", trimmed)
	}
}

func TestLoadProjectProfileBindingRejectsBadDocuments(t *testing.T) {
	cases := []struct {
		name    string
		content string
	}{
		{"空文档", ""},
		{"注释文档", "# 只有注释\n"},
		{"顶层非 mapping", "coding\n"},
		{"顶层是数组", "- coding\n"},
		{"缺少 profile 字段", "default: coding\n"},
		{"空 ref", "profile: \"\"\n"},
		{"数组值", "profile:\n  - coding\n"},
		{"嵌入 mapping", "profile:\n  name: coding\n"},
		{"未知字段", "profile: coding\nextra: 1\n"},
		{"重复字段", "profile: coding\nprofile: other\n"},
		{"非法 YAML", "profile: [unclosed\n"},
		{"多文档", "profile: coding\n---\nprofile: other\n"},
		{"非字符串标量", "profile: 12\n"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			workspace := t.TempDir()
			projectProfilePath(t, workspace, "coding")
			writeProjectBindingFixture(t, workspace, tc.content)

			binding, err := LoadProjectProfileBinding(workspace)
			if err != nil {
				t.Fatalf("文档错误不得升级成 workspace 错误：%v", err)
			}
			if !binding.Present {
				t.Fatalf("存在指针文件必须 present=true：%+v", binding)
			}
			if binding.Valid {
				t.Fatalf("非法文档必须 valid=false：%+v", binding)
			}
			if binding.Error == "" {
				t.Fatalf("非法文档必须给出可显示的错误：%+v", binding)
			}
		})
	}
}

func TestLoadProjectProfileBindingRejectsUnsafeRefs(t *testing.T) {
	cases := []string{
		".",
		"..",
		"../outside",
		"nested/profile",
		`nested\profile`,
		"/abs/profile",
		`C:\profiles\coding`,
		"c:/profiles/coding",
		`\\server\share\coding`,
		"//server/share",
		"user:coding",
		`C:coding`,
		"bad name",
		"..hidden",
	}
	for _, ref := range cases {
		ref := ref
		t.Run(ref, func(t *testing.T) {
			workspace := t.TempDir()
			projectProfilePath(t, workspace, "coding")
			// 目标目录确实存在（含遍历形状的假目标），校验必须发生在 Join 之前，
			// 不靠"目标恰好不在"来兜底。单引号是 YAML 字面标量：反斜杠不被转义，
			// 保证断言的是 ref 校验而不是 YAML 转义规则。
			writeProjectBindingFixture(t, workspace, "profile: '"+ref+"'\n")

			binding, err := LoadProjectProfileBinding(workspace)
			if err != nil {
				t.Fatalf("ref 非法是绑定内容错误，不该升级成 workspace 错误：%v", err)
			}
			if binding.Valid {
				t.Fatalf("非法 ref 必须被拒绝：%+v", binding)
			}
			if binding.Error == "" || binding.Ref != "" {
				t.Fatalf("非法 ref 必须给出错误且不回填 ref：%+v", binding)
			}
		})
	}
}

// 目标缺失（或不是文件）时必须如实报错，且**不得**去 user/config/default 找同名 profile。
func TestLoadProjectProfileBindingTargetMissingDoesNotFallBack(t *testing.T) {
	home := redirectUserLayerHome(t)
	workspace := t.TempDir()
	// user 层放同名 profile：任何回退都会命中它，从而让本用例失败。
	writeLayerProfileFixture(t, filepath.Join(home, ".aicli", "profiles"), "coding")
	writeProjectBindingFixture(t, workspace, "profile: coding\n")

	binding, err := LoadProjectProfileBinding(workspace)
	if err != nil {
		t.Fatalf("LoadProjectProfileBinding: %v", err)
	}
	if !binding.Present || binding.Valid {
		t.Fatalf("目标缺失必须 present=true valid=false：%+v", binding)
	}
	if binding.Ref != "coding" {
		t.Fatalf("Ref 应保留供展示：%+v", binding)
	}
	wantRoot := filepath.Join(workspace, ".aicli", "profiles", "coding")
	if binding.Root != wantRoot {
		t.Fatalf("Root 必须是本工作区项目层路径（不得回退 user）：%q want %q", binding.Root, wantRoot)
	}
	if !strings.Contains(binding.Error, "不存在") {
		t.Fatalf("错误必须说明目标缺失：%q", binding.Error)
	}

	// profile.yaml 被同名目录占位：也不是可用目标。
	if err := os.MkdirAll(filepath.Join(wantRoot, "profile.yaml"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	dirTarget, err := LoadProjectProfileBinding(workspace)
	if err != nil {
		t.Fatalf("LoadProjectProfileBinding: %v", err)
	}
	if dirTarget.Valid || dirTarget.Error == "" {
		t.Fatalf("目标不是文件必须报错：%+v", dirTarget)
	}
}

// 两个工作区各自独立：同名 profile 不串用，绑定只解析到自己工作区的项目层。
func TestLoadProjectProfileBindingKeepsWorkspacesSeparate(t *testing.T) {
	workspaceA := t.TempDir()
	workspaceB := t.TempDir()
	rootA := projectProfilePath(t, workspaceA, "shared")
	rootB := projectProfilePath(t, workspaceB, "shared")
	writeProjectBindingFixture(t, workspaceA, "profile: shared\n")
	writeProjectBindingFixture(t, workspaceB, "profile: shared\n")

	bindingA, err := LoadProjectProfileBinding(workspaceA)
	if err != nil {
		t.Fatalf("LoadProjectProfileBinding(A): %v", err)
	}
	bindingB, err := LoadProjectProfileBinding(workspaceB)
	if err != nil {
		t.Fatalf("LoadProjectProfileBinding(B): %v", err)
	}
	if bindingA.Root != rootA || bindingB.Root != rootB {
		t.Fatalf("两个工作区必须各自解析：A=%q B=%q", bindingA.Root, bindingB.Root)
	}
	if bindingA.Workspace == bindingB.Workspace {
		t.Fatalf("Workspace 不应相同：%q", bindingA.Workspace)
	}
	// 相对路径也要归一化成绝对路径（调用方可能直接拿 Workspace 做展示/比较）。
	// 相对路径按**进程 cwd** 解析（filepath.Abs 语义），因此先把 cwd 挪到父目录。
	t.Chdir(filepath.Dir(workspaceA))
	fromRelative, err := LoadProjectProfileBinding(filepath.Base(workspaceA))
	if err != nil {
		t.Fatalf("LoadProjectProfileBinding(relative): %v", err)
	}
	if !strings.EqualFold(fromRelative.Workspace, filepath.Clean(workspaceA)) {
		t.Fatalf("相对 workspace 必须归一化为绝对：%q want %q", fromRelative.Workspace, filepath.Clean(workspaceA))
	}
}
