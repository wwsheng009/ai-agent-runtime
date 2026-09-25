package skills

import (
	"context"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/wwsheng009/ai-agent-runtime/internal/foldertrust"
)

// FR-14 第一阶段（项目绑定只读发现）的 API 半程。
//
// 纪律（与 docs/plan/fr14-profile-binding-implementation-plan-20260925.md §3.2 对齐）：
//   - 只有请求显式给出 workspace 时才读取绑定、才按该工作区枚举项目层；
//   - 绑定**文件**的问题（YAML/ref/目标缺失）是 200 + project_binding.error，
//     列表照常返回，不 fallback 到 user/default；
//   - workspace 参数本身不可用（不存在/不是目录）才是 400；
//   - 发现是只读的：不改 default、不建目录、不写会话。

// writeProjectBindingPointer 在 <workspace>/.aicli/profile 落一个指针文件。
func writeProjectBindingPointer(t *testing.T, workspace, content string) string {
	t.Helper()
	path := filepath.Join(workspace, ".aicli", "profile")
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
	return path
}

// writeProjectBindingProfile 在 <workspace>/.aicli/profiles/<name> 落一个最小 profile。
func writeProjectBindingProfile(t *testing.T, workspace, name string) string {
	t.Helper()
	root := filepath.Join(workspace, ".aicli", "profiles", name)
	writeProfileGateFile(t, filepath.Join(root, "profile.yaml"), "profile:\n  name: "+name+"\n")
	return root
}

func redirectBindingHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("AICLI_HOME", t.TempDir())
	return home
}

func profileListPayloadEntry(t *testing.T, payload map[string]interface{}, name string) map[string]interface{} {
	t.Helper()
	entries, ok := payload["profiles"].([]interface{})
	require.True(t, ok, "profiles 字段缺失：%v", payload)
	for _, raw := range entries {
		entry, _ := raw.(map[string]interface{})
		if entry["name"] == name {
			return entry
		}
	}
	return nil
}

func bindingWorkspacePath(workspace string) string {
	return "/api/runtime/profiles?workspace=" + url.QueryEscape(workspace)
}

