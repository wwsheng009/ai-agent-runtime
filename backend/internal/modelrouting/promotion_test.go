package modelrouting

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	agentconfig "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
)

// promotionConfig 构造一份「路由开启 + 四档 level 齐全」的基线配置，测试只改写
// 与提升相关的字段（G3 三态 / G4 词表），避免每个用例重复铺配置。
func promotionConfig(mutate func(*agentconfig.AICLISubagentRoutingConfig)) *agentconfig.AICLISubagentRoutingConfig {
	enabled := true
	cfg := &agentconfig.AICLISubagentRoutingConfig{
		Enabled:           &enabled,
		DefaultDifficulty: DifficultyNormal,
		Levels: map[string]agentconfig.AICLISubagentRouteProfile{
			DifficultyEasy:   {Provider: "local", Model: "easy-model"},
			DifficultyNormal: {Provider: "local", Model: "normal-model"},
			DifficultyHard:   {Provider: "local", Model: "hard-model"},
			DifficultyExpert: {Provider: "remote", Model: "expert-model"},
		},
	}
	if mutate != nil {
		mutate(cfg)
	}
	return cfg
}

func resolveWithTask(t *testing.T, cfg *agentconfig.AICLISubagentRoutingConfig, task TaskHint) RouteDecision {
	t.Helper()
	decision, err := (Resolver{Config: cfg}).Resolve(ParentDefaults{
		Provider: "parent-provider",
		Model:    "parent-model",
	}, task)
	require.NoError(t, err)
	return decision
}

// TestResolveExplicitDifficulty_PromotesOnStrongKeyword 锁定 G3 的默认行为：
// 未配置 promote_explicit_difficulty 时默认 enforce，显式声明的 easy 被
// 高信号关键词（中文「迁移」）抬到 hard，并保留可回溯的证据链。
func TestResolveExplicitDifficulty_PromotesOnStrongKeyword(t *testing.T) {
	decision := resolveWithTask(t, promotionConfig(nil), TaskHint{
		Difficulty: DifficultyEasy,
		Goal:       "迁移数据库 schema 并补齐回滚脚本",
	})

	assert.Equal(t, DifficultyHard, decision.Difficulty)
	assert.Equal(t, SourceExplicitPromoted, decision.DifficultySource)
	assert.Equal(t, "local", decision.Provider)
	assert.Equal(t, "hard-model", decision.Model)
	assert.Contains(t, decision.Warnings, "difficulty_promoted_over_explicit")
	assert.Contains(t, decision.Warnings, "difficulty_promoted_by_keyword:迁移")
}

// TestResolveExplicitDifficulty_EnglishKeywordPromotes 覆盖英文词表：历史词表
// 必须继续生效，否则英文工作流的升档会静默退化。
func TestResolveExplicitDifficulty_EnglishKeywordPromotes(t *testing.T) {
	decision := resolveWithTask(t, promotionConfig(nil), TaskHint{
		Difficulty: DifficultyEasy,
		Goal:       "plan the auth migration and rotate credentials",
	})

	assert.Equal(t, DifficultyHard, decision.Difficulty)
	assert.Equal(t, SourceExplicitPromoted, decision.DifficultySource)
	assert.Contains(t, decision.Warnings, "difficulty_promoted_by_keyword:migration")
}

// TestResolveExplicitDifficulty_WarnModeKeepsDeclaredDifficulty 锁定 warn 语义：
// 只写告警、不动档位，用于先观测误报再决定是否 enforce。
func TestResolveExplicitDifficulty_WarnModeKeepsDeclaredDifficulty(t *testing.T) {
	cfg := promotionConfig(func(c *agentconfig.AICLISubagentRoutingConfig) {
		c.PromoteExplicitDifficulty = PromoteExplicitWarn
	})
	decision := resolveWithTask(t, cfg, TaskHint{
		Difficulty: DifficultyEasy,
		Goal:       "plan the auth migration",
	})

	assert.Equal(t, DifficultyEasy, decision.Difficulty)
	assert.Equal(t, "explicit", decision.DifficultySource)
	assert.Equal(t, "easy-model", decision.Model)
	assert.Contains(t, decision.Warnings, "difficulty_promoted_over_explicit")
	assert.Contains(t, decision.Warnings, "difficulty_promotion_warn_only")
	assert.Contains(t, decision.Warnings, "difficulty_promoted_by_keyword:migration")
}

