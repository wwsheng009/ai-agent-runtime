package agentconfig

import (
	"fmt"
	"strings"
)

// 主 Agent 动态 provider/model 切换的配置节（方案
// docs/plan/main-agent-dynamic-provider-model-switching-plan-20260921.md §6.1）。
//
// 与 aicli.subagents.routing 的关系：**独立配置节**，两者互不干扰。profiles 复用
// AICLISubagentRouteProfile 的类型（因此自动获得 availability / prompt_cache /
// candidates 的表达能力），但不共享配置实例，主 Agent 的开关不会改变子 Agent 路由。
const (
	// DefaultMainAgentDowngradeConfirmSteps 是降级迟滞的缺省连续确认步数（§5.5）。
	DefaultMainAgentDowngradeConfirmSteps = 3
	// DefaultMainAgentMinDwellSteps 是一次切换后的缺省最小驻留步数（§5.5）。
	DefaultMainAgentMinDwellSteps = 2
	// DefaultMainAgentMaxInvalidReportsPerTurn 是连续非法上报的缺省上限（§6.2）。
	DefaultMainAgentMaxInvalidReportsPerTurn = 3
	// DefaultMainAgentMaxConsecutiveExpensiveSteps 是昂贵档位连续步数的缺省护栏值。
	// 注意：0 在运行期表示「不限」，缺省值只用于文档化推荐值，不由默认值覆盖。
	DefaultMainAgentMaxConsecutiveExpensiveSteps = 6

	// MainAgentCostGuardModeSoft 只告警；MainAgentCostGuardModeHard 触发后本 turn 内不再升级。
	MainAgentCostGuardModeSoft = "soft"
	MainAgentCostGuardModeHard = "hard"

	// MainAgentHealthGateLatchScopeTurn 是唯一允许的闩锁范围（§5.10 规则 1）。
	MainAgentHealthGateLatchScopeTurn = "turn"
	// MainAgentHealthGateOnChainExhaustedBaseline 是唯一允许的候选链耗尽目标
	// （§5.10 规则 3：主 Agent 无父可退，只能降级到基线）。
	MainAgentHealthGateOnChainExhaustedBaseline = "baseline"

	// MainAgentDefaultDifficulty 是未配置 default_difficulty 时的档位（通常等于基线）。
	MainAgentDefaultDifficulty = "normal"
)

// AICLIMainAgentConfig 挂载主 Agent 的执行偏好。
type AICLIMainAgentConfig struct {
	Routing *AICLIMainAgentRoutingConfig `yaml:"routing" mapstructure:"routing"`
}

// AICLIMainAgentHealthGateConfig 描述与在途 provider 健康门禁的耦合方式（§5.10）。
//
// 两个单值字段（LatchScope / OnChainExhausted）不是为可配置性而存在，而是把
// §5.10 的两条硬规则显式化到配置层：写死取值使其在配置评审与 diff 中可见，
// 任何试图改成 step / parent 的改动都会成为一次显式配置变更（且会被校验拦下）。
type AICLIMainAgentHealthGateConfig struct {
	// RespectProviderHealth 为 true 时，turn 开始时读取 providerhealth 门禁结果。
	RespectProviderHealth bool `yaml:"respect_provider_health" mapstructure:"respect_provider_health"`
	// LatchScope 固定为 turn：健康维度在 turn 内闩锁，禁止逐 step 重解析。
	LatchScope string `yaml:"latch_scope" mapstructure:"latch_scope"`
	// OnChainExhausted 固定为 baseline：候选链耗尽时降级到基线，不退向不存在的父。
	OnChainExhausted string `yaml:"on_chain_exhausted" mapstructure:"on_chain_exhausted"`
	// HonorMinDwell 由 ApplyMainAgentRoutingDefaults 无条件置 true（§5.10 规则 2
	// 是硬规则，该字段只用于可见性，配置无法关闭它）。
	HonorMinDwell bool `yaml:"honor_min_dwell" mapstructure:"honor_min_dwell"`
}

// AICLIMainAgentRoutingConfig 把主 Agent 的难度档位映射到 route 偏移，并携带
// turn 级护栏（迟滞、成本、非法上报）。
type AICLIMainAgentRoutingConfig struct {
	Enabled bool `yaml:"enabled" mapstructure:"enabled"`
	// Levels 显式列出可用档位；显式列出即锁定（enabled=true 时不允许为空）。
	Levels            []string `yaml:"levels" mapstructure:"levels"`
	AllowExpert       bool     `yaml:"allow_expert" mapstructure:"allow_expert"`
	DefaultDifficulty string   `yaml:"default_difficulty" mapstructure:"default_difficulty"`
	// AllowEscalationRetry 预留：是否允许升级类上报在同一 step 内重试一次。
	AllowEscalationRetry bool   `yaml:"allow_escalation_retry" mapstructure:"allow_escalation_retry"`
	CostGuardMode        string `yaml:"cost_guard_mode" mapstructure:"cost_guard_mode"`
	// MaxConsecutiveExpensiveSteps 为 0 表示不限；allow_expert=true 时禁止为 0。
	MaxConsecutiveExpensiveSteps int                                  `yaml:"max_consecutive_expensive_steps" mapstructure:"max_consecutive_expensive_steps"`
	ExpensiveLevels              []string                             `yaml:"expensive_levels" mapstructure:"expensive_levels"`
	MaxInvalidReportsPerTurn     int                                  `yaml:"max_invalid_reports_per_turn" mapstructure:"max_invalid_reports_per_turn"`
	DowngradeConfirmSteps        int                                  `yaml:"downgrade_confirm_steps" mapstructure:"downgrade_confirm_steps"`
	MinDwellSteps                int                                  `yaml:"min_dwell_steps" mapstructure:"min_dwell_steps"`
	HealthGate                   AICLIMainAgentHealthGateConfig       `yaml:"health_gate" mapstructure:"health_gate"`
	Profiles                     map[string]AICLISubagentRouteProfile `yaml:"profiles" mapstructure:"profiles"`
}