// 合法绑定：metadata 与条目标注都要出现，且**不得**改动 default / 变成激活。
func TestRuntimeProfilesAPIReportsWorkspaceProjectBinding(t *testing.T) {
	redirectBindingHome(t)
	h := newProfilesAPIHarness(t)
	workspace := t.TempDir()
	targetRoot := writeProjectBindingProfile(t, workspace, "coding")
	pointer := writeProjectBindingPointer(t, workspace, "profile: coding\n")

	before, err := os.ReadFile(pointer)
	require.NoError(t, err)

	rec, payload := h.do(t, http.MethodGet, bindingWorkspacePath(workspace), nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	binding, ok := payload["project_binding"].(map[string]interface{})
	require.True(t, ok, "project_binding 缺失：%v", payload)
	assert.Equal(t, true, binding["present"])
	assert.Equal(t, true, binding["valid"])
	assert.Equal(t, "coding", binding["ref"])
	assert.Equal(t, "project_binding", binding["source"])
	assert.Equal(t, "project", binding["layer"])
	assert.Equal(t, targetRoot, binding["profile_root"])
	assert.Equal(t, pointer, binding["path"])
	assert.Equal(t, filepath.Clean(workspace), binding["workspace_path"])
	assert.Empty(t, binding["error"])

	entry := profileListPayloadEntry(t, payload, "coding")
	require.NotNil(t, entry, "绑定目标必须出现在清单里：%v", payload)
	assert.Equal(t, true, entry["is_bound"], "绑定目标必须标注 is_bound：%v", entry)
	assert.Equal(t, false, entry["is_default"], "绑定不是 default（发现不改语义）：%v", entry)
	assert.Equal(t, "project", entry["layer"])
	assert.Empty(t, payload["default_profile"], "发现绑定不得设置 default_profile")

	// 只读发现：不改指针文件、不建 profiles 之外的目录。
	after, err := os.ReadFile(pointer)
	require.NoError(t, err)
	assert.Equal(t, before, after, "发现不得改写绑定文件")
	entries, err := os.ReadDir(filepath.Join(workspace, ".aicli"))
	require.NoError(t, err)
	names := make([]string, 0, len(entries))
	for _, item := range entries {
		names = append(names, item.Name())
	}
	sort.Strings(names)
	assert.Equal(t, []string{"profile", "profiles"}, names, "发现不得在工作区里新建文件")

	// 不带 workspace：既有响应零变化（不读取、不回显绑定）。
	rec, plain := h.do(t, http.MethodGet, "/api/runtime/profiles", nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	_, hasBinding := plain["project_binding"]
	assert.False(t, hasBinding, "未声明工作区时不得回显绑定：%v", plain)
	_, hasWorkspace := plain["workspace_path"]
	assert.False(t, hasWorkspace, "未声明工作区时不得回显 workspace 上下文：%v", plain)
}

// 无绑定文件：present=false，不是错误；清单不标注任何条目。
func TestRuntimeProfilesAPIWithoutBindingIsNotAnError(t *testing.T) {
	redirectBindingHome(t)
	h := newProfilesAPIHarness(t)
	workspace := t.TempDir()
	writeProjectBindingProfile(t, workspace, "coding")

	rec, payload := h.do(t, http.MethodGet, bindingWorkspacePath(workspace), nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	binding, ok := payload["project_binding"].(map[string]interface{})
	require.True(t, ok, "workspace 声明时必须回显绑定状态：%v", payload)
	assert.Equal(t, false, binding["present"])
	assert.Equal(t, false, binding["valid"])
	assert.Empty(t, binding["error"], "没有绑定文件不是错误")
	entry := profileListPayloadEntry(t, payload, "coding")
	require.NotNil(t, entry)
	assert.NotEqual(t, true, entry["is_bound"], "没有绑定时不得标注 is_bound：%v", entry)
}

// 绑定文件内容非法：200 + valid=false + error；不静默忽略、不影响其余条目。
func TestRuntimeProfilesAPIBindingDocumentErrorsAreReported(t *testing.T) {
	redirectBindingHome(t)
	h := newProfilesAPIHarness(t)
	workspace := t.TempDir()
	writeProjectBindingProfile(t, workspace, "coding")
	writeProjectBindingPointer(t, workspace, "profile: [unclosed\n")

	rec, payload := h.do(t, http.MethodGet, bindingWorkspacePath(workspace), nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	binding, ok := payload["project_binding"].(map[string]interface{})
	require.True(t, ok, "project_binding 缺失：%v", payload)
	assert.Equal(t, true, binding["present"])
	assert.Equal(t, false, binding["valid"])
	assert.NotEmpty(t, binding["error"])
	assert.NotNil(t, profileListPayloadEntry(t, payload, "coding"), "绑定错误不得吞掉条目清单")
}

// 目标缺失时**不得**回退到 user 层同名 profile：清单里 user 条目仍在，但不被标注绑定。
func TestRuntimeProfilesAPIBindingTargetMissingDoesNotFallBack(t *testing.T) {
	home := redirectBindingHome(t)
	h := newProfilesAPIHarness(t)
	workspace := t.TempDir()
	// user 层同名 profile：任何回退都会让 binding.valid 变 true，从而让本用例失败。
	writeProfileGateFile(t,
		filepath.Join(home, ".aicli", "profiles", "coding", "profile.yaml"),
		"profile:\n  name: coding\n")
	writeProjectBindingPointer(t, workspace, "profile: coding\n")

	rec, payload := h.do(t, http.MethodGet, bindingWorkspacePath(workspace), nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	binding, ok := payload["project_binding"].(map[string]interface{})
	require.True(t, ok, "project_binding 缺失：%v", payload)
	assert.Equal(t, true, binding["present"])
	assert.Equal(t, false, binding["valid"], "目标缺失必须如实报告：%v", binding)
	assert.Contains(t, binding["error"], "不存在")
	assert.NotEqual(t, true, binding["valid"])

	entry := profileListPayloadEntry(t, payload, "coding")
	require.NotNil(t, entry, "user 层同名条目仍应可见：%v", payload)
	assert.Equal(t, "user", entry["layer"])
	assert.NotEqual(t, true, entry["is_bound"], "目标缺失时不得把别的层同名 profile 说成已绑定：%v", entry)
}

// workspace 参数本身不可用（路径不存在）→ 400（调用方输入错误，不是服务故障）。
func TestRuntimeProfilesAPIInvalidWorkspaceParameterIsBadRequest(t *testing.T) {
	redirectBindingHome(t)
	h := newProfilesAPIHarness(t)
	missing := filepath.Join(t.TempDir(), "does-not-exist")

	rec, payload := h.do(t, http.MethodGet, bindingWorkspacePath(missing), nil)
	require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	assert.NotEmpty(t, payload["error"])
}

// 两个工作区互不串用：清单只含本工作区的项目层 profile。
func TestRuntimeProfilesAPIWorkspaceIsolation(t *testing.T) {
	redirectBindingHome(t)
	h := newProfilesAPIHarness(t)
	workspaceA := t.TempDir()
	workspaceB := t.TempDir()
	writeProjectBindingProfile(t, workspaceA, "only-a")
	writeProjectBindingProfile(t, workspaceB, "only-b")
	writeProjectBindingPointer(t, workspaceA, "profile: only-a\n")
	writeProjectBindingPointer(t, workspaceB, "profile: only-b\n")

	_, listA := h.do(t, http.MethodGet, bindingWorkspacePath(workspaceA), nil)
	require.NotNil(t, profileListPayloadEntry(t, listA, "only-a"))
	assert.Nil(t, profileListPayloadEntry(t, listA, "only-b"), "A 的工作区清单不得含 B 的项目 profile")
	bindingA, ok := listA["project_binding"].(map[string]interface{})
	require.True(t, ok)
	assert.Equal(t, "only-a", bindingA["ref"])

	_, listB := h.do(t, http.MethodGet, bindingWorkspacePath(workspaceB), nil)
	require.NotNil(t, profileListPayloadEntry(t, listB, "only-b"))
	assert.Nil(t, profileListPayloadEntry(t, listB, "only-a"), "B 的工作区清单不得含 A 的项目 profile")
	bindingB, ok := listB["project_binding"].(map[string]interface{})
	require.True(t, ok)
	assert.Equal(t, "only-b", bindingB["ref"])
}

// D29：未信任工作区时绑定目标的 prompts 被扣留（绑定卡片与解析同源）；
// 一键信任后重载，扣留标记消失。
func TestRuntimeProfilesAPIBindingPromptSuppressionFollowsTrust(t *testing.T) {
	t.Setenv("AICLI_HOME", t.TempDir())
	t.Setenv(foldertrust.EnvFolderTrust, "1")
	redirectBindingHome(t)

	workspace := t.TempDir()
	harnessTrustTestWriteProjectProfile(t, workspace, "demo")
	writeProjectBindingPointer(t, workspace, "profile: demo\n")

	_, router := newHarnessTestRouter(t)

	listBefore := harnessTrustTestLoadProfiles(t, router, workspace)
	require.NotNil(t, listBefore.ProjectBinding, "workspace 声明时必须回显绑定")
	require.True(t, listBefore.ProjectBinding.Valid, "绑定目标应可用：%+v", listBefore.ProjectBinding)
	require.True(t, listBefore.ProjectBinding.PromptSuppressed,
		"未信任时绑定目标必须标注 prompts 扣留：%+v", listBefore.ProjectBinding)
	require.NotEmpty(t, listBefore.ProjectBinding.PromptSuppressionReason)
	require.True(t, harnessTrustTestFindProfile(t, listBefore, "demo").IsBound)

	grant := harnessTrustTestServe(t, router, http.MethodPost, "/api/runtime/harness/trust", map[string]any{
		"workspace_path": workspace,
		"action":         "grant",
	})
	require.Equal(t, http.StatusOK, grant.Code, grant.Body.String())

	listAfter := harnessTrustTestLoadProfiles(t, router, workspace)
	require.NotNil(t, listAfter.ProjectBinding)
	require.False(t, listAfter.ProjectBinding.PromptSuppressed,
		"信任后绑定目标不得再报扣留：%+v", listAfter.ProjectBinding)
	require.Empty(t, listAfter.ProjectBinding.PromptSuppressionReason)
	require.True(t, harnessTrustTestFindProfile(t, listAfter, "demo").IsBound)
}

// 「自动默认激活」未实现的反证：工作区里有绑定文件时，**未显式指定 profile** 的
// 解析路径仍然不落任何 profile——绑定只是候选，必须由用户显式发起（会话内 `/profile`）。
// 这条断言是 FR-14 分阶段决策的护栏：将来谁把绑定接进隐式链路，这里会先失败。
func TestRuntimeProfilesAPIBindingIsNotImplicitlyActivated(t *testing.T) {
	redirectBindingHome(t)
	h := newProfilesAPIHarness(t)
	workspace := t.TempDir()
	writeProjectBindingProfile(t, workspace, "coding")
	writeProjectBindingPointer(t, workspace, "profile: coding\n")

	state, cleanup, err := h.handler.resolveProfileRuntimeState(context.Background(), "", "", UsageScope{}, workspace)
	require.NoError(t, err)
	require.Nil(t, state, "绑定不得在未显式指定 profile 时被激活")
	require.Nil(t, cleanup)
}
