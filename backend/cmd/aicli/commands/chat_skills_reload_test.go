package commands

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/functions"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
	config "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	runtimebootstrap "github.com/wwsheng009/ai-agent-runtime/internal/bootstrap"
	runtimecfg "github.com/wwsheng009/ai-agent-runtime/internal/config"
)

type skillReloadTestEnv struct {
	session *ChatSession
	manager *runtimebootstrap.Manager
	root    string
}

// newSkillReloadTestEnv 构造 /skills reload 测试用的会话与 bootstrap manager。
// seed=true 时预置一个 SKILL.md 技能；hotReload 控制目录监听开关。
func newSkillReloadTestEnv(t *testing.T, hotReload bool, seed bool) *skillReloadTestEnv {
	t.Helper()
	tempDir := t.TempDir()
	home := t.TempDir()
	t.Setenv("USERPROFILE", home)
	t.Setenv("HOME", home)
	previousMCP := MCPManagerInstance
	MCPManagerInstance = nil
	t.Cleanup(func() { MCPManagerInstance = previousMCP })
	chdirTest(t, tempDir)

	cfg := &config.Config{SkillsRuntime: &config.SkillsRuntimeConfig{Enabled: true}}
	session := &ChatSession{
		ProviderName:     "nvidia",
		Model:            "test-model",
		FunctionRegistry: functions.NewFunctionRegistry(),
		FunctionCatalog:  newAICLIFunctionCatalog("openai", nil),
		Config:           cfg,
	}

	runtimeConfig := runtimecfg.DefaultRuntimeConfig()
	runtimeConfig.HotReload.Enabled = hotReload
	opts := &runtimebootstrap.Options{
		Config:          runtimeConfig,
		DiscoverOnly:    true,
		EnableHotReload: hotReload,
	}
	root := filepath.Join(tempDir, ".agents", "skills")
	if seed {
		require.NoError(t, os.MkdirAll(root, 0o755))
		writeHotReloadChatTestSkill(t, root, "seed-skill", "seed")
		opts.SkillDir = root
		cfg.SkillsRuntime.SkillDir = root
	}
	if hotReload {
		opts.WatchSkillDirs = resolveChatSkillWatchDirs(cfg, session)
	}

	shared, err := runtimebootstrap.NewManager(opts)
	require.NoError(t, err)
	t.Cleanup(func() { _ = shared.Stop() })
	binding, err := initSkillFunctionsWithManager(cfg, session, nil, shared, nil, 0, "")
	require.NoError(t, err)
	require.NotNil(t, binding)
	return &skillReloadTestEnv{session: session, manager: shared, root: root}
}

func TestParseSkillsReloadQuery(t *testing.T) {
	for _, query := range []string{"reload", "refresh", "RELOAD", " reload "} {
		require.True(t, parseSkillsReloadQuery(query), query)
	}
	for _, query := range []string{"", "reload now", "image", "list", "disable x"} {
		require.False(t, parseSkillsReloadQuery(query), query)
	}
}

// reload 会重建 registry/函数面：忙时排队（queue），生效域仍是 live（空闲时
// 立即执行并马上生效），且不需要确认门。
func TestSkillsReloadRegisteredAsQueueLive(t *testing.T) {
	spec, ok := resolveRuntimeCommandSpec("/skills reload")
	require.True(t, ok)
	require.Equal(t, runtimeModeQueue, spec.Mode)
	require.Equal(t, runtimeEffectLive, spec.Effect)
	require.False(t, spec.Confirm, "reload is a safe refresh and must not require confirmation")
}

func TestSkillsSubcommandCompletionIncludesReload(t *testing.T) {
	found := false
	for _, item := range skillsSubcommandArgumentCandidates() {
		if item.Command == "reload" {
			found = true
			require.True(t, strings.Contains(item.Summary, "热刷新"))
		}
	}
	require.True(t, found, "completion must offer /skills reload")
}

// 无热加载（未开监听）时退化为显式重建：即使没有 watcher，手工 reload 也要
// 把磁盘上的新增/删除同步进 registry 与函数面，并报告差集。
func TestSkillsReloadCommandReportsAddedAndRemovedWithoutHotReload(t *testing.T) {
	env := newSkillReloadTestEnv(t, false, true)

	writeHotReloadChatTestSkill(t, env.root, "runtime-skill", "added by reload test")
	report, err := runSkillReloadCommand(env.session)
	require.NoError(t, err)
	require.Equal(t, 1, report.Before)
	require.Equal(t, 2, report.After)
	require.Equal(t, []string{"runtime-skill"}, report.Added)
	require.Empty(t, report.Removed)
	require.False(t, report.HotReload)
	require.Equal(t, 2, env.session.FunctionCatalog.Stats().SkillFunctions)
	require.Contains(t, report.Text(), "已重载 skills")

	require.NoError(t, os.RemoveAll(filepath.Join(env.root, "seed-skill")))
	report, err = runSkillReloadCommand(env.session)
	require.NoError(t, err)
	require.Equal(t, 2, report.Before)
	require.Equal(t, 1, report.After)
	require.Equal(t, []string{"seed-skill"}, report.Removed)
	require.Empty(t, report.Added)
	require.Equal(t, 1, env.session.FunctionCatalog.Stats().SkillFunctions)
}

// 开启目录监听时走 HotReload 整表重载；结构化入口 /skills reload 与 --json
// 都要报告 after 计数并落到函数面。
func TestExecuteStructuredSkillsReloadUsesHotReload(t *testing.T) {
	env := newSkillReloadTestEnv(t, true, true)
	writeHotReloadChatTestSkill(t, env.root, "runtime-skill", "hot added")

	result, handled := executeStructuredSkillsMenuCommand(env.session, "/skills reload")
	require.True(t, handled)
	require.Nil(t, result.Screen)
	text := strings.TrimSpace(ui.RenderDocumentPlain(result.Document()))
	require.Contains(t, text, "已重载 skills")
	require.Contains(t, text, "total=2")
	require.Contains(t, text, "热重载")
	require.Equal(t, 2, env.session.FunctionCatalog.Stats().SkillFunctions)

	jsonResult, handled := executeStructuredSkillsMenuCommand(env.session, "/skills reload --json")
	require.True(t, handled)
	var payload struct {
		Status    string `json:"status"`
		HotReload bool   `json:"hot_reload"`
		Before    int    `json:"before"`
		After     int    `json:"after"`
	}
	require.NoError(t, json.Unmarshal([]byte(strings.TrimSpace(ui.RenderDocumentPlain(jsonResult.Document()))), &payload))
	require.Equal(t, "success", payload.Status)
	require.True(t, payload.HotReload)
	require.Equal(t, 2, payload.After)
}

// 全新工作区（启动时没有任何技能根）手工 reload 不应报错：清零 + 刷新为空面板。
func TestSkillsReloadCommandEmptyWorkspaceReportsZero(t *testing.T) {
	env := newSkillReloadTestEnv(t, true, false)

	report, err := runSkillReloadCommand(env.session)
	require.NoError(t, err)
	require.Equal(t, 0, report.Before)
	require.Equal(t, 0, report.After)
	require.False(t, report.HotReload)
	require.Contains(t, report.Text(), "total=0")
	require.Equal(t, 0, env.session.FunctionCatalog.Stats().SkillFunctions)
}