// EffectiveMainAgentRoutingConfig 返回主 Agent 路由配置；未配置时返回 nil。
func EffectiveMainAgentRoutingConfig(cfg *Config) *AICLIMainAgentRoutingConfig {
	if cfg == nil || cfg.AICLI == nil || cfg.AICLI.MainAgent == nil {
		return nil
	}
	return cfg.AICLI.MainAgent.Routing
}

// ApplyMainAgentRoutingDefaults 把「未配置」的字段回落到文档默认值。
//
// 约定：数值 0 视为未配置（回落默认值），显式负值由校验报错。唯一的例外是
// MaxConsecutiveExpensiveSteps——0 有独立语义（不限），因此不被默认值覆盖。
// HealthGate.HonorMinDwell 无条件置 true：§5.10 规则 2 是硬规则。
func (c *AICLIMainAgentRoutingConfig) ApplyMainAgentRoutingDefaults() {
	if c == nil {
		return
	}
	if c.DowngradeConfirmSteps == 0 {
		c.DowngradeConfirmSteps = DefaultMainAgentDowngradeConfirmSteps
	}
	if c.MinDwellSteps == 0 {
		c.MinDwellSteps = DefaultMainAgentMinDwellSteps
	}
	if c.MaxInvalidReportsPerTurn == 0 {
		c.MaxInvalidReportsPerTurn = DefaultMainAgentMaxInvalidReportsPerTurn
	}
	if strings.TrimSpace(c.CostGuardMode) == "" {
		c.CostGuardMode = MainAgentCostGuardModeSoft
	}
	if len(c.ExpensiveLevels) == 0 {
		c.ExpensiveLevels = []string{"hard", "expert"}
	}
	if strings.TrimSpace(c.DefaultDifficulty) == "" {
		c.DefaultDifficulty = MainAgentDefaultDifficulty
	}
	if strings.TrimSpace(c.HealthGate.LatchScope) == "" {
		c.HealthGate.LatchScope = MainAgentHealthGateLatchScopeTurn
	}
	if strings.TrimSpace(c.HealthGate.OnChainExhausted) == "" {
		c.HealthGate.OnChainExhausted = MainAgentHealthGateOnChainExhaustedBaseline
	}
	c.HealthGate.HonorMinDwell = true
}

