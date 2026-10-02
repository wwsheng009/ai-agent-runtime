package commands

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/functions"
	config "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	runtimebootstrap "github.com/wwsheng009/ai-agent-runtime/internal/bootstrap"
	runtimecfg "github.com/wwsheng009/ai-agent-runtime/internal/config"
)

func writeHotReloadChatTestSkill(t *testing.T, root, name, description string) {
	t.Helper()
	dir := filepath.Join(root, name)
	require.NoError(t, os.MkdirAll(dir, 0o755))
	content := "---\nname: " + name + "\ndescription: " + description + "\n---\n\nbody\n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(content), 0o644))
}

// 运行中的 aicli chat：动态安装一个 skill（新建目录 + SKILL.md）后，无需重启
// 即可进入会话函数面。覆盖 DiscoverOnly + EnableHotReload 的目录监听、事件
// 防抖刷新与 refreshSkillsRuntimeBinding 的整条链路。
func TestInitSkillFunctionsWithManager_HotReloadPicksUpNewSkill(t *testing.T) {
	tempDir := t.TempDir()
	chdirTest(t, tempDir)
	// 隔离用户级目录，避免真实 HOME 下的 skills 混入计数。
	home := t.TempDir()
	t.Setenv("USERPROFILE", home)
	t.Setenv("HOME", home)
	// 隔离进程级 MCP 兜底，避免其它测试的全局状态改变 MCP 来源。
	previousMCP := MCPManagerInstance
	MCPManagerInstance = nil
	t.Cleanup(func() { MCPManagerInstance = previousMCP })

	skillRoot := filepath.Join(tempDir, ".agents", "skills")
	require.NoError(t, os.MkdirAll(skillRoot, 0o755))
	writeHotReloadChatTestSkill(t, skillRoot, "seed-skill", "seed")

	runtimeConfig := runtimecfg.DefaultRuntimeConfig()
	runtimeConfig.HotReload.Enabled = true
	shared, err := runtimebootstrap.NewManager(&runtimebootstrap.Options{
		Config:          runtimeConfig,
		SkillDir:        skillRoot,
		DiscoverOnly:    true,
		EnableHotReload: true,
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = shared.Stop() })
	require.NotNil(t, shared.HotReload())

	cfg := &config.Config{
		SkillsRuntime: &config.SkillsRuntimeConfig{Enabled: true, SkillDir: skillRoot},
	}
	session := &ChatSession{
		ProviderName:     "nvidia",
		Model:            "test-model",
		FunctionRegistry: functions.NewFunctionRegistry(),
		FunctionCatalog:  newAICLIFunctionCatalog("openai", nil),
		Config:           cfg,
	}
	binding, err := initSkillFunctionsWithManager(cfg, session, nil, shared, nil, 0, "")
	require.NoError(t, err)
	require.NotNil(t, binding)
	require.NotNil(t, session.SkillsBinding)
	require.Equal(t, 1, session.FunctionCatalog.Stats().SkillFunctions,
		"seed skill must be present in the session function surface")

	// 运行中安装新 skill：只写文件，不触碰任何进程内状态。
	writeHotReloadChatTestSkill(t, skillRoot, "runtime-skill", "installed while running")

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if session.FunctionCatalog.Stats().SkillFunctions >= 2 {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("newly installed skill did not reach the session function surface without restart (stats=%+v)",
		session.FunctionCatalog.Stats())
}

// 新工作区（启动时一个技能目录都不存在）：第一次安装 skill 也必须热加载。
// 覆盖空集合 binding + StartEmpty/WatchRoots 候选监听 + 安装后函数面重建。
func TestInitSkillFunctionsWithManager_HotReloadPicksUpFirstInstall(t *testing.T) {
	tempDir := t.TempDir()
	chdirTest(t, tempDir)
	home := t.TempDir()
	t.Setenv("USERPROFILE", home)
	t.Setenv("HOME", home)
	previousMCP := MCPManagerInstance
	MCPManagerInstance = nil
	t.Cleanup(func() { MCPManagerInstance = previousMCP })

	cfg := &config.Config{
		SkillsRuntime: &config.SkillsRuntimeConfig{Enabled: true},
	}
	session := &ChatSession{
		ProviderName:     "nvidia",
		Model:            "test-model",
		FunctionRegistry: functions.NewFunctionRegistry(),
		FunctionCatalog:  newAICLIFunctionCatalog("openai", nil),
		Config:           cfg,
	}

	runtimeConfig := runtimecfg.DefaultRuntimeConfig()
	runtimeConfig.HotReload.Enabled = true
	shared, err := runtimebootstrap.NewManager(&runtimebootstrap.Options{
		Config:          runtimeConfig,
		DiscoverOnly:    true,
		EnableHotReload: true,
		WatchSkillDirs:  resolveChatSkillWatchDirs(cfg, session),
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = shared.Stop() })
	require.NotNil(t, shared.HotReload(), "empty workspace must still keep hot reload capability")

	binding, err := initSkillFunctionsWithManager(cfg, session, nil, shared, nil, 0, "")
	require.NoError(t, err)
	require.NotNil(t, binding, "empty workspace with hot reload must keep an empty skills binding")
	require.Equal(t, 0, session.FunctionCatalog.Stats().SkillFunctions)

	// 第一次安装：创建标准安装位并写入 SKILL.md。
	skillRoot := filepath.Join(tempDir, ".agents", "skills")
	writeHotReloadChatTestSkill(t, skillRoot, "first-install", "installed into a brand new workspace")

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if session.FunctionCatalog.Stats().SkillFunctions >= 1 {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("first install did not reach the session function surface without restart (stats=%+v)",
		session.FunctionCatalog.Stats())
}
