package agentconfig

import (
	"reflect"
	"strings"
	"testing"
)

func routingStrPtr(value string) *string { return &value }
func routingIntPtr(value int) *int       { return &value }
func routingBoolPtr(value bool) *bool    { return &value }

// routingTestConfig 构造「配置层已开启且自洽」的基线配置。
func routingTestConfig() *Config {
	return &Config{AICLI: &AICLIConfig{MainAgent: &AICLIMainAgentConfig{Routing: &AICLIMainAgentRoutingConfig{
		Enabled:           true,
		Levels:            []string{"easy", "normal", "hard"},
		DefaultDifficulty: "normal",
		Profiles: map[string]AICLISubagentRouteProfile{
			"easy":   {Provider: "openai", Model: "gpt-4o-mini", ReasoningEffort: "low"},
			"normal": {Provider: "openai", Model: "gpt-4o"},
			"hard":   {Provider: "anthropic", Model: "claude-sonnet-4", ReasoningEffort: "high"},
		},
	}}}}
}

func sessionRoutingOverride(main *AICLISessionMainAgentRoutingOverride) *AICLISessionRoutingOverride {
	return &AICLISessionRoutingOverride{MainAgent: main}
}

func TestResolveMainAgentRoutingFastPathKeepsSamePointer(t *testing.T) {
	cfg := routingTestConfig()
	baseline := EffectiveMainAgentRoutingConfig(cfg)

	res := ResolveMainAgentRouting(cfg, nil, nil, nil)

	if res.Effective != baseline {
		t.Fatal("快路径必须返回配置层的同一指针（M8/REG 前提）")
	}
	if len(res.Warnings) != 0 {
		t.Fatalf("快路径不应产生 warning，got %+v", res.Warnings)
	}
	if got := res.Sources["main_agent.profiles.hard.model"]; got != RoutingSourceConfig {
		t.Fatalf("profiles.hard.model 来源 = %q, want %q", got, RoutingSourceConfig)
	}
	if got := res.Sources["main_agent.allow_expert"]; got != RoutingSourceDefault {
		t.Fatalf("allow_expert 来源 = %q, want %q", got, RoutingSourceDefault)
	}

	empty := ResolveMainAgentRouting(nil, nil, nil, nil)
	if empty.Effective != nil {
		t.Fatalf("无配置时 Effective 应为 nil，got %+v", empty.Effective)
	}
	if got := empty.Sources["main_agent.enabled"]; got != RoutingSourceDefault {
		t.Fatalf("无配置时 enabled 来源 = %q, want %q", got, RoutingSourceDefault)
	}
}

func TestResolveMainAgentRoutingSessionProfileFieldMerge(t *testing.T) {
	cfg := routingTestConfig()
	baseline := EffectiveMainAgentRoutingConfig(cfg)
	before := cloneMainAgentRoutingConfig(baseline)

	override := sessionRoutingOverride(&AICLISessionMainAgentRoutingOverride{
		Profiles: map[string]AICLISessionRouteProfileOverride{
			"hard": {Model: routingStrPtr("claude-opus-4")},
		},
	})
	res := ResolveMainAgentRouting(cfg, override, nil, nil)

	if res.Effective == baseline {
		t.Fatal("存在覆盖时必须返回深拷贝，不得复用配置层指针")
	}
	profile := res.Effective.Profiles["hard"]
	if profile.Model != "claude-opus-4" {
		t.Fatalf("hard.model = %q, want claude-opus-4", profile.Model)
	}
	if profile.ReasoningEffort != "high" {
		t.Fatalf("未覆盖字段必须继承配置层：hard.reasoning_effort = %q, want high", profile.ReasoningEffort)
	}
	if profile.Provider != "anthropic" {
		t.Fatalf("hard.provider = %q, want anthropic（继承）", profile.Provider)
	}
	if got := res.Sources["main_agent.profiles.hard.model"]; got != RoutingSourceSession {
		t.Fatalf("hard.model 来源 = %q, want %q", got, RoutingSourceSession)
	}
	if got := res.Sources["main_agent.profiles.hard.reasoning_effort"]; got != RoutingSourceConfig {
		t.Fatalf("hard.reasoning_effort 来源 = %q, want %q", got, RoutingSourceConfig)
	}
	if !reflect.DeepEqual(before, EffectiveMainAgentRoutingConfig(cfg)) {
		t.Fatal("INV-A5：解析不得原地改写传入配置")
	}
	// 合法覆盖不应产生与覆盖字段相关的 warning（expensive_levels 越界的既有
	// 告警由 ValidateMainAgentRoutingConfig 产生，与本用例无关）。
	for _, warning := range res.Warnings {
		if strings.HasPrefix(warning.Field, "main_agent.profiles.hard") {
			t.Fatalf("合法覆盖不应产生 profile warning，got %+v", res.Warnings)
		}
	}
}

