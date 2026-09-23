package runtimeserver

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	agentconfig "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
)

// 外部改动感知（§9.1 登记的「快照刷新边界」）用例。
//
// 关键性质：
//  1. 签名只反映「来源文件变了没有」，首次轮询只建基线，不重复应用启动时已加载的配置；
//  2. 外部改动走与 config document API 写入**同一条**热重载路径（同一套热/冷判据、
//     同一套 warning），不新建第二套生效语义；
//  3. 半写入/非法改动保持旧快照 + warning，绝不让服务不可用；
//  4. 轮询随 ctx 取消退出（不泄漏 goroutine）。

func TestConfigSourceSignatureTracksLayerFileChanges(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	other := filepath.Join(dir, "other.yaml")

	absent := configSourceSignatureForPaths([]string{path})
	require.Contains(t, absent, "absent")

	require.NoError(t, os.WriteFile(path, []byte("aicli: {}\n"), 0o600))
	present := configSourceSignatureForPaths([]string{path})
	require.NotEqual(t, absent, present)
	require.NotContains(t, present, "absent")

	// 内容/大小变化必须改变签名（不依赖时间戳精度）。
	require.NoError(t, os.WriteFile(path, []byte("aicli:\n  teams: {}\n"), 0o600))
	require.NotEqual(t, present, configSourceSignatureForPaths([]string{path}))

	// 签名与输入顺序无关（层栈顺序变化不该被当成文件改动）。
	forward := configSourceSignatureForPaths([]string{path, other})
	backward := configSourceSignatureForPaths([]string{other, path})
	require.Equal(t, forward, backward)

	// 空路径被忽略；来源清单与加载器同源且非空。
	require.Equal(t, forward, configSourceSignatureForPaths([]string{path, " ", other}))
	require.NotEmpty(t, configSourceLayerPaths())
	require.NotEmpty(t, ConfigSourceSignature())
}

// TestConfigSourceSignatureForWatchesExplicitPathAndPresets 钉住「来源清单 = 加载器输入
// 闭包」：分层搜索栈之外，显式 --config 路径与预设层（~/.aicli/presets.yaml）都必须在
// 签名里，否则自定义路径启动的 runtime-server 永远感知不到外部改动。
func TestConfigSourceSignatureForWatchesExplicitPathAndPresets(t *testing.T) {
	dir := t.TempDir()
	custom := filepath.Join(dir, "custom-config.yaml")
	require.NoError(t, os.WriteFile(custom, []byte("aicli: {}\n"), 0o600))

	baseline := ConfigSourceSignatureFor(custom)
	require.Contains(t, baseline, filepath.Base(custom))

	// 显式路径的内容变化必须改变签名。
	require.NoError(t, os.WriteFile(custom, []byte("aicli:\n  teams: {}\n"), 0o600))
	require.NotEqual(t, baseline, ConfigSourceSignatureFor(custom))

	// 预设层是合并到用户配置之下的层，用户预设文件在签名内。
	require.NotEmpty(t, strings.TrimSpace(agentconfig.UserPresetsPath()))
	require.Contains(t, baseline, filepath.Base(agentconfig.UserPresetsPath()))

	// 未指定显式路径时退化到默认集合（与 ConfigSourceSignature 同一份）。
	require.Equal(t, ConfigSourceSignature(), ConfigSourceSignatureFor(""))
	require.Equal(t, ConfigSourceSignature(), ConfigSourceSignatureFor("   "))
}

func TestConfigExternalReloaderAppliesExternalChange(t *testing.T) {
	enabled := true
	currentCfg := &agentconfig.Config{AICLI: &agentconfig.AICLIConfig{
		Teams: &agentconfig.AICLITeamsConfig{Routing: &agentconfig.AICLISubagentRoutingConfig{
			Enabled: &enabled,
			Levels: map[string]agentconfig.AICLISubagentRouteProfile{
				"expert": {Provider: "old-provider", Model: "old-model"},
			},
		}},
	}}
	target := &fakeRuntimeConfigApplyTarget{}
	reloader := NewRuntimeConfigHotReloader(target, currentCfg, nil)
	require.NotNil(t, reloader)

	signature := "sig-1"
	loadCalls := 0
	var staged *agentconfig.Config
	external := NewConfigExternalReloader(
		reloader,
		func() (*agentconfig.Config, error) {
			loadCalls++
			if staged == nil {
				return nil, errors.New("config file is being rewritten")
			}
			return staged, nil
		},
		func() string { return signature },
		0,
	)
	require.NotNil(t, external)

	// 1) 首次轮询只建基线：不加载、不应用（启动时已加载并注入）。
	applied, result := external.PollOnce()
	require.False(t, applied)
	require.Empty(t, result.Warnings)
	require.Equal(t, 0, loadCalls)

	// 2) 签名未变化：不加载、不应用。
	applied, _ = external.PollOnce()
	require.False(t, applied)
	require.Equal(t, 0, loadCalls)

	// 3) 外部改动但读取/解析失败（半写入）：保持旧快照 + warning，不触碰目标。
	signature = "sig-2"
	applied, result = external.PollOnce()
	require.False(t, applied)
	require.Len(t, result.Warnings, 1)
	require.Contains(t, result.Warnings[0], "继续使用进程内快照")
	require.Equal(t, 1, loadCalls)
	require.Nil(t, target.aicliConfig)
	require.Equal(t, "old-model", currentCfg.AICLI.Teams.Routing.Levels["expert"].Model)

	// 4) 外部改动加载成功且命中热重载路径：与 API 写入同一条路径（应用到目标 + 推进快照）。
	staged = &agentconfig.Config{AICLI: &agentconfig.AICLIConfig{
		Teams: &agentconfig.AICLITeamsConfig{Routing: &agentconfig.AICLISubagentRoutingConfig{
			Enabled: &enabled,
			Levels: map[string]agentconfig.AICLISubagentRouteProfile{
				"expert": {Provider: "team-provider", Model: "team-model"},
			},
		}},
	}}
	signature = "sig-3"
	applied, result = external.PollOnce()
	require.True(t, applied)
	// 路径落在与 config document API 相同的 YAML 键空间（叶子级），
	// 而不是 typed struct 的字段名——否则热/冷判据就不是同一套结论。
	require.ElementsMatch(t, []string{
		"aicli.teams.routing.levels.expert.model",
		"aicli.teams.routing.levels.expert.provider",
	}, result.AppliedPaths)
	require.NotEmpty(t, result.Warnings)
	require.Contains(t, result.Warnings[0], "已即时应用到当前 runtime-server 进程")
	require.Same(t, staged, target.aicliConfig)
	require.Equal(t, "team-model", currentCfg.AICLI.Teams.Routing.Levels["expert"].Model)
}

