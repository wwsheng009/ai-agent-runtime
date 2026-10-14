package skill

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHotReload_ReloadAllSkills_RegistersDiscoveryStubs(t *testing.T) {
	loader := NewLoader(nil)
	registry := NewRegistry(nil)
	hotReload, err := NewHotReload(loader, registry)
	require.NoError(t, err)
	t.Cleanup(func() { _ = hotReload.Stop() })

	skillDir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(skillDir, "skill.yaml"), []byte(`name: hot-lazy
description: hot lazy reload
triggers:
  - type: keyword
    values: ["hot"]
    weight: 1
`), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(skillDir, "prompt.md"), []byte("You are hot lazy."), 0o644))

	hotReload.skillDirs = []string{skillDir}
	require.NoError(t, hotReload.reloadAllSkills([]string{skillDir}))

	item, ok := registry.Get("hot-lazy")
	require.True(t, ok)
	require.NotNil(t, item)
	require.NotNil(t, item.Source)
	assert.True(t, item.Source.DiscoveryOnly)
	assert.Equal(t, filepath.Join(skillDir, "prompt.md"), item.Source.PromptPath)
	assert.Equal(t, "", item.SystemPrompt)
	assert.Equal(t, "", item.UserPrompt)
}

// 回归：全量重载（chat/runtime-server 启用热加载后的启动路径）扫描到声明了
// 当前工具面未注册工具的 skill 时，必须与 loader 的 discovery 软失败路径一致：
// 登记 unavailable 记录并跳过，而不是让整次 Reload / 能力面初始化失败。
// 触发场景：未启用 images_generations provider 时的 imagegen/openai_image_generate。
func TestHotReload_ReloadAllSkills_MissingToolSoftFails(t *testing.T) {
	mcp := newFakeSkillsMCPManager() // 空工具面：openai_image_generate 未注册
	loader := NewLoader(mcp)
	registry := NewRegistry(mcp)
	hotReload, err := NewHotReload(loader, registry)
	require.NoError(t, err)
	t.Cleanup(func() { _ = hotReload.Stop() })

	skillDir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(skillDir, "skill.yaml"), []byte(`name: imagegen
description: generate an image
triggers:
  - type: keyword
    values: ["imagegen"]
    weight: 1
tools:
  - openai_image_generate
`), 0o644))

	require.NoError(t, hotReload.reloadAllSkills([]string{skillDir}),
		"missing-tool skills must be soft-skipped, not abort the whole reload")

	if _, ok := registry.Get("imagegen"); ok {
		t.Fatalf("skill with missing tool must not be registered")
	}
	unavailable := registry.UnavailableSkills()
	require.Len(t, unavailable, 1)
	assert.Equal(t, "imagegen", unavailable[0].Name)
	assert.Contains(t, unavailable[0].MissingTools, "openai_image_generate")
}

// 回归：软失败只覆盖「工具缺失」；真实校验错误（缺少名称/描述）仍必须上报，
// 不能被热重载路径吞掉。
func TestHotReload_RegisterDiscoveredSkill_RealValidationErrorStillReturns(t *testing.T) {
	mcp := newFakeSkillsMCPManager("fetch")
	registry := NewRegistry(mcp)
	hotReload, err := NewHotReload(NewLoader(mcp), registry)
	require.NoError(t, err)
	t.Cleanup(func() { _ = hotReload.Stop() })

	if _, err := hotReload.registerDiscoveredSkill(&Skill{Description: "missing name", Tools: []string{"fetch"}}); err == nil {
		t.Fatalf("expected validation error for skill without name, got nil")
	}
}

func TestHotReload_ReloadSkill_RegistersDiscoveryStub(t *testing.T) {
	loader := NewLoader(nil)
	registry := NewRegistry(nil)
	hotReload, err := NewHotReload(loader, registry)
	require.NoError(t, err)
	t.Cleanup(func() { _ = hotReload.Stop() })

	skillDir := t.TempDir()
	manifestPath := filepath.Join(skillDir, "skill.yaml")
	require.NoError(t, os.WriteFile(manifestPath, []byte(`name: hot-single
description: hot single reload
triggers:
  - type: keyword
    values: ["hot"]
    weight: 1
`), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(skillDir, "prompt.md"), []byte("You are hot single."), 0o644))

	hotReload.skillDirs = []string{skillDir}
	hotReload.reloadSkill(manifestPath)

	item, ok := registry.Get("hot-single")
	require.True(t, ok)
	require.NotNil(t, item)
	require.NotNil(t, item.Source)
	assert.True(t, item.Source.DiscoveryOnly)
	assert.Equal(t, filepath.Join(skillDir, "prompt.md"), item.Source.PromptPath)
	assert.Equal(t, "", item.SystemPrompt)
}