// TestResolveExplicitDifficulty_OffModeRestoresLegacyBehavior 锁定 off 是历史
// 行为的回退开关：既不提升，也不产生任何提升告警（否则告警噪音会淹没审计）。
func TestResolveExplicitDifficulty_OffModeRestoresLegacyBehavior(t *testing.T) {
	cfg := promotionConfig(func(c *agentconfig.AICLISubagentRoutingConfig) {
		c.PromoteExplicitDifficulty = PromoteExplicitOff
	})
	decision := resolveWithTask(t, cfg, TaskHint{
		Difficulty: DifficultyEasy,
		Goal:       "plan the auth migration",
	})

	assert.Equal(t, DifficultyEasy, decision.Difficulty)
	assert.Equal(t, "explicit", decision.DifficultySource)
	assert.Equal(t, "easy-model", decision.Model)
	assert.NotContains(t, decision.Warnings, "difficulty_promoted_over_explicit")
	assert.NotContains(t, decision.Warnings, "difficulty_promoted_by_keyword:migration")

	// 对照组：同一个 goal 在默认 enforce 下必须升档。否则上面的 NotContains
	// 可能只是因为词表压根没命中，断言会变成空转。
	control := resolveWithTask(t, promotionConfig(nil), TaskHint{
		Difficulty: DifficultyEasy,
		Goal:       "plan the auth migration",
	})
	assert.Equal(t, DifficultyHard, control.Difficulty)
	assert.Equal(t, SourceExplicitPromoted, control.DifficultySource)
}

// TestResolveExplicitDifficulty_NeverDowngradesExpert 锁定单调性：提升取 rank
// 最大值，显式声明的 expert 不会被"命中词只到 hard"拉低。
func TestResolveExplicitDifficulty_NeverDowngradesExpert(t *testing.T) {
	decision := resolveWithTask(t, promotionConfig(nil), TaskHint{
		Difficulty: DifficultyExpert,
		Goal:       "review security boundaries of the provider protocol",
	})

	assert.Equal(t, DifficultyExpert, decision.Difficulty)
	assert.Equal(t, "explicit", decision.DifficultySource)
	assert.Equal(t, "expert-model", decision.Model)
	assert.NotContains(t, decision.Warnings, "difficulty_promoted_over_explicit")
}

// TestResolveExplicitDifficulty_WeakSignalNeedsTwoHits 覆盖弱信号阈值：单个弱
// 命中（「一致性」）不足以升档，两个命中才升——这是压制误报的核心阀门。
func TestResolveExplicitDifficulty_WeakSignalNeedsTwoHits(t *testing.T) {
	single := resolveWithTask(t, promotionConfig(nil), TaskHint{
		Difficulty: DifficultyEasy,
		Goal:       "检查缓存一致性",
	})
	assert.Equal(t, DifficultyEasy, single.Difficulty)
	assert.Equal(t, "explicit", single.DifficultySource)
	assert.NotContains(t, single.Warnings, "difficulty_promoted_over_explicit")
	assert.NotContains(t, single.Warnings, "difficulty_promoted_by_keyword:一致性")

	double := resolveWithTask(t, promotionConfig(nil), TaskHint{
		Difficulty: DifficultyEasy,
		Goal:       "检查缓存一致性与接口兼容",
	})
	assert.Equal(t, DifficultyHard, double.Difficulty)
	assert.Equal(t, SourceExplicitPromoted, double.DifficultySource)
	assert.Contains(t, double.Warnings, "difficulty_promoted_by_keyword:一致性")
	assert.Contains(t, double.Warnings, "difficulty_promoted_by_keyword:兼容")
}

// TestResolveExplicitDifficulty_WeakSignalWithWriterRole 覆盖「1 个弱命中 +
// 非只读 writer」的组合规则：写任务碰到弱信号就升，读任务不升。
func TestResolveExplicitDifficulty_WeakSignalWithWriterRole(t *testing.T) {
	writer := resolveWithTask(t, promotionConfig(nil), TaskHint{
		Difficulty: DifficultyEasy,
		Role:       "writer",
		Goal:       "检查缓存一致性并补齐测试",
	})
	assert.Equal(t, DifficultyHard, writer.Difficulty)
	assert.Equal(t, SourceExplicitPromoted, writer.DifficultySource)
	assert.Contains(t, writer.Warnings, "difficulty_promoted_by_role:writer")
	assert.Contains(t, writer.Warnings, "difficulty_promoted_by_keyword:一致性")

	readOnly := resolveWithTask(t, promotionConfig(nil), TaskHint{
		Difficulty: DifficultyEasy,
		Role:       "writer",
		ReadOnly:   true,
		Goal:       "检查缓存一致性",
	})
	assert.Equal(t, DifficultyEasy, readOnly.Difficulty)
	assert.Equal(t, "explicit", readOnly.DifficultySource)
}

