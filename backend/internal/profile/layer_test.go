package profile

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
)

// redirectUserLayerHome 把 user 层根（LayerRoot 走 os.UserHomeDir）重定向到临时目录，
// 让层发现测试不依赖开发机 ~/.aicli/profiles 的真实内容。
func redirectUserLayerHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	return home
}

// writeLayerProfileFixture 在 layerRoot/<name> 生成带 profile.yaml 的 profile 目录。
func writeLayerProfileFixture(t *testing.T, layerRoot, name string) string {
	t.Helper()
	root := filepath.Join(layerRoot, name)
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", root, err)
	}
	content := "profile:\n  name: " + name + "\n"
	if err := os.WriteFile(filepath.Join(root, "profile.yaml"), []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", filepath.Join(root, "profile.yaml"), err)
	}
	return root
}

func TestLayerProfilesDiscoversUserAndProjectLayers(t *testing.T) {
	redirectUserLayerHome(t)
	t.Chdir(t.TempDir())

	userRoot, err := LayerRoot("user")
	if err != nil {
		t.Fatalf("LayerRoot(user): %v", err)
	}
	projectRoot, err := LayerRoot("project")
	if err != nil {
		t.Fatalf("LayerRoot(project): %v", err)
	}

	projectProfile := writeLayerProfileFixture(t, projectRoot, "project-only")
	userProfile := writeLayerProfileFixture(t, userRoot, "user-only")
	// 没有 profile.yaml 的目录不是 profile（与 `profile list`/API 清单同一判据）。
	if err := os.MkdirAll(filepath.Join(userRoot, "not-a-profile"), 0o755); err != nil {
		t.Fatalf("mkdir not-a-profile: %v", err)
	}

	got := LayerProfiles()
	want := []LayerProfile{
		{Layer: "project", Name: "project-only", Root: projectProfile},
		{Layer: "user", Name: "user-only", Root: userProfile},
	}
	if len(got) != len(want) {
		t.Fatalf("LayerProfiles() = %+v, want %+v", got, want)
	}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("LayerProfiles()[%d] = %+v, want %+v", index, got[index], want[index])
		}
	}

	if layer := LayerForRoot(userProfile); layer != "user" {
		t.Fatalf("LayerForRoot(user profile) = %q, want user", layer)
	}
	if layer := LayerForRoot(projectRoot); layer != "project" {
		t.Fatalf("LayerForRoot(层根自身) = %q, want project", layer)
	}
	if layer := LayerForRoot(t.TempDir()); layer != "" {
		t.Fatalf("LayerForRoot(非层目录) = %q, want 空", layer)
	}
}

// 服务端的 project 层按会话工作区解析（进程 cwd 不等于工作区）；user 层与 LayerRoot 同源。
func TestLayerRootForWorkspace(t *testing.T) {
	redirectUserLayerHome(t)
	t.Chdir(t.TempDir())

	workspace := t.TempDir()
	projectRoot, err := LayerRootForWorkspace("project", workspace)
	if err != nil {
		t.Fatalf("LayerRootForWorkspace(project): %v", err)
	}
	if want := filepath.Join(workspace, ".aicli", "profiles"); projectRoot != want {
		t.Fatalf("project 层根 = %s, want %s", projectRoot, want)
	}
	if _, err := LayerRootForWorkspace("project", "  "); err == nil {
		t.Fatal("空工作区必须报错（不静默回退 cwd）")
	}
	userRoot, err := LayerRootForWorkspace("user", "")
	if err != nil {
		t.Fatalf("LayerRootForWorkspace(user): %v", err)
	}
	wantUserRoot, err := LayerRoot("user")
	if err != nil {
		t.Fatalf("LayerRoot(user): %v", err)
	}
	if userRoot != wantUserRoot {
		t.Fatalf("user 层根 = %s, want %s", userRoot, wantUserRoot)
	}
}

