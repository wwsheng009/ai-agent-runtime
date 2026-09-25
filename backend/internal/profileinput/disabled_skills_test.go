package profileinput

import "testing"

// SK-6：disabled_skills 与 profile 选择取交集，deny 优先。
func TestWithDisabledSkills_DenyWinsOverAllow(t *testing.T) {
	base := BuildSkillFilter(ResolvedSkillSelection{
		Allowlist: []string{"alpha", "beta"},
	})
	if base == nil {
		t.Fatal("expected allowlist filter")
	}

	filter := WithDisabledSkills(base, []string{"beta"})
	if filter == nil {
		t.Fatal("expected composed filter")
	}
	if !filter("alpha") {
		t.Fatal("alpha should stay allowed")
	}
	if filter("beta") {
		t.Fatal("disabled skill must be denied even when allowed by profile")
	}
	if filter("gamma") {
		t.Fatal("skill outside the allowlist must stay denied")
	}
}

func TestWithDisabledSkills_NilBaseOnlyBlocksDisabled(t *testing.T) {
	filter := WithDisabledSkills(nil, []string{" Alpha ", ""})
	if filter == nil {
		t.Fatal("expected filter when disabled list is non-empty")
	}
	if filter("alpha") {
		t.Fatal("disabled name matching must be case-insensitive and trimmed")
	}
	if !filter("beta") {
		t.Fatal("non-disabled skill must be allowed when there is no base filter")
	}
}

// 未配置禁用名单时必须保持原过滤器（含 nil），避免改变既有行为（NFR-1）。
func TestWithDisabledSkills_EmptyDisabledKeepsBase(t *testing.T) {
	if got := WithDisabledSkills(nil, nil); got != nil {
		t.Fatal("nil base + empty disabled must stay nil")
	}
	base := BuildSkillFilter(ResolvedSkillSelection{Denylist: []string{"alpha"}})
	got := WithDisabledSkills(base, []string{"  "})
	if got == nil {
		t.Fatal("empty disabled must keep the base filter")
	}
	if got("alpha") {
		t.Fatal("base denylist must keep working")
	}
	if !got("beta") {
		t.Fatal("base denylist must not deny beta")
	}
}
