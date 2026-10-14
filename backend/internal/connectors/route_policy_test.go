package connectors

import "testing"

// TestRoutePolicyHasOverride 锁定 D6 路由策略的判定语义：任一项非空即覆盖。
func TestRoutePolicyHasOverride(t *testing.T) {
	cases := []struct {
		name   string
		route  RoutePolicy
		expect bool
	}{
		{"零值无覆盖", RoutePolicy{}, false},
		{"仅 provider", RoutePolicy{Provider: "workbuddy"}, true},
		{"仅 model", RoutePolicy{Model: "global:deepseek-v4.1-flash"}, true},
		{"仅 reasoning", RoutePolicy{ReasoningEffort: "high"}, true},
		{"空白串不算覆盖", RoutePolicy{Provider: "  ", Model: "  "}, false},
	}
	for _, tc := range cases {
		if got := tc.route.HasOverride(); got != tc.expect {
			t.Fatalf("%s: HasOverride = %v, want %v", tc.name, got, tc.expect)
		}
	}
}