func TestResolveMainAgentRoutingRequestBeatsSession(t *testing.T) {
	cfg := routingTestConfig()
	session := sessionRoutingOverride(&AICLISessionMainAgentRoutingOverride{
		Profiles: map[string]AICLISessionRouteProfileOverride{
			"hard": {Model: routingStrPtr("session-model")},
		},
	})
	request := &AICLIRequestRoutingOverride{
		MainAgent: &AICLISessionMainAgentRoutingOverride{
			Profiles: map[string]AICLISessionRouteProfileOverride{
				"hard": {Model: routingStrPtr("request-model")},
			},
		},
	}
	res := ResolveMainAgentRouting(cfg, session, nil, request)

	if got := res.Effective.Profiles["hard"].Model; got != "request-model" {
		t.Fatalf("hard.model = %q, want request-model（请求层优先）", got)
	}
	if got := res.Sources["main_agent.profiles.hard.model"]; got != RoutingSourceRequest {
		t.Fatalf("hard.model 来源 = %q, want %q", got, RoutingSourceRequest)
	}
}

func TestResolveMainAgentRoutingWorkspaceLayerBelowSession(t *testing.T) {
	cfg := routingTestConfig()
	workspace := &AICLIWorkspaceRoutingPreferences{
		MainAgent: &AICLIMainAgentRoutingConfig{
			Profiles: map[string]AICLISubagentRouteProfile{
				"hard": {Model: "workspace-model", ReasoningEffort: "medium"},
			},
		},
	}
	session := sessionRoutingOverride(&AICLISessionMainAgentRoutingOverride{
		Profiles: map[string]AICLISessionRouteProfileOverride{
			"hard": {Provider: routingStrPtr("google")},
		},
	})
	res := ResolveMainAgentRouting(cfg, session, workspace, nil)

	profile := res.Effective.Profiles["hard"]
	if profile.Model != "workspace-model" {
		t.Fatalf("hard.model = %q, want workspace-model", profile.Model)
	}
	if profile.Provider != "google" {
		t.Fatalf("hard.provider = %q, want google（会话层优先）", profile.Provider)
	}
	if profile.ReasoningEffort != "medium" {
		t.Fatalf("hard.reasoning_effort = %q, want medium（工作区层优先于配置层）", profile.ReasoningEffort)
	}
	if got := res.Sources["main_agent.profiles.hard.model"]; got != RoutingSourceWorkspace {
		t.Fatalf("hard.model 来源 = %q, want %q", got, RoutingSourceWorkspace)
	}
	if got := res.Sources["main_agent.profiles.hard.provider"]; got != RoutingSourceSession {
		t.Fatalf("hard.provider 来源 = %q, want %q", got, RoutingSourceSession)
	}
}

func TestResolveMainAgentRoutingExplicitZeroResetsToDefault(t *testing.T) {
	cfg := routingTestConfig()
	cfg.AICLI.MainAgent.Routing.MinDwellSteps = 7

	override := sessionRoutingOverride(&AICLISessionMainAgentRoutingOverride{
		MinDwellSteps: routingIntPtr(0),
		Enabled:       routingBoolPtr(false),
	})
	res := ResolveMainAgentRouting(cfg, override, nil, nil)

	if res.Effective.MinDwellSteps != DefaultMainAgentMinDwellSteps {
		t.Fatalf("显式 0 应回落到内置默认：min_dwell_steps = %d, want %d",
			res.Effective.MinDwellSteps, DefaultMainAgentMinDwellSteps)
	}
	if got := res.Sources["main_agent.min_dwell_steps"]; got != RoutingSourceSession {
		t.Fatalf("min_dwell_steps 来源 = %q, want %q", got, RoutingSourceSession)
	}
	if res.Effective.Enabled {
		t.Fatal("显式 false 必须生效（指针语义）")
	}
	if got := res.Sources["main_agent.enabled"]; got != RoutingSourceSession {
		t.Fatalf("enabled 来源 = %q, want %q", got, RoutingSourceSession)
	}
}

