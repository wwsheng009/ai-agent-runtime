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