// 同名去重顺序即优先级：config 注册项 > profiles.root 下可用目录 > project 层 > user 层。
func TestRegisterLayerFallbacksPrecedence(t *testing.T) {
	redirectUserLayerHome(t)
	t.Chdir(t.TempDir())

	userRoot, err := LayerRoot("user")
	if err != nil {
		t.Fatalf("LayerRoot(user): %v", err)
	}
	projectRoot, err := LayerRoot("project")
	if err != nil {
		t.Fatalf("LayerRoot(project): %v", err)
	}

	// 两层同名 → project 生效；user 层独有名 → user 生效。
	projectShared := writeLayerProfileFixture(t, projectRoot, "shared")
	writeLayerProfileFixture(t, userRoot, "shared")
	userOnly := writeLayerProfileFixture(t, userRoot, "user-only")
	// config 注册项与 profiles.root 的同名目录必须压过层发现。
	registeredRoot := writeLayerProfileFixture(t, t.TempDir(), "registered")
	writeLayerProfileFixture(t, userRoot, "registered")
	profilesRoot := t.TempDir()
	rootedRoot := writeLayerProfileFixture(t, profilesRoot, "rooted")
	writeLayerProfileFixture(t, userRoot, "rooted")

	cfg := &agentconfig.ProfilesConfig{
		Root:  profilesRoot,
		Items: map[string]agentconfig.ProfileConfig{"registered": {Root: registeredRoot}},
	}
	registry := NewRegistryFromProfilesConfig(cfg)
	added := RegisterLayerFallbacks(registry)

	addedNames := make([]string, 0, len(added))
	for _, item := range added {
		addedNames = append(addedNames, item.Name)
	}
	wantAdded := []string{"shared", "user-only"}
	if len(addedNames) != len(wantAdded) {
		t.Fatalf("RegisterLayerFallbacks 新增 = %v, want %v", addedNames, wantAdded)
	}
	for index := range wantAdded {
		if addedNames[index] != wantAdded[index] {
			t.Fatalf("RegisterLayerFallbacks 新增[%d] = %q, want %q", index, addedNames[index], wantAdded[index])
		}
	}

	for name, wantRoot := range map[string]string{
		"registered": registeredRoot,
		"rooted":     rootedRoot,
		"shared":     projectShared,
		"user-only":  userOnly,
	} {
		root, err := registry.Resolve(name)
		if err != nil {
			t.Fatalf("Resolve(%s): %v", name, err)
		}
		if root != wantRoot {
			t.Fatalf("Resolve(%s) = %s, want %s", name, root, wantRoot)
		}
	}
}

// 服务端侧：project 层按**会话工作区**枚举，user 层不受影响；workspace 为空时
// 退化为既有 LayerProfiles()（project 层按进程 cwd），保持旧行为零变化。
func TestLayerProfilesForWorkspace(t *testing.T) {
	redirectUserLayerHome(t)
	cwd := t.TempDir()
	t.Chdir(cwd)

	userRoot, err := LayerRoot("user")
	if err != nil {
		t.Fatalf("LayerRoot(user): %v", err)
	}
	cwdProjectRoot, err := LayerRoot("project")
	if err != nil {
		t.Fatalf("LayerRoot(project): %v", err)
	}

	userProfile := writeLayerProfileFixture(t, userRoot, "user-only")
	cwdProfile := writeLayerProfileFixture(t, cwdProjectRoot, "cwd-only")
	workspace := t.TempDir()
	workspaceRoot, err := LayerRootForWorkspace("project", workspace)
	if err != nil {
		t.Fatalf("LayerRootForWorkspace: %v", err)
	}
	workspaceProfile := writeLayerProfileFixture(t, workspaceRoot, "ws-only")

	got := LayerProfilesForWorkspace(workspace)
	want := []LayerProfile{
		{Layer: "project", Name: "ws-only", Root: workspaceProfile},
		{Layer: "user", Name: "user-only", Root: userProfile},
	}
	if len(got) != len(want) {
		t.Fatalf("LayerProfilesForWorkspace(%s) = %+v, want %+v", workspace, got, want)
	}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("LayerProfilesForWorkspace[%d] = %+v, want %+v", index, got[index], want[index])
		}
	}
	for _, profile := range got {
		if profile.Name == "cwd-only" {
			t.Fatalf("工作区枚举不得混入进程 cwd 的项目层 profile：%+v", got)
		}
	}

	// 空工作区：退化为既有 LayerProfiles()（含 cwd 项目层），不报错、不猜工作区。
	fallback := LayerProfilesForWorkspace("   ")
	found := false
	for _, profile := range fallback {
		if profile.Name == "cwd-only" && profile.Root == cwdProfile {
			found = true
		}
	}
	if !found {
		t.Fatalf("空工作区必须退化为 LayerProfiles()：%+v", fallback)
	}
}
