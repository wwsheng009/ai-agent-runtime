package agentconfig

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func boolPtrMainAgent(value bool) *bool { return &value }

func TestApplyMainAgentRoutingDefaults(t *testing.T) {
	cfg := &AICLIMainAgentRoutingConfig{}
	cfg.ApplyMainAgentRoutingDefaults()

	if cfg.DowngradeConfirmSteps != DefaultMainAgentDowngradeConfirmSteps {
		t.Fatalf("downgrade_confirm_steps default = %d, want %d", cfg.DowngradeConfirmSteps, DefaultMainAgentDowngradeConfirmSteps)
	}
	if cfg.MinDwellSteps != DefaultMainAgentMinDwellSteps {
		t.Fatalf("min_dwell_steps default = %d, want %d", cfg.MinDwellSteps, DefaultMainAgentMinDwellSteps)
	}
	if cfg.MaxInvalidReportsPerTurn != DefaultMainAgentMaxInvalidReportsPerTurn {
		t.Fatalf("max_invalid_reports_per_turn default = %d, want %d", cfg.MaxInvalidReportsPerTurn, DefaultMainAgentMaxInvalidReportsPerTurn)
	}
	if cfg.CostGuardMode != MainAgentCostGuardModeSoft {
		t.Fatalf("cost_guard_mode default = %q, want %q", cfg.CostGuardMode, MainAgentCostGuardModeSoft)
	}
	if len(cfg.ExpensiveLevels) != 2 || cfg.ExpensiveLevels[0] != "hard" || cfg.ExpensiveLevels[1] != "expert" {
		t.Fatalf("expensive_levels default = %v, want [hard expert]", cfg.ExpensiveLevels)
	}
	if cfg.DefaultDifficulty != MainAgentDefaultDifficulty {
		t.Fatalf("default_difficulty default = %q, want %q", cfg.DefaultDifficulty, MainAgentDefaultDifficulty)
	}
	if cfg.HealthGate.LatchScope != MainAgentHealthGateLatchScopeTurn {
		t.Fatalf("latch_scope default = %q, want %q", cfg.HealthGate.LatchScope, MainAgentHealthGateLatchScopeTurn)
	}
	if cfg.HealthGate.OnChainExhausted != MainAgentHealthGateOnChainExhaustedBaseline {
		t.Fatalf("on_chain_exhausted default = %q, want %q", cfg.HealthGate.OnChainExhausted, MainAgentHealthGateOnChainExhaustedBaseline)
	}
	if !cfg.HealthGate.HonorMinDwell {
		t.Fatal("honor_min_dwell must be forced to true: §5.10 rule 2 is a hard rule")
	}
	// 0 有独立语义（不限），默认值不得覆盖它。
	if cfg.MaxConsecutiveExpensiveSteps != 0 {
		t.Fatalf("max_consecutive_expensive_steps = %d, want 0 (unlimited)", cfg.MaxConsecutiveExpensiveSteps)
	}
}