// TestConfigExternalReloaderKeepsColdPathJudgement 钉住「外部改动不绕过冷/热判据」：
// 需要重启才生效的路径（skills_runtime.config_file）同样只给 warning，不会被静默应用到
// 运行中的进程，也不会误调 SetAICLIConfig。
func TestConfigExternalReloaderKeepsColdPathJudgement(t *testing.T) {
	currentCfg := &agentconfig.Config{SkillsRuntime: &agentconfig.SkillsRuntimeConfig{
		ConfigFile: "configs/runtime.yaml",
	}}
	target := &fakeRuntimeConfigApplyTarget{}
	reloader := NewRuntimeConfigHotReloader(target, currentCfg, nil)

	signature := "sig-1"
	staged := &agentconfig.Config{SkillsRuntime: &agentconfig.SkillsRuntimeConfig{
		ConfigFile: "configs/runtime-elsewhere.yaml",
	}}
	external := NewConfigExternalReloader(
		reloader,
		func() (*agentconfig.Config, error) { return staged, nil },
		func() string { return signature },
		0,
	)

	external.PollOnce() // 基线
	signature = "sig-2"
	applied, result := external.PollOnce()
	require.True(t, applied)
	require.NotEmpty(t, result.Warnings)
	require.Contains(t, result.Warnings[0], "skills_runtime.config_file")
	require.Contains(t, result.Warnings[0], "需重启")
	require.Empty(t, result.AppliedPaths)
	require.Nil(t, target.aicliConfig)
}

// TestConfigExternalReloaderDetectsRealFileEdit 用真实文件验证签名接线：外部改写来源
// 文件（大小/mtime 变化）后下一次轮询必须重载。
func TestConfigExternalReloaderDetectsRealFileEdit(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	require.NoError(t, os.WriteFile(path, []byte("aicli: {}\n"), 0o600))

	enabled := true
	currentCfg := &agentconfig.Config{AICLI: &agentconfig.AICLIConfig{
		Teams: &agentconfig.AICLITeamsConfig{Routing: &agentconfig.AICLISubagentRoutingConfig{
			Enabled: &enabled,
			Levels: map[string]agentconfig.AICLISubagentRouteProfile{
				"expert": {Model: "old-model"},
			},
		}},
	}}
	target := &fakeRuntimeConfigApplyTarget{}
	reloader := NewRuntimeConfigHotReloader(target, currentCfg, nil)
	staged := &agentconfig.Config{AICLI: &agentconfig.AICLIConfig{
		Teams: &agentconfig.AICLITeamsConfig{Routing: &agentconfig.AICLISubagentRoutingConfig{
			Enabled: &enabled,
			Levels: map[string]agentconfig.AICLISubagentRouteProfile{
				"expert": {Model: "new-model"},
			},
		}},
	}}
	external := NewConfigExternalReloader(
		reloader,
		func() (*agentconfig.Config, error) { return staged, nil },
		func() string { return configSourceSignatureForPaths([]string{path}) },
		0,
	)

	applied, _ := external.PollOnce()
	require.False(t, applied)
	require.Nil(t, target.aicliConfig)

	require.NoError(t, os.WriteFile(path, []byte("aicli:\n  teams:\n    routing: {}\n"), 0o600))
	applied, result := external.PollOnce()
	require.True(t, applied)
	require.Equal(t, []string{"aicli.teams.routing.levels.expert.model"}, result.AppliedPaths)
	require.Same(t, staged, target.aicliConfig)
	require.Equal(t, "new-model", currentCfg.AICLI.Teams.Routing.Levels["expert"].Model)
}

func TestConfigExternalReloaderRunStopsOnContextCancel(t *testing.T) {
	currentCfg := &agentconfig.Config{}
	reloader := NewRuntimeConfigHotReloader(&fakeRuntimeConfigApplyTarget{}, currentCfg, nil)

	var mu sync.Mutex
	loadCalls := 0
	external := NewConfigExternalReloader(
		reloader,
		func() (*agentconfig.Config, error) {
			mu.Lock()
			loadCalls++
			mu.Unlock()
			return &agentconfig.Config{}, nil
		},
		func() string { return "stable-signature" },
		10*time.Millisecond,
	)
	require.NotNil(t, external)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		external.Run(ctx)
		close(done)
	}()

	time.Sleep(60 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run 未在 ctx 取消后返回（轮询 goroutine 泄漏）")
	}

	mu.Lock()
	defer mu.Unlock()
	require.Equal(t, 0, loadCalls, "签名稳定时不应触发重载")
}
