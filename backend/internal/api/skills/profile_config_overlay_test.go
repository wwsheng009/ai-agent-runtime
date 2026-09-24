package skills

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	profilesys "github.com/wwsheng009/ai-agent-runtime/internal/profile"
	runtimeskill "github.com/wwsheng009/ai-agent-runtime/internal/skill"
)

func skillsRuntimeBaseConfig() *agentconfig.Config {
	return &agentconfig.Config{
		AICLI: &agentconfig.AICLIConfig{},
		SkillsRuntime: &agentconfig.SkillsRuntimeConfig{
			Enabled:                false,
			AICLISkillExposureMode: "auto",
			AICLISkillExposureTopK: 3,
		},
	}
}

// TestBuildProfileConfigOverlayAppliesSkillsRuntimeOverride 是 D13 server 半程
// （V27 定位的请求级生效点）的单一权威断言：
//   - profile 的 `runtime.overrides` 真的改变本次请求读取到的技能运行时配置；
//   - 宿主配置快照不被就地修改：同一 handler 的其它会话仍读到基线（D13/R10，
//     覆盖是会话级视图，不跨会话泄漏）。
//
// 覆盖键取 `skills_runtime.*` 字段级白名单内的键（V11/Q9）：目录/曝光开关是
// server 侧 catalog/exposure 注入真正读取的字段。
func TestBuildProfileConfigOverlayAppliesSkillsRuntimeOverride(t *testing.T) {
	handler := NewHandler(runtimeskill.NewRegistry(nil), nil, nil)
	handler.SetAICLIConfig(skillsRuntimeBaseConfig())

	resolved := &profilesys.ResolvedAgent{Overrides: map[string]interface{}{
		"skills_runtime": map[string]interface{}{
			"enabled":                    true,
			"aicli_skill_exposure_mode":  "always",
			"aicli_skill_exposure_top_k": 7,
		},
	}}

	overlay, keys, origins, err := handler.buildProfileConfigOverlay(resolved)
	require.NoError(t, err)
	require.NotNil(t, overlay, "覆盖必须产出合并视图")
	require.Equal(t, []string{
		"skills_runtime.aicli_skill_exposure_mode",
		"skills_runtime.aicli_skill_exposure_top_k",
		"skills_runtime.enabled",
	}, keys)
	require.Equal(t, profilesys.OverrideOriginProfile, origins["skills_runtime.enabled"])

	cfg := handler.skillsRuntimeConfigFor(&profileRuntimeState{ConfigOverlay: overlay})
	require.NotNil(t, cfg)
	require.True(t, cfg.Enabled)
	require.Equal(t, "always", cfg.AICLISkillExposureMode)
	require.Equal(t, 7, cfg.AICLISkillExposureTopK)

	base := handler.runtimeSkillsConfig()
	require.False(t, base.Enabled, "宿主快照必须保持基线（不就地修改）")
	require.Equal(t, "auto", base.AICLISkillExposureMode)
	require.Equal(t, 3, base.AICLISkillExposureTopK)
	require.NotSame(t, base, cfg, "覆盖视图必须是独立副本")

	// 无 profile 的请求（profileState=nil 或空状态）回落宿主快照本身：零变化。
	require.Same(t, base, handler.skillsRuntimeConfigFor(nil))
	require.Same(t, base, handler.skillsRuntimeConfigFor(&profileRuntimeState{}))
}