func TestValidateMainAgentRoutingConfigRules(t *testing.T) {
	enabled := func(cfg *AICLIMainAgentRoutingConfig) *AICLIMainAgentRoutingConfig {
		cfg.Enabled = true
		return cfg
	}
	base := func() *AICLIMainAgentRoutingConfig {
		return &AICLIMainAgentRoutingConfig{
			Enabled:           true,
			Levels:            []string{"easy", "normal", "hard"},
			DefaultDifficulty: "normal",
			Profiles:          map[string]AICLISubagentRouteProfile{"hard": {ReasoningEffort: "high"}},
		}
	}

	tests := []struct {
		name      string
		cfg       *AICLIMainAgentRoutingConfig
		wantErr   string
		wantWarn  string
		afterFunc func(t *testing.T, cfg *AICLIMainAgentRoutingConfig)
	}{
		{
			name:    "enabled requires explicit levels",
			cfg:     enabled(&AICLIMainAgentRoutingConfig{DefaultDifficulty: "normal"}),
			wantErr: "levels must list",
		},
		{
			name: "expert in levels requires opt-in",
			cfg: func() *AICLIMainAgentRoutingConfig {
				cfg := base()
				cfg.Levels = append(cfg.Levels, "expert")
				return cfg
			}(),
			wantErr: "allow_expert=false",
		},
		{
			name: "allow_expert forbids unlimited expensive steps",
			cfg: func() *AICLIMainAgentRoutingConfig {
				cfg := base()
				cfg.Levels = append(cfg.Levels, "expert")
				cfg.AllowExpert = true
				cfg.MaxConsecutiveExpensiveSteps = 0
				return cfg
			}(),
			wantErr: "must be a finite value",
		},
		{
			name: "default_difficulty must be listed",
			cfg: func() *AICLIMainAgentRoutingConfig {
				cfg := base()
				cfg.DefaultDifficulty = "easy"
				cfg.Levels = []string{"normal", "hard"}
				return cfg
			}(),
			wantErr: "not listed in levels",
		},
		{
			name: "profile key must be listed",
			cfg: func() *AICLIMainAgentRoutingConfig {
				cfg := base()
				cfg.Profiles["easy"] = AICLISubagentRouteProfile{}
				cfg.Levels = []string{"normal", "hard"}
				return cfg
			}(),
			wantErr: "profiles.easy is not listed in levels",
		},
		{
			name: "unknown level entry rejected",
			cfg: func() *AICLIMainAgentRoutingConfig {
				cfg := base()
				cfg.Levels = []string{"normal", "impossible"}
				return cfg
			}(),
			wantErr: "levels entry",
		},
		{
			name: "expensive level outside levels warns and is dropped",
			cfg: func() *AICLIMainAgentRoutingConfig {
				cfg := base()
				cfg.ExpensiveLevels = []string{"hard", "expert"}
				return cfg
			}(),
			wantWarn: "expensive_levels entry",
			afterFunc: func(t *testing.T, cfg *AICLIMainAgentRoutingConfig) {
				if len(cfg.ExpensiveLevels) != 1 || cfg.ExpensiveLevels[0] != "hard" {
					t.Fatalf("expensive_levels after validation = %v, want [hard]", cfg.ExpensiveLevels)
				}
			},
		},
		{
			name: "downgrade confirm steps must be positive",
			cfg: func() *AICLIMainAgentRoutingConfig {
				cfg := base()
				cfg.DowngradeConfirmSteps = -1
				return cfg
			}(),
			wantErr: "downgrade_confirm_steps",
		},
		{
			name: "min dwell steps cannot be negative",
			cfg: func() *AICLIMainAgentRoutingConfig {
				cfg := base()
				cfg.MinDwellSteps = -1
				return cfg
			}(),
			wantErr: "min_dwell_steps",
		},
		{
			name: "max invalid reports cannot be negative",
			cfg: func() *AICLIMainAgentRoutingConfig {
				cfg := base()
				cfg.MaxInvalidReportsPerTurn = -1
				return cfg
			}(),
			wantErr: "max_invalid_reports_per_turn",
		},
		{
			name: "cost guard mode must be an enum value",
			cfg: func() *AICLIMainAgentRoutingConfig {
				cfg := base()
				cfg.CostGuardMode = "turbo"
				return cfg
			}(),
			wantErr: "cost_guard_mode",
		},
		{
			name: "latch scope step is rejected",
			cfg: func() *AICLIMainAgentRoutingConfig {
				cfg := base()
				cfg.HealthGate.LatchScope = "step"
				return cfg
			}(),
			wantErr: "latch_scope must be",
		},
		{
			name: "chain exhausted parent is rejected",
			cfg: func() *AICLIMainAgentRoutingConfig {
				cfg := base()
				cfg.HealthGate.OnChainExhausted = "parent"
				return cfg
			}(),
			wantErr: "on_chain_exhausted must be",
		},
		{
			name: "profile budget must not be negative",
			cfg: func() *AICLIMainAgentRoutingConfig {
				cfg := base()
				cfg.Profiles["hard"] = AICLISubagentRouteProfile{MaxTokens: -1}
				return cfg
			}(),
			wantErr: "max_tokens cannot be negative",
		},
		{
			name: "profile candidate must be complete",
			cfg: func() *AICLIMainAgentRoutingConfig {
				cfg := base()
				cfg.Profiles["hard"] = AICLISubagentRouteProfile{
					Candidates: []AICLISubagentRouteCandidate{{Provider: "openai"}},
				}
				return cfg
			}(),
			wantErr: "must set model",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			warnings, err := ValidateMainAgentRoutingConfig(test.cfg)
			if test.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
			} else if err == nil || !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("error = %v, want containing %q", err, test.wantErr)
			}
			if test.wantWarn != "" {
				found := false
				for _, warning := range warnings {
					if strings.Contains(warning, test.wantWarn) {
						found = true
						break
					}
				}
				if !found {
					t.Fatalf("warnings = %v, want containing %q", warnings, test.wantWarn)
				}
			}
			if test.afterFunc != nil {
				test.afterFunc(t, test.cfg)
			}
		})
	}
}