func TestResolveMainAgentRoutingLadderFallbackDropsInvalidOverride(t *testing.T) {
	cfg := routingTestConfig()
	override := sessionRoutingOverride(&AICLISessionMainAgentRoutingOverride{
		Levels: &[]string{"hard"},
	})
	res := ResolveMainAgentRouting(cfg, override, nil, nil)

	if got := res.Effective.Levels; !reflect.DeepEqual(got, []string{"easy", "normal", "hard"}) {
		t.Fatalf("levels = %v, want 配置层取值（阶梯回退后）", got)
	}
	if got := res.Sources["main_agent.levels"]; got != RoutingSourceConfig {
		t.Fatalf("levels 来源 = %q, want %q", got, RoutingSourceConfig)
	}
	found := false
	for _, warning := range res.Warnings {
		if warning.Field == "main_agent.levels" && strings.Contains(warning.Reason, "not listed in levels") {
			found = true
			if warning.FallbackTo != RoutingSourceConfig {
				t.Fatalf("warning.FallbackTo = %q, want %q", warning.FallbackTo, RoutingSourceConfig)
			}
		}
	}
	if !found {
		t.Fatalf("缺少 levels 回退 warning：%+v", res.Warnings)
	}
	if !res.Effective.Enabled {
		t.Fatal("回退不应改变未被覆盖的 enabled")
	}
}

func TestResolveMainAgentRoutingInvalidLevelEntryIsIgnored(t *testing.T) {
	cfg := routingTestConfig()
	override := sessionRoutingOverride(&AICLISessionMainAgentRoutingOverride{
		Levels: &[]string{"bogus"},
	})
	res := ResolveMainAgentRouting(cfg, override, nil, nil)

	if got := res.Effective.Levels; !reflect.DeepEqual(got, []string{"easy", "normal", "hard"}) {
		t.Fatalf("非法档位不得生效：levels = %v", got)
	}
	if len(res.Warnings) == 0 || res.Warnings[0].Field != "main_agent.levels" {
		t.Fatalf("应记录非法档位 warning，got %+v", res.Warnings)
	}
}

func TestResolveMainAgentRoutingDerivation(t *testing.T) {
	t.Run("allow_expert with finite guard derives expert", func(t *testing.T) {
		cfg := &Config{AICLI: &AICLIConfig{MainAgent: &AICLIMainAgentConfig{Routing: &AICLIMainAgentRoutingConfig{
			Enabled:                      true,
			AllowExpert:                  true,
			MaxConsecutiveExpensiveSteps: 3,
		}}}}
		override := sessionRoutingOverride(&AICLISessionMainAgentRoutingOverride{
			Enabled: routingBoolPtr(true),
		})
		res := ResolveMainAgentRouting(cfg, override, nil, nil)

		if got := res.Effective.Levels; !reflect.DeepEqual(got, []string{"easy", "normal", "hard", "expert"}) {
			t.Fatalf("levels = %v, want [easy normal hard expert]", got)
		}
		if got := res.Sources["main_agent.levels"]; got != RoutingSourceDerived {
			t.Fatalf("levels 来源 = %q, want %q", got, RoutingSourceDerived)
		}
		if got := res.Effective.DefaultDifficulty; got != MainAgentDefaultDifficulty {
			t.Fatalf("default_difficulty = %q, want %q", got, MainAgentDefaultDifficulty)
		}
		if len(res.Warnings) != 0 {
			t.Fatalf("推导成功不应有 warning，got %+v", res.Warnings)
		}
	})

	t.Run("expert is dropped without a finite guard", func(t *testing.T) {
		cfg := &Config{AICLI: &AICLIConfig{MainAgent: &AICLIMainAgentConfig{Routing: &AICLIMainAgentRoutingConfig{
			Enabled:     true,
			AllowExpert: true,
		}}}}
		override := sessionRoutingOverride(&AICLISessionMainAgentRoutingOverride{
			Enabled: routingBoolPtr(true),
		})
		res := ResolveMainAgentRouting(cfg, override, nil, nil)

		if got := res.Effective.Levels; !reflect.DeepEqual(got, []string{"easy", "normal", "hard"}) {
			t.Fatalf("levels = %v, want [easy normal hard]", got)
		}
		found := false
		for _, warning := range res.Warnings {
			if warning.Field == "main_agent.levels" && strings.Contains(warning.Reason, "expert not derived") {
				found = true
			}
		}
		if !found {
			t.Fatalf("缺少 expert 推导失败 warning：%+v", res.Warnings)
		}
	})
}

