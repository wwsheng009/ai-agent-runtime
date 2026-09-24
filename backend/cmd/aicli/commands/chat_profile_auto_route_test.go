package commands

import (
	"strings"
	"testing"

	config "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
)

// FR-11（Batch 6）：`--profile auto` 的 CLI 半程。路由结果必须落地为**具体**
// profile（Reference = 已解析引用），这样 `/profile reload`、`/profile save`
// 无需提示词即可复用同一引用；启动期无提示词时不猜、不静默降级 —— 显式报错。
func writeAutoRouteFixtures(t *testing.T, root string) {
	t.Helper()
	for _, name := range []string{"executor", "planner", "explore"} {
		writeProfileCommandFixture(t, root, name, name+" prompt.", "")
	}
}

func TestResolveChatProfileStateAutoRoutesFromPrompt(t *testing.T) {
	profilesRoot := t.TempDir()
	writeAutoRouteFixtures(t, profilesRoot)
	cfg := &config.Config{Profiles: &config.ProfilesConfig{Root: profilesRoot}}

	cases := []struct {
		prompt string
		want   string
	}{
		{"fix the failing test", "executor"},
		{"implement the endpoint", "executor"},
		{"plan the migration", "planner"},
		{"design the retry strategy", "planner"},
		{"search for callers", "explore"},
		{"help me understand this module", "explore"},
		{"hello there", "executor"}, // 未命中 → 内置兜底
	}
	for _, tc := range cases {
		state, err := resolveChatProfileState(cfg, &chatCommandOptions{ProfileFlag: "auto", Message: tc.prompt})
		if err != nil {
			t.Fatalf("prompt %q: resolveChatProfileState: %v", tc.prompt, err)
		}
		if state == nil || !state.Active() {
			t.Fatalf("prompt %q: expected an active profile state", tc.prompt)
		}
		if state.Reference != tc.want {
			t.Fatalf("prompt %q: Reference=%q want %q", tc.prompt, state.Reference, tc.want)
		}
		if state.AutoRoutedFrom != "auto" {
			t.Fatalf("prompt %q: AutoRoutedFrom=%q want auto", tc.prompt, state.AutoRoutedFrom)
		}
	}
}

func TestResolveChatProfileStateAutoRouteRequiresPrompt(t *testing.T) {
	profilesRoot := t.TempDir()
	writeAutoRouteFixtures(t, profilesRoot)
	cfg := &config.Config{Profiles: &config.ProfilesConfig{Root: profilesRoot}}

	_, err := resolveChatProfileState(cfg, &chatCommandOptions{ProfileFlag: "auto"})
	if err == nil {
		t.Fatal("auto without a prompt must fail loudly instead of guessing a profile")
	}
	if !strings.Contains(err.Error(), "auto") || !strings.Contains(err.Error(), "--prompt") {
		t.Fatalf("error must name the ref and the actionable fix, got %q", err.Error())
	}
}

func TestResolveChatProfileStateAutoRouteUsesConfiguredRules(t *testing.T) {
	profilesRoot := t.TempDir()
	writeAutoRouteFixtures(t, profilesRoot)
	writeProfileCommandFixture(t, profilesRoot, "minimal", "Minimal prompt.", "")
	cfg := &config.Config{Profiles: &config.ProfilesConfig{
		Root: profilesRoot,
		Auto: &config.ProfilesAutoConfig{
			Fallback: "minimal",
			Rules:    []config.ProfilesAutoRouteRule{{Profile: "minimal", Keywords: []string{"summarize"}}},
		},
	}}

	state, err := resolveChatProfileState(cfg, &chatCommandOptions{ProfileFlag: "auto", Message: "please summarize the diff"})
	if err != nil {
		t.Fatalf("configured rule: resolveChatProfileState: %v", err)
	}
	if state.Reference != "minimal" {
		t.Fatalf("configured rule must win, got %q", state.Reference)
	}

	// 显式规则整体替换内置映射：内置 write→executor 不再生效，改走配置兜底。
	state, err = resolveChatProfileState(cfg, &chatCommandOptions{ProfileFlag: "auto", Message: "fix the bug"})
	if err != nil {
		t.Fatalf("configured fallback: resolveChatProfileState: %v", err)
	}
	if state.Reference != "minimal" {
		t.Fatalf("configured fallback must be used, got %q", state.Reference)
	}
}

func TestResolveChatProfileStateDefaultProfileAuto(t *testing.T) {
	profilesRoot := t.TempDir()
	writeAutoRouteFixtures(t, profilesRoot)
	cfg := &config.Config{Profiles: &config.ProfilesConfig{Root: profilesRoot, DefaultProfile: "auto"}}

	// 配置默认 auto（用户级设定）+ 启动提示词 → 同样路由。
	state, err := resolveChatProfileState(cfg, &chatCommandOptions{Message: "plan the work"})
	if err != nil {
		t.Fatalf("resolveChatProfileState: %v", err)
	}
	if state.Reference != "planner" || state.AutoRoutedFrom != "auto" {
		t.Fatalf("configured default auto must route, got ref=%q from=%q", state.Reference, state.AutoRoutedFrom)
	}

	// 显式 --profile 优先级不变：直接命中，不产生 auto 归因。
	state, err = resolveChatProfileState(cfg, &chatCommandOptions{ProfileFlag: "explore", Message: "plan the work"})
	if err != nil {
		t.Fatalf("resolveChatProfileState (explicit): %v", err)
	}
	if state.Reference != "explore" || state.AutoRoutedFrom != "" {
		t.Fatalf("explicit ref must win without auto attribution, got ref=%q from=%q", state.Reference, state.AutoRoutedFrom)
	}
}

// 用户写了 auto 就必须看得到最终选了谁（启动摘要 + /profile status 两处警告面）。
func TestProfileAutoRouteNoticeSurfacesAttribution(t *testing.T) {
	profilesRoot := t.TempDir()
	writeAutoRouteFixtures(t, profilesRoot)
	cfg := &config.Config{Profiles: &config.ProfilesConfig{Root: profilesRoot}}

	session := &ChatSession{Config: cfg}
	state, err := resolveChatProfileState(cfg, &chatCommandOptions{ProfileFlag: "auto", Message: "fix it"})
	if err != nil {
		t.Fatalf("resolveChatProfileState: %v", err)
	}
	if !applyProfileStateToChatSession(session, state) {
		t.Fatal("expected profile surface projection")
	}
	notice := profileAutoRouteNotice(session)
	if !strings.Contains(notice, "auto →") || !strings.Contains(notice, "首轮提示词") {
		t.Fatalf("notice must explain the routing origin, got %q", notice)
	}
	status := chatProfileStatusText(session)
	if !strings.Contains(status, "路由: auto →") {
		t.Fatalf("/profile status must surface the auto routing:\n%s", status)
	}

	// 非自动路由：两处警告面逐字零变化（无归因行）。
	plain := &ChatSession{Config: cfg}
	plainState, err := resolveChatProfileState(cfg, &chatCommandOptions{ProfileFlag: "executor", Message: "fix it"})
	if err != nil {
		t.Fatalf("resolveChatProfileState (explicit): %v", err)
	}
	if !applyProfileStateToChatSession(plain, plainState) {
		t.Fatal("expected profile surface projection")
	}
	if notice := profileAutoRouteNotice(plain); notice != "" {
		t.Fatalf("explicit binding must not report a routing origin, got %q", notice)
	}
	if status := chatProfileStatusText(plain); strings.Contains(status, "路由:") {
		t.Fatalf("explicit binding status must stay unchanged:\n%s", status)
	}
}