func TestShouldParseSkillManifest_IgnoresCodexResourceDirectories(t *testing.T) {
	workspace := t.TempDir()
	assert.False(t, shouldParseSkillManifest(filepath.Join(workspace, "scripts", "helper.yaml")))
	assert.False(t, shouldParseSkillManifest(filepath.Join(workspace, "references", "doc.yml")))
	assert.False(t, shouldParseSkillManifest(filepath.Join(workspace, "assets", "data.yaml")))
	assert.False(t, shouldParseSkillManifest(filepath.Join(workspace, "agents", "openai.yaml")))
	assert.True(t, shouldParseSkillManifest(filepath.Join(workspace, "skill.yaml")))
	assert.True(t, shouldParseSkillManifest(filepath.Join(workspace, "system.yaml")))
	assert.True(t, shouldParseSkillManifest(filepath.Join(workspace, "extra.yml")))
}

func TestSkillManifestPathForWatchedFile_RecognizesLegacyCustomManifests(t *testing.T) {
	dir := t.TempDir()
	systemManifest := filepath.Join(dir, "system.yaml")
	extraManifest := filepath.Join(dir, "extra.yml")
	require.NoError(t, os.WriteFile(systemManifest, []byte(`name: watched-system
description: watched system skill
triggers:
  - type: keyword
    values: ["watch"]
    weight: 1
`), 0o644))
	require.NoError(t, os.WriteFile(extraManifest, []byte(`name: watched-extra
description: watched extra skill
triggers:
  - type: keyword
    values: ["watch"]
    weight: 1
`), 0o644))

	manifestPath, kind := skillManifestPathForWatchedFile(filepath.Join(dir, "prompt.md"))
	require.Equal(t, skillManifestKindCompanion, kind)
	assert.Equal(t, systemManifest, manifestPath)

	manifestPath, kind = skillManifestPathForWatchedFile(systemManifest)
	require.Equal(t, skillManifestKindManifest, kind)
	assert.Equal(t, systemManifest, manifestPath)

	manifestPath, kind = skillManifestPathForWatchedFile(extraManifest)
	require.Equal(t, skillManifestKindManifest, kind)
	assert.Equal(t, extraManifest, manifestPath)

	manifestPath, kind = skillManifestPathForWatchedFile(filepath.Join(dir, "scripts", "helper.yaml"))
	assert.Equal(t, "", manifestPath)
	assert.Equal(t, skillManifestKindUnknown, kind)
}

// 动态安装：运行中新建 skill 目录并写入 SKILL.md，无需重启即可注册；删除目录
// 后必须注销，避免 registry 残留幽灵技能。跨平台用例：Linux inotify 的目录
// 监听非递归，依赖目录补齐扫描兜住新目录里的事件。
func TestHotReload_DirectoryInstallAndRemove(t *testing.T) {
	loader := NewLoader(nil)
	registry := NewRegistry(nil)
	hotReload, err := NewHotReload(loader, registry)
	require.NoError(t, err)
	hotReload.SetDebounceTime(50 * time.Millisecond)

	skillDir := t.TempDir()
	require.NoError(t, hotReload.StartMany([]string{skillDir}))
	t.Cleanup(func() { _ = hotReload.Stop() })

	waitFor := func(assertion func() bool, message string) {
		t.Helper()
		deadline := time.Now().Add(8 * time.Second)
		for time.Now().Before(deadline) {
			if assertion() {
				return
			}
			time.Sleep(25 * time.Millisecond)
		}
		t.Fatalf("timed out: %s", message)
	}

	installed := filepath.Join(skillDir, "dynamic-skill")
	require.NoError(t, os.MkdirAll(installed, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(installed, "SKILL.md"),
		[]byte("---\nname: dynamic-skill\ndescription: installed while running\n---\n\nbody\n"), 0o644))

	waitFor(func() bool {
		item, ok := registry.Get("dynamic-skill")
		return ok && item != nil
	}, "newly installed skill must register without restart")

	require.NoError(t, os.RemoveAll(installed))
	waitFor(func() bool {
		_, ok := registry.Get("dynamic-skill")
		return !ok
	}, "removed skill directory must unregister its skills")
}

