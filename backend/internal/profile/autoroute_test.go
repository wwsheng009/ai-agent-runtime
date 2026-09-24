package profile

import (
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
)

// FR-11（Batch 6）：auto 路由是 CLI 与 server 共用的**单一权威**。未配置
// `profiles.auto` 时必须与历史 server 启发式逐字同源，否则"从不开 auto"的既有
// 部署会静默换挡（NFR-1 零变化）。
func TestRouteProfileForPromptMatchesHistoricalHeuristic(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		prompt string
		want   string
	}{
		{"write", "please fix the failing test", "executor"},
		{"implement", "implement the new endpoint", "executor"},
		{"plan", "plan the migration", "planner"},
		{"design", "design the retry strategy", "planner"},
		{"search", "search the repo for callers", "explore"},
		{"understand", "help me understand this module", "explore"},
		{"uppercase", "FIX THE BUG", "executor"},
		{"rule order wins", "plan and then implement it", "executor"},
		{"unmatched falls back", "hello there", AutoRouteDefaultFallback},
		{"empty prompt stays empty", "   ", ""},
		{"empty input stays empty", "", ""},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := RouteProfileForPrompt(tc.prompt, AutoRouteConfig{}); got != tc.want {
				t.Fatalf("RouteProfileForPrompt(%q) = %q, want %q", tc.prompt, got, tc.want)
			}
		})
	}
}

func TestRouteProfileForPromptCustomRulesReplaceDefaults(t *testing.T) {
	t.Parallel()
	cfg := AutoRouteConfig{
		Rules: []AutoRouteRule{
			{Profile: "reviewer", Keywords: []string{"Review", "audit"}},
			{Profile: "planner", Keywords: []string{"plan"}},
		},
		Fallback: "minimal",
	}
	if got := RouteProfileForPrompt("please REVIEW this diff", cfg); got != "reviewer" {
		t.Fatalf("keyword matching must be case-insensitive, got %q", got)
	}
	if got := RouteProfileForPrompt("plan it", cfg); got != "planner" {
		t.Fatalf("second rule must still match, got %q", got)
	}
	// 显式规则整体替换内置映射：内置 executor 规则不再参与匹配。
	if got := RouteProfileForPrompt("write the code", cfg); got != "minimal" {
		t.Fatalf("unmatched prompt must use the configured fallback, got %q", got)
	}
}

func TestRouteProfileForPromptDropsUnusableRules(t *testing.T) {
	t.Parallel()
	cfg := AutoRouteConfig{Rules: []AutoRouteRule{
		{Profile: "   ", Keywords: []string{"ghost"}},
		{Profile: "explore", Keywords: []string{" ", "  FIND  "}},
	}}
	if got := RouteProfileForPrompt("find it", cfg); got != "explore" {
		t.Fatalf("keywords must be trimmed/lowered before matching, got %q", got)
	}
	// 全部规则不可用 → 回落内置默认（而不是"无路由"）。
	empty := AutoRouteConfig{Rules: []AutoRouteRule{{Profile: "", Keywords: []string{"z"}}}}
	if got := RouteProfileForPrompt("hello", empty); got != AutoRouteDefaultFallback {
		t.Fatalf("unusable rules must fall back to the built-in default, got %q", got)
	}
}

func TestNewAutoRouteConfigFromProfilesConfig(t *testing.T) {
	t.Parallel()
	if cfg := NewAutoRouteConfig(nil); len(cfg.Rules) != 0 || cfg.Fallback != "" {
		t.Fatalf("nil config must map to the zero value, got %#v", cfg)
	}
	// 未声明 `profiles.auto` → 零值 → 内置默认（既有部署零变化）。
	plain := NewAutoRouteConfig(&agentconfig.ProfilesConfig{})
	if got := RouteProfileForPrompt("fix it", plain); got != "executor" {
		t.Fatalf("unconfigured auto must keep the historical heuristic, got %q", got)
	}

	cfg := NewAutoRouteConfig(&agentconfig.ProfilesConfig{Auto: &agentconfig.ProfilesAutoConfig{
		Fallback: " minimal ",
		Rules:    []agentconfig.ProfilesAutoRouteRule{{Profile: " reviewer ", Keywords: []string{"audit"}}},
	}})
	if cfg.Fallback != "minimal" {
		t.Fatalf("fallback must be trimmed, got %q", cfg.Fallback)
	}
	if len(cfg.Rules) != 1 || cfg.Rules[0].Profile != "reviewer" {
		t.Fatalf("rule profile must be trimmed, got %#v", cfg.Rules)
	}
	if got := RouteProfileForPrompt("audit the PR", cfg); got != "reviewer" {
		t.Fatalf("configured rule must drive routing, got %q", got)
	}
	if got := RouteProfileForPrompt("hello", cfg); got != "minimal" {
		t.Fatalf("configured fallback must drive routing, got %q", got)
	}
}

func TestIsAutoProfileRef(t *testing.T) {
	t.Parallel()
	for _, value := range []string{"auto", " AUTO ", "Auto"} {
		if !IsAutoProfileRef(value) {
			t.Fatalf("IsAutoProfileRef(%q) must be true", value)
		}
	}
	for _, value := range []string{"", "autos", "executor", "auto2", " absolute"} {
		if IsAutoProfileRef(value) {
			t.Fatalf("IsAutoProfileRef(%q) must be false", value)
		}
	}
}

func TestResolveAutoProfileRef(t *testing.T) {
	t.Parallel()
	if ref, ok := ResolveAutoProfileRef("coding", "fix it", AutoRouteConfig{}); ref != "coding" || ok {
		t.Fatalf("explicit ref must pass through unchanged, got (%q, %v)", ref, ok)
	}
	if ref, ok := ResolveAutoProfileRef(" auto ", "fix it", AutoRouteConfig{}); ref != "executor" || !ok {
		t.Fatalf("auto ref must route by prompt, got (%q, %v)", ref, ok)
	}
	// 不猜：auto + 空提示词返回空串，是否降级由调用方显式决定。
	if ref, ok := ResolveAutoProfileRef("auto", "", AutoRouteConfig{}); ref != "" || !ok {
		t.Fatalf("auto with empty prompt must return an empty ref, got (%q, %v)", ref, ok)
	}
}