// TestResolveExplicitDifficulty_HeuristicsDisabledSkipsKeywords 锁定
// heuristics.disabled 的边界：关键词提升整体关闭，但角色提升（verifier）
// 仍然生效——两者是独立规则。
func TestResolveExplicitDifficulty_HeuristicsDisabledSkipsKeywords(t *testing.T) {
	cfg := promotionConfig(func(c *agentconfig.AICLISubagentRoutingConfig) {
		c.Heuristics = &agentconfig.AICLISubagentRoutingHeuristics{Disabled: true}
	})
	decision := resolveWithTask(t, cfg, TaskHint{
		Difficulty: DifficultyEasy,
		Goal:       "plan the auth migration",
	})
	assert.Equal(t, DifficultyEasy, decision.Difficulty)
	assert.Equal(t, "explicit", decision.DifficultySource)
	assert.NotContains(t, decision.Warnings, "difficulty_promoted_over_explicit")
	assert.NotContains(t, decision.Warnings, "difficulty_promoted_by_keyword:migration")

	verifier := resolveWithTask(t, cfg, TaskHint{
		Difficulty: DifficultyEasy,
		Role:       "verifier",
		Goal:       "plan the auth migration",
	})
	assert.Equal(t, DifficultyNormal, verifier.Difficulty)
	assert.Equal(t, SourceExplicitPromoted, verifier.DifficultySource)
	assert.Contains(t, verifier.Warnings, "difficulty_promoted_by_role:verifier")
	assert.NotContains(t, verifier.Warnings, "difficulty_promoted_by_keyword:migration")
}

// TestResolveExplicitDifficulty_CustomKeywordsAppend 锁定 G4 的追加语义：
// 配置词表只做补充，内置词表始终生效。
func TestResolveExplicitDifficulty_CustomKeywordsAppend(t *testing.T) {
	cfg := promotionConfig(func(c *agentconfig.AICLISubagentRoutingConfig) {
		c.Heuristics = &agentconfig.AICLISubagentRoutingHeuristics{
			PromoteKeywords: []string{"kafka"},
		}
	})
	custom := resolveWithTask(t, cfg, TaskHint{
		Difficulty: DifficultyEasy,
		Goal:       "rebalance kafka consumer groups",
	})
	assert.Equal(t, DifficultyHard, custom.Difficulty)
	assert.Contains(t, custom.Warnings, "difficulty_promoted_by_keyword:kafka")

	builtin := resolveWithTask(t, cfg, TaskHint{
		Difficulty: DifficultyEasy,
		Goal:       "harden the provider protocol surface",
	})
	assert.Equal(t, DifficultyHard, builtin.Difficulty)
	assert.Contains(t, builtin.Warnings, "difficulty_promoted_by_keyword:provider")
	assert.Contains(t, builtin.Warnings, "difficulty_promoted_by_keyword:protocol")
}

// TestResolveInferredDifficulty_PromotesWithHeuristicWarning 覆盖未声明难度的
// 推断路径：默认档位被抬升时，告警必须与显式路径可区分。
func TestResolveInferredDifficulty_PromotesWithHeuristicWarning(t *testing.T) {
	decision := resolveWithTask(t, promotionConfig(nil), TaskHint{
		Goal: "升级 provider 协议并补齐灰度发布",
	})

	assert.Equal(t, DifficultyHard, decision.Difficulty)
	assert.Equal(t, "inferred", decision.DifficultySource)
	assert.Contains(t, decision.Warnings, "difficulty_missing_defaulted")
	assert.Contains(t, decision.Warnings, "difficulty_promoted_by_heuristic")
	assert.Contains(t, decision.Warnings, "difficulty_promoted_by_keyword:provider")
}