// TestBuildProfileConfigOverlayZeroChangeStaysNil 锁定零变化语义（NFR-1）：未声明
// 覆盖、或覆盖值与基线同值（合并后无差异）时必须返回 nil 视图，调用方回落基线。
func TestBuildProfileConfigOverlayZeroChangeStaysNil(t *testing.T) {
	handler := NewHandler(runtimeskill.NewRegistry(nil), nil, nil)
	handler.SetAICLIConfig(skillsRuntimeBaseConfig())

	overlay, keys, origins, err := handler.buildProfileConfigOverlay(&profilesys.ResolvedAgent{})
	require.NoError(t, err)
	require.Nil(t, overlay)
	require.Nil(t, keys)
	require.Nil(t, origins)

	sameValue := &profilesys.ResolvedAgent{Overrides: map[string]interface{}{
		"skills_runtime": map[string]interface{}{"aicli_skill_exposure_top_k": 3},
	}}
	overlay, _, _, err = handler.buildProfileConfigOverlay(sameValue)
	require.NoError(t, err)
	require.Nil(t, overlay, "同值覆盖必须按零变化处理")

	overlay, _, _, err = handler.buildProfileConfigOverlay(nil)
	require.NoError(t, err)
	require.Nil(t, overlay)
}

// TestBuildProfileConfigOverlayWithoutHostConfigStaysNil 锁定缺省语义：宿主未接线
// 配置（SetAICLIConfig 未调用）时没有可叠加的基线，覆盖视图保持 nil，而不是凭空
// 造一份配置（避免"配了看起来生效、实际无消费者"的假开关）。
func TestBuildProfileConfigOverlayWithoutHostConfigStaysNil(t *testing.T) {
	handler := NewHandler(runtimeskill.NewRegistry(nil), nil, nil)

	overlay, _, _, err := handler.buildProfileConfigOverlay(&profilesys.ResolvedAgent{
		Overrides: map[string]interface{}{"skills_runtime": map[string]interface{}{"aicli_skill_exposure_top_k": 7}},
	})
	require.NoError(t, err)
	require.Nil(t, overlay)
	require.Nil(t, handler.skillsRuntimeConfigFor(&profileRuntimeState{ConfigOverlay: overlay}))
}

// TestBuildProfileConfigOverlayRejectsForbiddenKeys 锁定 D14 的运行时双执行：即使
// 绕过解析期（构造非法 ResolvedAgent），禁止域覆盖也必须在构造视图时报错，而不是
// 把坏配置带进运行时。
func TestBuildProfileConfigOverlayRejectsForbiddenKeys(t *testing.T) {
	handler := NewHandler(runtimeskill.NewRegistry(nil), nil, nil)
	handler.SetAICLIConfig(skillsRuntimeBaseConfig())

	_, _, _, err := handler.buildProfileConfigOverlay(&profilesys.ResolvedAgent{
		Overrides: map[string]interface{}{"runtime": map[string]interface{}{"mode": "server"}},
	})
	require.Error(t, err)
}

// TestBuildProfileConfigOverlayCarriesRoutingDomains 锁定会话级覆盖视图的语义：
// 白名单内的 routing 域同样进入合并视图（server 侧会话级消费点余项见 V27 回填），
// 且同样不改动宿主快照。
func TestBuildProfileConfigOverlayCarriesRoutingDomains(t *testing.T) {
	handler := NewHandler(runtimeskill.NewRegistry(nil), nil, nil)
	handler.SetAICLIConfig(skillsRuntimeBaseConfig())

	overlay, keys, _, err := handler.buildProfileConfigOverlay(&profilesys.ResolvedAgent{
		Overrides: map[string]interface{}{
			"aicli": map[string]interface{}{
				"subagents": map[string]interface{}{
					"routing": map[string]interface{}{"default_difficulty": "high"},
				},
			},
		},
	})
	require.NoError(t, err)
	require.NotNil(t, overlay)
	require.NotNil(t, overlay.AICLI)
	require.NotNil(t, overlay.AICLI.Subagents)
	require.NotNil(t, overlay.AICLI.Subagents.Routing)
	require.Equal(t, "high", overlay.AICLI.Subagents.Routing.DefaultDifficulty)
	require.Equal(t, []string{"aicli.subagents.routing.default_difficulty"}, keys)

	require.Nil(t, handler.subagentRoutingConfig(), "宿主快照必须保持基线（覆盖不泄漏到进程级配置）")
}
