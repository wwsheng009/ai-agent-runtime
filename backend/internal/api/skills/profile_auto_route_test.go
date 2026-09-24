package skills

import (
	"testing"

	profilesys "github.com/wwsheng009/ai-agent-runtime/internal/profile"
)

// FR-11（Batch 6）：server 侧 auto 路由复用 internal/profile 的单一实现；
// 未配置 `profiles.auto` 时与历史启发式逐字一致（既有部署零变化）。
func TestHandlerRouteAutoProfileForPrompt(t *testing.T) {
	t.Parallel()
	handler := &Handler{}
	cases := []struct{ prompt, want string }{
		{"please fix the failing test", "executor"},
		{"plan the rollout", "planner"},
		{"find usages of this symbol", "explore"},
		{"hello there", "executor"},
		// 不猜：无提示词时不路由（调用方保留自身默认/无 profile 语义）。
		{"", ""},
		{"   ", ""},
	}
	for _, tc := range cases {
		if got := handler.routeAutoProfileForPrompt(tc.prompt); got != tc.want {
			t.Fatalf("routeAutoProfileForPrompt(%q) = %q, want %q", tc.prompt, got, tc.want)
		}
	}

	var nilHandler *Handler
	if got := nilHandler.routeAutoProfileForPrompt("fix it"); got != "" {
		t.Fatalf("nil handler must not panic and must return an empty ref, got %q", got)
	}
}

func TestHandlerRouteAutoProfileHonorsConfiguredRules(t *testing.T) {
	t.Parallel()
	handler := &Handler{}
	handler.SetProfileSupport(ProfileSupportConfig{
		Registry: profilesys.NewRegistry(""),
		AutoRoute: profilesys.AutoRouteConfig{
			Rules:    []profilesys.AutoRouteRule{{Profile: "reviewer", Keywords: []string{"audit"}}},
			Fallback: "minimal",
		},
	})
	if got := handler.routeAutoProfileForPrompt("audit this change"); got != "reviewer" {
		t.Fatalf("configured rule must win, got %q", got)
	}
	// 显式规则整体替换内置映射：内置 executor 规则不再参与匹配。
	if got := handler.routeAutoProfileForPrompt("fix this"); got != "minimal" {
		t.Fatalf("configured fallback must be used, got %q", got)
	}
	if got := handler.routeAutoProfileForPrompt(""); got != "" {
		t.Fatalf("empty prompt must stay unrouted, got %q", got)
	}
}

// SetProfileSupport 必须在设置期快照路由表：调用方后续改动不得影响已配置的 handler。
func TestSetProfileSupportSnapshotsAutoRouteRules(t *testing.T) {
	t.Parallel()
	rules := []profilesys.AutoRouteRule{{Profile: "reviewer", Keywords: []string{"audit"}}}
	handler := &Handler{}
	handler.SetProfileSupport(ProfileSupportConfig{
		AutoRoute: profilesys.AutoRouteConfig{Rules: rules, Fallback: "minimal"},
	})
	rules[0].Profile = "mutated"
	if got := handler.routeAutoProfileForPrompt("audit this"); got != "reviewer" {
		t.Fatalf("rule profile must be snapshotted, got %q", got)
	}
}