func TestValidateMainAgentRoutingConfigDisabledIsInert(t *testing.T) {
	// 默认关闭（enabled=false，levels 为空）不得报错：保证零行为变化。
	cfg := &AICLIMainAgentRoutingConfig{Enabled: false}
	if _, err := ValidateMainAgentRoutingConfig(cfg); err != nil {
		t.Fatalf("disabled config must not fail validation: %v", err)
	}
	if _, err := ValidateMainAgentRoutingConfig(nil); err != nil {
		t.Fatalf("nil config must not fail validation: %v", err)
	}
	// 枚举类硬约束在关闭时也要拦下：它们描述配置自洽性。
	cfg = &AICLIMainAgentRoutingConfig{HealthGate: AICLIMainAgentHealthGateConfig{LatchScope: "step"}}
	if _, err := ValidateMainAgentRoutingConfig(cfg); err == nil {
		t.Fatal("latch_scope=step must fail even when disabled")
	}
}

func TestEffectiveMainAgentRoutingConfig(t *testing.T) {
	if got := EffectiveMainAgentRoutingConfig(nil); got != nil {
		t.Fatalf("nil config must return nil, got %+v", got)
	}
	if got := EffectiveMainAgentRoutingConfig(&Config{}); got != nil {
		t.Fatalf("config without aicli must return nil, got %+v", got)
	}
	routing := &AICLIMainAgentRoutingConfig{Enabled: true}
	cfg := &Config{AICLI: &AICLIConfig{MainAgent: &AICLIMainAgentConfig{Routing: routing}}}
	if got := EffectiveMainAgentRoutingConfig(cfg); got != routing {
		t.Fatalf("accessor returned %p, want %p", got, routing)
	}
	// 配置隔离：子 Agent 路由存在时主 Agent 访问器仍只读 main_agent 节。
	cfg.AICLI.Subagents = &AICLISubagentsConfig{Routing: &AICLISubagentRoutingConfig{Enabled: boolPtrMainAgent(true)}}
	if got := EffectiveMainAgentRoutingConfig(cfg); got != routing {
		t.Fatalf("subagent routing must not leak into main agent accessor: %+v", got)
	}
}

func TestMainAgentRoutingConfigYAMLRoundTrip(t *testing.T) {
	document := `
aicli:
  main_agent:
    routing:
      enabled: true
      levels: [easy, normal, hard]
      allow_expert: false
      default_difficulty: normal
      allow_escalation_retry: false
      cost_guard_mode: hard
      max_consecutive_expensive_steps: 6
      expensive_levels: [hard]
      max_invalid_reports_per_turn: 4
      downgrade_confirm_steps: 3
      min_dwell_steps: 2
      health_gate:
        respect_provider_health: true
        latch_scope: turn
        on_chain_exhausted: baseline
        honor_min_dwell: true
      profiles:
        easy:
          reasoning_effort: low
        hard:
          reasoning_effort: high
`
	var cfg Config
	if err := yaml.Unmarshal([]byte(document), &cfg); err != nil {
		t.Fatalf("yaml unmarshal failed: %v", err)
	}
	routing := EffectiveMainAgentRoutingConfig(&cfg)
	if routing == nil {
		t.Fatal("main_agent.routing was not parsed")
	}
	if !routing.Enabled || routing.CostGuardMode != MainAgentCostGuardModeHard {
		t.Fatalf("unexpected routing config: %+v", routing)
	}
	if len(routing.Levels) != 3 || routing.Levels[2] != "hard" {
		t.Fatalf("levels = %v, want [easy normal hard]", routing.Levels)
	}
	if got := routing.Profiles["hard"].ReasoningEffort; got != "high" {
		t.Fatalf("profiles.hard.reasoning_effort = %q, want high", got)
	}
	if !routing.HealthGate.RespectProviderHealth || !routing.HealthGate.HonorMinDwell {
		t.Fatalf("health_gate = %+v, want respect_provider_health/honor_min_dwell true", routing.HealthGate)
	}
	if _, err := ValidateMainAgentRoutingConfig(routing); err != nil {
		t.Fatalf("documented example config must validate: %v", err)
	}
}