func TestResolveSubagentRouting(t *testing.T) {
	enabled := true
	cfg := &Config{AICLI: &AICLIConfig{Subagents: &AICLISubagentsConfig{
		Routing: &AICLISubagentRoutingConfig{
			Enabled: &enabled,
			Levels: map[string]AICLISubagentRouteProfile{
				"hard": {Provider: "anthropic", Model: "claude-sonnet-4", ReasoningEffort: "high"},
			},
		},
	}}}
	baseline := subagentRoutingBaseline(cfg)

	fast := ResolveSubagentRouting(cfg, nil, nil, nil)
	if fast.EffectiveSub != baseline {
		t.Fatal("子 Agent 快路径必须返回配置层同一指针")
	}
	if got := fast.SubSources["sub_agent.enabled"]; got != RoutingSourceConfig {
		t.Fatalf("sub_agent.enabled 来源 = %q, want %q", got, RoutingSourceConfig)
	}

	override := &AICLISessionRoutingOverride{SubAgent: &AICLISessionSubAgentRoutingOverride{
		Levels: map[string]AICLISessionRouteProfileOverride{
			"hard": {Model: routingStrPtr("claude-opus-4")},
		},
	}}
	res := ResolveSubagentRouting(cfg, override, nil, nil)

	if res.EffectiveSub == baseline {
		t.Fatal("存在覆盖时必须深拷贝")
	}
	profile := res.EffectiveSub.Levels["hard"]
	if profile.Model != "claude-opus-4" {
		t.Fatalf("hard.model = %q, want claude-opus-4", profile.Model)
	}
	if profile.Provider != "anthropic" || profile.ReasoningEffort != "high" {
		t.Fatalf("未覆盖字段必须继承配置层：%+v", profile)
	}
	if got := res.SubSources["sub_agent.levels.hard.model"]; got != RoutingSourceSession {
		t.Fatalf("hard.model 来源 = %q, want %q", got, RoutingSourceSession)
	}
	if got := baseline.Levels["hard"].Model; got != "claude-sonnet-4" {
		t.Fatalf("配置层不得被改写：hard.model = %q", got)
	}
}

func TestSessionRoutingOverrideEncodeDecodeRoundTrip(t *testing.T) {
	override := &AICLISessionRoutingOverride{MainAgent: &AICLISessionMainAgentRoutingOverride{
		Profiles: map[string]AICLISessionRouteProfileOverride{
			"hard": {Model: routingStrPtr("claude-opus-4"), MaxTokens: routingIntPtr(8192)},
		},
	}}
	encoded, err := EncodeSessionRoutingOverride(override)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	decoded, err := DecodeSessionRoutingOverride(encoded)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !reflect.DeepEqual(override, decoded) {
		t.Fatalf("round trip 不一致：%+v vs %+v", override, decoded)
	}
	if !decoded.HasMainAgentFields() {
		t.Fatal("HasMainAgentFields 应为 true")
	}
	if decoded.HasSubAgentFields() {
		t.Fatal("HasSubAgentFields 应为 false")
	}

	empty, err := DecodeSessionRoutingOverride("  ")
	if err != nil || empty != nil {
		t.Fatalf("空字符串应返回 (nil, nil)，got (%+v, %v)", empty, err)
	}
	if _, err := DecodeSessionRoutingOverride("{not json"); err == nil {
		t.Fatal("非法 JSON 必须报错（M9）")
	}
}