func TestNormalizePromoteExplicitMode(t *testing.T) {
	cases := map[string]string{
		"":          PromoteExplicitEnforce,
		"enforce":   PromoteExplicitEnforce,
		"on":        PromoteExplicitEnforce,
		"TRUE":      PromoteExplicitEnforce,
		"warn":      PromoteExplicitWarn,
		"dry_run":   PromoteExplicitWarn,
		"off":       PromoteExplicitOff,
		"false":     PromoteExplicitOff,
		" none ":    PromoteExplicitOff,
		"disabled":  PromoteExplicitOff,
		"nonsense":  "",
		"enforce-x": "",
	}
	for input, expected := range cases {
		got, ok := NormalizePromoteExplicitMode(input)
		if expected == "" {
			assert.False(t, ok, "expected %q to be rejected", input)
			continue
		}
		require.True(t, ok, "expected %q to normalize", input)
		assert.Equal(t, expected, got, "input %q", input)
	}
}

func TestPromoteExplicitMode_DefaultsToEnforce(t *testing.T) {
	assert.Equal(t, PromoteExplicitEnforce, PromoteExplicitMode(nil))

	empty := promotionConfig(nil)
	assert.Equal(t, PromoteExplicitEnforce, PromoteExplicitMode(empty))

	invalid := promotionConfig(func(c *agentconfig.AICLISubagentRoutingConfig) {
		c.PromoteExplicitDifficulty = "bogus"
	})
	assert.Equal(t, PromoteExplicitEnforce, PromoteExplicitMode(invalid))
}

func TestExpertLimitLabel(t *testing.T) {
	assert.Equal(t, "unlimited", ExpertLimitLabel(nil))

	disabled := false
	assert.Equal(t, "unlimited", ExpertLimitLabel(&agentconfig.AICLISubagentRoutingConfig{
		Enabled:              &disabled,
		MaxExpertConcurrency: 2,
	}))

	unset := promotionConfig(nil)
	assert.Equal(t, "unlimited", ExpertLimitLabel(unset))

	negative := promotionConfig(func(c *agentconfig.AICLISubagentRoutingConfig) {
		c.MaxExpertConcurrency = -1
	})
	assert.Equal(t, "unlimited", ExpertLimitLabel(negative))

	limited := promotionConfig(func(c *agentconfig.AICLISubagentRoutingConfig) {
		c.MaxExpertConcurrency = 3
	})
	assert.Equal(t, "3", ExpertLimitLabel(limited))
}

func TestPromotionHitsKeywordsOrderAndBound(t *testing.T) {
	hits := promotionHits{
		Strong: []string{"security", "permission", "migration", "architecture"},
		Combo:  []string{"refactor"},
	}
	assert.Equal(t, []string{"security", "permission", "migration"}, hits.keywords())

	assert.True(t, promotionHits{}.empty())
	assert.False(t, promotionHits{Role: true}.empty())
	assert.Nil(t, promotionHits{}.keywords())
}

func TestNormalizeKeywordText(t *testing.T) {
	assert.Equal(t, "auth migration", normalizeKeywordText("  Auth\u3000Migration  "))
	assert.Equal(t, "security", normalizeKeywordText("Ｓｅｃｕｒｉｔｙ"))
	assert.Equal(t, "", normalizeKeywordText("   "))
}

// TestKeywordStem 锁定词形归一的规则表与两条护栏。返回 "" 表示"该词不参与
// 词形归一"，仍由子串匹配负责，因此这里期望 "" 不等于"不命中"。
func TestKeywordStem(t *testing.T) {
	cases := map[string]string{
		// 同一词干的不同词尾形态（含用户点名的 migrate/migration）
		"migration":     "migrat",
		"migrate":       "migrat",
		"migrates":      "migrat",
		"migrating":     "migrat",
		"migrated":      "migrat",
		"migrations":    "migrat",
		"permission":    "permiss",
		"permissions":   "permiss",
		"encryption":    "encrypt",
		"encrypting":    "encrypt",
		"security":      "securiti",
		"securities":    "securiti",
		"boundary":      "boundari",
		"boundaries":    "boundari",
		"architecture":  "architectur",
		"architectures": "architectur",
		"release":       "releas",
		"releases":      "releas",
		"refactoring":   "refactor",
		"refactors":     "refactor",
		"protocols":     "protocol",
		"providers":     "provider",
		"processes":     "process",
		// 已是原形 → 不归一（子串匹配已覆盖）
		"encrypt":  "",
		"provider": "",
		"protocol": "",
		// 护栏：短词（否则 act/action、use/using 会碰撞）
		"use":   "",
		"using": "",
		// 护栏：剥得过狠则放弃（quest/vers 这类词干不作为归一键）
		"question": "",
		"union":    "",
		// 不适用：中文、多词、空
		"迁移":         "",
		"rate limit": "",
		"":           "",
	}
	for input, expected := range cases {
		assert.Equal(t, expected, keywordStem(input), "keywordStem(%q)", input)
	}
}