// ValidateMainAgentRoutingConfig 校验主 Agent 路由配置（方案 §6.1 的 fail-fast 规则）。
//
// 返回值 warnings 承载「警告并忽略」类结论（当前只有 expensive_levels 越界项），
// 调用方若没有 warning 通道可以丢弃，但函数会就地剔除被忽略的项，运行期无需再判。
// 枚举类与硬约束在 enabled=false 时同样校验：它们描述的是「配置本身是否自洽」，
// 而 level 相关规则只在 enabled=true 时校验，保证默认关闭时零行为变化。
func ValidateMainAgentRoutingConfig(cfg *AICLIMainAgentRoutingConfig) ([]string, error) {
	if cfg == nil {
		return nil, nil
	}
	cfg.ApplyMainAgentRoutingDefaults()

	levels := make(map[string]bool, len(cfg.Levels))
	for _, raw := range cfg.Levels {
		level, ok := normalizeSubagentDifficulty(raw)
		if !ok {
			return nil, fmt.Errorf("invalid aicli.main_agent.routing.levels entry %q", raw)
		}
		levels[level] = true
	}

	if cfg.Enabled && len(levels) == 0 {
		return nil, fmt.Errorf("aicli.main_agent.routing.levels must list the allowed difficulties when enabled")
	}
	if levels["expert"] && !cfg.AllowExpert {
		return nil, fmt.Errorf("aicli.main_agent.routing.levels contains expert but allow_expert=false")
	}
	if cfg.AllowExpert && cfg.MaxConsecutiveExpensiveSteps == 0 {
		return nil, fmt.Errorf("aicli.main_agent.routing.max_consecutive_expensive_steps must be a finite value when allow_expert=true")
	}
	defaultDifficulty, ok := normalizeSubagentDifficulty(cfg.DefaultDifficulty)
	if !ok {
		return nil, fmt.Errorf("invalid aicli.main_agent.routing.default_difficulty %q", cfg.DefaultDifficulty)
	}
	if cfg.Enabled && !levels[defaultDifficulty] {
		return nil, fmt.Errorf("aicli.main_agent.routing.default_difficulty %q is not listed in levels", cfg.DefaultDifficulty)
	}

	warnings := make([]string, 0, len(cfg.ExpensiveLevels))
	if len(cfg.ExpensiveLevels) > 0 {
		kept := make([]string, 0, len(cfg.ExpensiveLevels))
		for _, raw := range cfg.ExpensiveLevels {
			level, ok := normalizeSubagentDifficulty(raw)
			if !ok || !levels[level] {
				warnings = append(warnings, fmt.Sprintf(
					"aicli.main_agent.routing.expensive_levels entry %q is not in levels; ignored", raw))
				continue
			}
			kept = append(kept, level)
		}
		cfg.ExpensiveLevels = kept
	}

	if cfg.DowngradeConfirmSteps < 1 {
		return nil, fmt.Errorf("aicli.main_agent.routing.downgrade_confirm_steps must be >= 1")
	}
	if cfg.MinDwellSteps < 0 {
		return nil, fmt.Errorf("aicli.main_agent.routing.min_dwell_steps cannot be negative")
	}
	if cfg.MaxInvalidReportsPerTurn < 0 {
		return nil, fmt.Errorf("aicli.main_agent.routing.max_invalid_reports_per_turn cannot be negative")
	}
	switch strings.ToLower(strings.TrimSpace(cfg.CostGuardMode)) {
	case MainAgentCostGuardModeSoft, MainAgentCostGuardModeHard:
	default:
		return nil, fmt.Errorf("invalid aicli.main_agent.routing.cost_guard_mode %q", cfg.CostGuardMode)
	}
	if strings.ToLower(strings.TrimSpace(cfg.HealthGate.LatchScope)) != MainAgentHealthGateLatchScopeTurn {
		return nil, fmt.Errorf("aicli.main_agent.routing.health_gate.latch_scope must be %q (turn-scoped latch is a hard rule)",
			MainAgentHealthGateLatchScopeTurn)
	}
	if strings.ToLower(strings.TrimSpace(cfg.HealthGate.OnChainExhausted)) != MainAgentHealthGateOnChainExhaustedBaseline {
		return nil, fmt.Errorf("aicli.main_agent.routing.health_gate.on_chain_exhausted must be %q (main agent has no parent to degrade to)",
			MainAgentHealthGateOnChainExhaustedBaseline)
	}

	for key, profile := range cfg.Profiles {
		level, ok := normalizeSubagentDifficulty(key)
		if !ok {
			return nil, fmt.Errorf("invalid aicli.main_agent.routing.profiles key %q", key)
		}
		if cfg.Enabled && !levels[level] {
			return nil, fmt.Errorf("aicli.main_agent.routing.profiles.%s is not listed in levels", level)
		}
		if err := validateMainAgentRouteProfile("aicli.main_agent.routing.profiles."+level, profile); err != nil {
			return nil, err
		}
	}
	return warnings, nil
}

// validateMainAgentRouteProfile 复用子 Agent 的 profile 校验口径（不 import
// modelrouting，避免 agentconfig → modelrouting → agentconfig 的循环依赖）。
func validateMainAgentRouteProfile(label string, profile AICLISubagentRouteProfile) error {
	if profile.MaxTokens < 0 {
		return fmt.Errorf("%s.max_tokens cannot be negative", label)
	}
	if profile.Timeout < 0 {
		return fmt.Errorf("%s.timeout cannot be negative", label)
	}
	if strings.TrimSpace(profile.Availability) != "" {
		switch strings.ToLower(strings.TrimSpace(profile.Availability)) {
		case "available", "ok", "healthy", "up",
			"degraded", "warn", "warning", "flaky",
			"unavailable", "down", "error", "errored", "dead", "disabled":
		default:
			return fmt.Errorf("%s invalid availability %q", label, profile.Availability)
		}
	}
	for index, candidate := range profile.Candidates {
		candidateLabel := fmt.Sprintf("%s.candidates[%d]", label, index)
		if strings.TrimSpace(candidate.Provider) == "" {
			return fmt.Errorf("%s must set provider", candidateLabel)
		}
		if strings.TrimSpace(candidate.Model) == "" {
			return fmt.Errorf("%s must set model", candidateLabel)
		}
		if strings.TrimSpace(candidate.Availability) != "" {
			switch strings.ToLower(strings.TrimSpace(candidate.Availability)) {
			case "available", "ok", "healthy", "up",
				"degraded", "warn", "warning", "flaky",
				"unavailable", "down", "error", "errored", "dead", "disabled":
			default:
				return fmt.Errorf("%s invalid availability %q", candidateLabel, candidate.Availability)
			}
		}
	}
	return nil
}