// 启动时尚不存在标准安装位（新工作区）：WatchRoots 监听最近已存在的祖先，
// 第一次安装（mkdir -p `<cwd>/.agents/skills/<name>` + SKILL.md）即注册，
// 不需要重启，也不需要在启动时预创建目录。
func TestHotReload_WatchRoots_PicksUpFirstInstall(t *testing.T) {
	loader := NewLoader(nil)
	registry := NewRegistry(nil)
	hotReload, err := NewHotReload(loader, registry)
	require.NoError(t, err)
	hotReload.SetDebounceTime(50 * time.Millisecond)

	workspace := t.TempDir()
	root := filepath.Join(workspace, ".agents", "skills")
	require.NoDirExists(t, root)

	require.NoError(t, hotReload.StartMany([]string{workspace}))
	hotReload.WatchRoots([]string{root})
	t.Cleanup(func() { _ = hotReload.Stop() })

	installed := filepath.Join(root, "first-install")
	require.NoError(t, os.MkdirAll(installed, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(installed, "SKILL.md"),
		[]byte("---\nname: first-install\ndescription: first install\n---\n\nbody\n"), 0o644))

	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		if item, ok := registry.Get("first-install"); ok && item != nil {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatal("first install into a previously missing skill root must hot reload")
}

// 回归（2026-10-14 itsm 实例）：目录批量创建后、清单文件隔一段时间才写入
//（超过防抖窗口），也必须热加载。现有测试都是"目录 + SKILL.md 同批写"，
// 覆盖不到"先建目录、后写清单"的真实安装形态。
// 覆盖链路：watch 候选（含尚不存在的 <cwd>/.agents/skills）→ pending 根
// 接管 / 目录创建补扫 → 补扫为子树挂监听 → 晚到的 SKILL.md 写事件。
func TestHotReload_WatchRoots_PicksUpDelayedManifestWrite(t *testing.T) {
	baseRoot := isolatedSkillTestRoot(t)
	homeRoot := filepath.Join(baseRoot, "home")
	workspace := filepath.Join(baseRoot, "workspace")
	require.NoError(t, os.MkdirAll(homeRoot, 0o755))
	require.NoError(t, os.MkdirAll(workspace, 0o755))
	t.Setenv("USERPROFILE", homeRoot)
	t.Setenv("HOME", homeRoot)

	loader := NewLoader(nil)
	registry := NewRegistry(nil)
	hotReload, err := NewHotReload(loader, registry)
	require.NoError(t, err)
	t.Cleanup(func() { _ = hotReload.Stop() })
	hotReload.SetDebounceTime(150 * time.Millisecond)
	require.NoError(t, hotReload.StartEmpty())
	hotReload.WatchRoots(DiscoverCodexCompatibleSkillWatchDirs(workspace, ""))

	// 阶段 1：批量创建目录树（真实安装的第一步），此刻没有清单文件。
	skillDir := filepath.Join(workspace, ".agents", "skills", "delayed-skill")
	require.NoError(t, os.MkdirAll(skillDir, 0o755))
	// 等待超过防抖：此窗口内所有一次性补齐扫描都会扑空（清单还不存在），
	// 后续必须依靠已被勾住的子树监听捕获写入。
	time.Sleep(600 * time.Millisecond)

	// 阶段 2：稍后写入 SKILL.md（itsm 实例中相隔数分钟）。
	require.NoError(t, os.WriteFile(filepath.Join(skillDir, "SKILL.md"),
		[]byte("---\nname: delayed-skill\ndescription: manifest written after a delay\n---\n\nbody\n"), 0o644))

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if item, ok := registry.Get("delayed-skill"); ok && item != nil {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("delayed SKILL.md write was not hot-loaded (summaries=%d)", len(registry.ListSummaries()))
}

// 补齐扫描（scheduleDirectoryScan / advancePendingRoots 路径）执行后，被扫描
// 子树的目录监听必须已经生效：之后才写入的清单文件要直接触发热加载，而不是
// 等待永不发生的下一次扫描（2026-10-14 itsm 实例的"目录在但永远不生效"）。
func TestHotReload_ReloadSkillsUnderDir_ArmsSubtreeWatchers(t *testing.T) {
	loader := NewLoader(nil)
	registry := NewRegistry(nil)
	hotReload, err := NewHotReload(loader, registry)
	require.NoError(t, err)
	t.Cleanup(func() { _ = hotReload.Stop() })
	hotReload.SetDebounceTime(150 * time.Millisecond)
	require.NoError(t, hotReload.StartEmpty())

	root := t.TempDir()
	skillDir := filepath.Join(root, "late-skill")
	require.NoError(t, os.MkdirAll(skillDir, 0o755))

	// 模拟"目录已批量创建、补齐扫描先行"：扫描时还没有清单文件。
	hotReload.reloadSkillsUnderDir(root)

	// 扫描之后才写完清单：子树监听必须已在位，写入必须能热加载。
	require.NoError(t, os.WriteFile(filepath.Join(skillDir, "SKILL.md"),
		[]byte("---\nname: late-skill\ndescription: manifest after scan\n---\n\nbody\n"), 0o644))

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if item, ok := registry.Get("late-skill"); ok && item != nil {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("manifest written after a directory scan must hot reload (subtree watchers not armed)")
}

// 回归（2026-10-14 itsm 实例，删除链路）：技能根目录被删除后，整表重载必须
// 跳过死路径继续重扫其余根，而不是整体失败（live 报
// "cannot find the path specified"，/skills reload 直接不可用）。
func TestHotReload_ReloadAllSkills_SkipsMissingDirs(t *testing.T) {
	loader := NewLoader(nil)
	registry := NewRegistry(nil)
	hotReload, err := NewHotReload(loader, registry)
	require.NoError(t, err)
	t.Cleanup(func() { _ = hotReload.Stop() })

	existingDir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(existingDir, "keep-skill"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(existingDir, "keep-skill", "SKILL.md"),
		[]byte("---\nname: keep-skill\ndescription: keep\n---\n\nbody\n"), 0o644))
	missingDir := filepath.Join(t.TempDir(), "deleted-root")

	require.NoError(t, hotReload.reloadAllSkills([]string{existingDir, missingDir}),
		"deleted root must not abort the whole reload")
	_, ok := registry.Get("keep-skill")
	require.True(t, ok, "remaining roots must still be rescanned")
}

// 回归（2026-10-14 itsm 实例，首装竞态）：批量 mkdir 的中间目录创建事件可能
// 在 watcher 生效前丢失，pending 候选根会卡住。补齐扫描执行点必须按磁盘现状
// 补接管（advancePendingRoots），让"第一次安装"不依赖事件时序。
func TestHotReload_ReloadSkillsUnderDir_AdoptsStuckPendingRoots(t *testing.T) {
	loader := NewLoader(nil)
	registry := NewRegistry(nil)
	hotReload, err := NewHotReload(loader, registry)
	require.NoError(t, err)
	t.Cleanup(func() { _ = hotReload.Stop() })
	hotReload.SetDebounceTime(150 * time.Millisecond)

	workspace := t.TempDir()
	root := filepath.Join(workspace, ".agents", "skills")
	require.NoError(t, hotReload.StartMany([]string{workspace}))
	hotReload.WatchRoots([]string{root})

	containsDir := func(dirs []string, want string) bool {
		for _, dir := range dirs {
			if filepath.Clean(dir) == filepath.Clean(want) {
				return true
			}
		}
		return false
	}
	require.False(t, containsDir(hotReload.SkillDirs(), root), "前置条件：根尚未被接管")

	// 模拟批量 mkdir 完成但事件未处理/丢失：子树就位，仍未被接管。
	skillDir := filepath.Join(root, "late-skill")
	require.NoError(t, os.MkdirAll(skillDir, 0o755))

	hotReload.reloadSkillsUnderDir(filepath.Join(workspace, ".agents"))
	require.True(t, containsDir(hotReload.SkillDirs(), root),
		"补齐扫描必须补接管卡住的 pending 根: %v", hotReload.SkillDirs())

	require.NoError(t, os.WriteFile(filepath.Join(skillDir, "SKILL.md"),
		[]byte("---\nname: late-skill\ndescription: late\n---\n\nbody\n"), 0o644))
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, ok := registry.Get("late-skill"); ok {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("skill under the adopted pending root must register")
}