// TestMatchedKeywords_StemVariants 覆盖两级匹配：词形归一必须命中（第一级子串
// 命中不了的那些），而历史子串语义与误报护栏必须原样保留。
func TestMatchedKeywords_StemVariants(t *testing.T) {
	cases := []struct {
		name    string
		goal    string
		keyword string
		hit     bool
	}{
		{"词表是名词、goal 是动词", "migrate the auth tables", "migration", true},
		{"词表是动词、goal 是名词", "plan the auth migration", "migrate", true},
		{"进行时", "migrating the auth tables", "migration", true},
		{"过去式", "we migrated the schema", "migration", true},
		{"复数", "rotate the permissions", "permission", true},
		{"y→ies", "review module boundaries", "boundary", true},
		{"-ion 家族（encrypt/encryption）", "encrypt the payload", "encryption", true},
		{"弱信号 y→ies", "verify cache consistencies across nodes", "consistency", true},
		{"中英混排也要能切词", "迁移migrate脚本", "migration", true},
		// 历史语义回归：中文子串、长词内含子串都必须继续命中
		{"中文子串", "迁移数据库 schema", "迁移", true},
		{"长词内含", "rearchitecture the module", "architecture", true},
		// 误报护栏。注意：负例的 keyword 不能是 goal 的子串，否则会被第一级
		// 子串匹配命中（历史语义），护栏就测不到了。
		{"短词不归一：use 不命中 using", "using the cache", "use", false},
		{"词干过短不归一：action 不命中 acts", "he acts quickly", "action", false},
		{"词干过短放弃：question 不命中 quest", "the quest begins", "question", false},
		{"不同词源不误命中（protocol/prototype）", "prototype the flow", "protocol", false},
		{"不同词源不误命中（permit/permission）", "permit the request", "permission", false},
		{"多词条目仍只走子串", "the rate is limited", "rate limit", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := matchedKeywords(normalizeKeywordText(tc.goal), []string{tc.keyword})
			if tc.hit {
				assert.Equal(t, []string{tc.keyword}, got)
				return
			}
			assert.Empty(t, got)
		})
	}
}

// TestResolveExplicitDifficulty_StemVariantPromotes 是词形归一的端到端验收：
// goal 只写 "migrate"（词表里是 "migration"）也必须被识别为迁移任务并升档，
// 且告警仍按词表条目记录——按词调优的方式不变。
func TestResolveExplicitDifficulty_StemVariantPromotes(t *testing.T) {
	strong := resolveWithTask(t, promotionConfig(nil), TaskHint{
		Difficulty: DifficultyEasy,
		Goal:       "migrate the auth tables and rotate credentials",
	})
	assert.Equal(t, DifficultyHard, strong.Difficulty)
	assert.Equal(t, SourceExplicitPromoted, strong.DifficultySource)
	assert.Equal(t, "local", strong.Provider)
	assert.Equal(t, "hard-model", strong.Model)
	assert.Contains(t, strong.Warnings, "difficulty_promoted_by_keyword:migration")

	// 弱信号词同样享受词形归一：refactoring + boundaries 两个命中触发升档。
	combo := resolveWithTask(t, promotionConfig(nil), TaskHint{
		Difficulty: DifficultyEasy,
		Goal:       "refactoring the boundaries between modules",
	})
	assert.Equal(t, DifficultyHard, combo.Difficulty)
	assert.Equal(t, SourceExplicitPromoted, combo.DifficultySource)
	assert.Contains(t, combo.Warnings, "difficulty_promoted_by_keyword:refactor")
	assert.Contains(t, combo.Warnings, "difficulty_promoted_by_keyword:boundary")
}
