package profile

import (
	"testing"

	config "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	"gopkg.in/yaml.v3"
)

// TestOverrideCatalogAuthoritativeEffectPoints 是 Batch 9 / T1（设计 §9.2 开关
// 目录）的单一权威断言：目录中每个"覆盖型（T1）"开关必须同时满足两条——
//
//  1. **白名单放行**：ValidateOverrides 不产生 error（profile 写得进去）；
//  2. **覆盖落到生产读取的字段**：ApplyConfigOverlayYAML 解码后的 *Config 字段
//     真的变化（不是 dormant 路径 R9，也不是"配了不生效"的假开关）。
//
// 只断言第 1 条会漏掉 Batch 7 把根级 `skills_runtime` 写成 `aicli.skills_runtime`
// 的那类错误（V11）：路径写错时校验器可能仍放行（历史规则更宽），但配置永远不
// 生效。第 2 条把它钉死——键路径必须在 schema 中真实存在且被生产结构体消费。
//
// 新增目录项时必须同时在这里加一行，否则"接线到白名单"就没有验收线。
func TestOverrideCatalogAuthoritativeEffectPoints(t *testing.T) {
	cases := []struct {
		name      string
		overrides map[string]interface{}
		check     func(t *testing.T, cfg *config.Config)
	}{
		{
			// §9.2「Skills 运行时总开关」：agentconfig/config.go:811
			// （env SKILLS_RUNTIME_ENABLED）。
			name:      "skills runtime enabled",
			overrides: map[string]interface{}{"skills_runtime": map[string]interface{}{"enabled": false}},
			check: func(t *testing.T, cfg *config.Config) {
				if cfg.SkillsRuntime == nil {
					t.Fatal("skills_runtime 节解码为 nil")
				}
				if cfg.SkillsRuntime.Enabled {
					t.Fatal("覆盖 skills_runtime.enabled=false 后生产字段仍为 true（假开关）")
				}
			},
		},
		{
			// §9.2「Skills 曝光模式 / TopK」：config.go:815-816，与第一部分 FR-4
			// 同一消费点（技能曝光预算）。
			name: "skills exposure mode and top_k",
			overrides: map[string]interface{}{"skills_runtime": map[string]interface{}{
				"aicli_skill_exposure_mode":  "top_k_only",
				"aicli_skill_exposure_top_k": 3,
			}},
			check: func(t *testing.T, cfg *config.Config) {
				if cfg.SkillsRuntime == nil {
					t.Fatal("skills_runtime 节解码为 nil")
				}
				if cfg.SkillsRuntime.AICLISkillExposureMode != "top_k_only" {
					t.Fatalf("exposure mode = %q, want top_k_only", cfg.SkillsRuntime.AICLISkillExposureMode)
				}
				if cfg.SkillsRuntime.AICLISkillExposureTopK != 3 {
					t.Fatalf("exposure top_k = %d, want 3", cfg.SkillsRuntime.AICLISkillExposureTopK)
				}
			},
		},
		{
			// §9.2「日志开关」：config.go:550（AICLILogConfig.Enabled，*bool）。
			name:      "aicli log enabled",
			overrides: map[string]interface{}{"aicli": map[string]interface{}{"log": map[string]interface{}{"enabled": false}}},
			check: func(t *testing.T, cfg *config.Config) {
				if cfg.AICLI == nil || cfg.AICLI.Log == nil || cfg.AICLI.Log.Enabled == nil {
					t.Fatal("aicli.log.enabled 未落到生产字段")
				}
				if *cfg.AICLI.Log.Enabled {
					t.Fatal("覆盖 aicli.log.enabled=false 后生产字段仍为 true（假开关）")
				}
			},
		},
		{
			// §9.2「Model cards」：config.go:644（AICLIModelCardsConfig.Enabled）。
			name:      "aicli model cards enabled",
			overrides: map[string]interface{}{"aicli": map[string]interface{}{"model_cards": map[string]interface{}{"enabled": false}}},
			check: func(t *testing.T, cfg *config.Config) {
				if cfg.AICLI == nil || cfg.AICLI.ModelCards == nil || cfg.AICLI.ModelCards.Enabled == nil {
					t.Fatal("aicli.model_cards.enabled 未落到生产字段")
				}
				if *cfg.AICLI.ModelCards.Enabled {
					t.Fatal("覆盖 aicli.model_cards.enabled=false 后生产字段仍为 true（假开关）")
				}
			},
		},
		{
			// §9.2「主 Agent 动态 provider/model 切换」：config.go:540 +
			// main_agent_routing.go:64（AICLIMainAgentRoutingConfig.Enabled，
			// 注释明确"默认关闭"）。review 类场景可关。
			name: "aicli main agent routing enabled",
			overrides: map[string]interface{}{"aicli": map[string]interface{}{"main_agent": map[string]interface{}{
				"routing": map[string]interface{}{
					"enabled":            true,
					"levels":             []interface{}{"normal", "hard"},
					"default_difficulty": "normal",
				},
			}}},
			check: func(t *testing.T, cfg *config.Config) {
				if cfg.AICLI == nil || cfg.AICLI.MainAgent == nil || cfg.AICLI.MainAgent.Routing == nil {
					t.Fatal("aicli.main_agent.routing 未落到生产字段（MainAgent.Routing 仍为 nil）")
				}
				if !cfg.AICLI.MainAgent.Routing.Enabled {
					t.Fatal("覆盖 aicli.main_agent.routing.enabled=true 后生产字段仍为 false（假开关）")
				}
				if len(cfg.AICLI.MainAgent.Routing.Levels) != 2 {
					t.Fatalf("routing levels = %v, want 2 项", cfg.AICLI.MainAgent.Routing.Levels)
				}
			},
		},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			for _, issue := range ValidateOverrides(tt.overrides) {
				if issue.Severity == ProfileSpecIssueError {
					t.Fatalf("目录开关必须在覆盖白名单内（D14）：%s", issue.Message)
				}
			}
			overlayYAML, err := yaml.Marshal(tt.overrides)
			if err != nil {
				t.Fatalf("encode overlay: %v", err)
			}
			merged, changed, err := config.ApplyConfigOverlayYAML(profileCatalogBaseConfig(), overlayYAML)
			if err != nil {
				t.Fatalf("apply overlay: %v", err)
			}
			if !changed || merged == nil {
				t.Fatal("覆盖未改变配置：该键路径在 schema 中不存在（dormant / R9 假开关）")
			}
			tt.check(t, merged)
		})
	}
}

// profileCatalogBaseConfig 是与生产启动值同形的基线：目录开关的断言必须是
// "true → false / 关闭 → 打开"的真实翻转，而不是"零值恰好等于期望值"。
func profileCatalogBaseConfig() *config.Config {
	enabled := true
	return &config.Config{
		AICLI: &config.AICLIConfig{
			Log:        &config.AICLILogConfig{Enabled: &enabled},
			ModelCards: &config.AICLIModelCardsConfig{Enabled: &enabled},
		},
		SkillsRuntime: &config.SkillsRuntimeConfig{
			Enabled:                true,
			AICLISkillExposureMode: "off",
			AICLISkillExposureTopK: 0,
		},
	}
}
