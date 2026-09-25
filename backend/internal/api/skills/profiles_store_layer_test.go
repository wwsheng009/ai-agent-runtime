package skills

import (
	"path/filepath"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	profilesys "github.com/wwsheng009/ai-agent-runtime/internal/profile"
)

// 回归（与 TUI `/profile` 同源缺陷）：create/move/import 把 profile 落在标准层根，
// 而 API 的清单与 {ref} 解析只认 config 注册项与 default root。结果是
// "创建成功 → 清单里查不到、set_profile 报未找到"的写读分叉。
func TestRuntimeProfileLayerDiscovery(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	workspace := t.TempDir()
	t.Chdir(workspace)

	layerRoot := filepath.Join(home, ".aicli", "profiles", "layer-only")
	writeProfileGateFile(t, filepath.Join(layerRoot, "profile.yaml"), "profile:\n  name: layer-only\n  default_agent: coder\n")
	writeProfileGateFile(t, filepath.Join(layerRoot, "agents", "coder", "prompts", "system.md"), "Layer prompt.")

	handler := &Handler{}
	handler.SetProfileSupport(ProfileSupportConfig{
		Registry: profilesys.NewRegistryFromProfilesConfig(&agentconfig.ProfilesConfig{
			Root: filepath.Join(workspace, "profiles"),
		}),
	})

	list, err := handler.listRuntimeProfileEntries("")
	if err != nil {
		t.Fatalf("listRuntimeProfileEntries: %v", err)
	}
	var found *runtimeProfileEntry
	for index := range list.Profiles {
		if list.Profiles[index].Name == "layer-only" {
			found = &list.Profiles[index]
			break
		}
	}
	if found == nil {
		t.Fatalf("层 profile 必须出现在清单里，got %+v", list.Profiles)
	}
	if found.Source != "layer" || found.Layer != "user" || found.Path != layerRoot || !found.Valid {
		t.Fatalf("层清单条目不正确：%+v", *found)
	}

	target, err := handler.resolveRuntimeProfileTarget("layer-only")
	if err != nil {
		t.Fatalf("resolveRuntimeProfileTarget: %v", err)
	}
	if target.Root != layerRoot || target.Source != "layer" || target.Layer != "user" {
		t.Fatalf("层引用解析不正确：%+v", target)
	}
}

// 会话解析（set_profile 执行核心）必须能命中标准层根：清单里可见的名字不能在
// 解析时"未找到"（写读分叉）。project 层按会话工作区解析，user 层按用户目录。
func TestFallbackProfileRootFindsStandardLayers(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	workspace := t.TempDir()

	userProfile := filepath.Join(home, ".aicli", "profiles", "user-layer")
	writeProfileGateFile(t, filepath.Join(userProfile, "profile.yaml"), "profile:\n  name: user-layer\n")
	projectProfile := filepath.Join(workspace, ".aicli", "profiles", "project-layer")
	writeProfileGateFile(t, filepath.Join(projectProfile, "profile.yaml"), "profile:\n  name: project-layer\n")
	// `.gagent` 是历史目录约定，仍然优先（既有行为不回退）。
	legacyProfile := filepath.Join(workspace, ".gagent", "profiles", "legacy")
	writeProfileGateFile(t, filepath.Join(legacyProfile, "profile.yaml"), "profile:\n  name: legacy\n")

	if got := fallbackProfileRoot("user-layer", workspace); got != userProfile {
		t.Fatalf("user 层兜底 = %q, want %q", got, userProfile)
	}
	if got := fallbackProfileRoot("project-layer", workspace); got != projectProfile {
		t.Fatalf("project 层兜底 = %q, want %q", got, projectProfile)
	}
	if got := fallbackProfileRoot("legacy", workspace); got != legacyProfile {
		t.Fatalf(".gagent 兜底 = %q, want %q", got, legacyProfile)
	}
	if got := fallbackProfileRoot("missing", workspace); got != "" {
		t.Fatalf("未知名必须返回空（不猜），got %q", got)
	}
}
