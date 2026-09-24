package commands

import (
	"testing"

	config "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	profilesys "github.com/wwsheng009/ai-agent-runtime/internal/profile"
)

func overlayTestSession() (*ChatSession, *config.Config) {
	base := &config.Config{
		Providers: config.ProvidersConfig{DefaultProvider: "base-provider"},
		AICLI:     &config.AICLIConfig{Theme: &config.AICLIThemeConfig{Name: "classic"}},
	}
	return &ChatSession{Config: base}, base
}

func overlayTestOverlay(t *testing.T, value string) *chatProfileConfigOverlay {
	t.Helper()
	overlay := newChatProfileConfigOverlay(&profilesys.ResolvedAgent{Overrides: map[string]interface{}{
		"providers": map[string]interface{}{"default_provider": value},
	}})
	if overlay == nil || !overlay.Active() {
		t.Fatalf("overlay must be active for %q", value)
	}
	return overlay
}

func TestNewChatProfileConfigOverlaySkipsEmptyOverrides(t *testing.T) {
	if got := newChatProfileConfigOverlay(nil); got != nil {
		t.Fatalf("nil resolved agent must not create an overlay: %#v", got)
	}
	if got := newChatProfileConfigOverlay(&profilesys.ResolvedAgent{}); got != nil {
		t.Fatalf("absent overrides must not create an overlay: %#v", got)
	}
	if got := newChatProfileConfigOverlay(&profilesys.ResolvedAgent{Overrides: map[string]interface{}{}}); got != nil {
		t.Fatalf("empty overrides must not create an overlay: %#v", got)
	}
}

func TestChatProfileConfigOverlayKeysAndOrigins(t *testing.T) {
	source := map[string]interface{}{
		"providers": map[string]interface{}{"default_provider": "profile-provider"},
	}
	overlay := newChatProfileConfigOverlay(&profilesys.ResolvedAgent{Overrides: source})
	if overlay == nil || !overlay.Active() {
		t.Fatalf("overlay must be active")
	}
	if len(overlay.Keys) != 1 || overlay.Keys[0] != "providers.default_provider" {
		t.Fatalf("Keys = %#v", overlay.Keys)
	}
	if got := overlay.Origins["providers.default_provider"]; got != "profile" {
		t.Fatalf("Origins = %#v, want profile", overlay.Origins)
	}
	// 覆盖树必须深拷贝：解析结果与会话状态不得共享可变映射。
	overlay.Overrides["providers"].(map[string]interface{})["default_provider"] = "mutated"
	if got := source["providers"].(map[string]interface{})["default_provider"]; got != "profile-provider" {
		t.Fatalf("resolved overrides were mutated through the overlay: %#v", source)
	}
}

func TestApplyProfileConfigOverlayAppliesThenRestoresBaseline(t *testing.T) {
	session, base := overlayTestSession()
	overlay := overlayTestOverlay(t, "profile-provider")

	if err := applyProfileConfigOverlay(session, overlay); err != nil {
		t.Fatalf("apply overlay: %v", err)
	}
	if session.Config == base {
		t.Fatalf("overlay must install a distinct config view")
	}
	if got := session.Config.Providers.DefaultProvider; got != "profile-provider" {
		t.Fatalf("session config = %q, want profile-provider", got)
	}
	// 反证：基线配置不得被就地修改。
	if got := base.Providers.DefaultProvider; got != "base-provider" {
		t.Fatalf("baseline was mutated: %q", got)
	}
	if session.ProfileConfigBase != base {
		t.Fatalf("baseline must be captured on first apply")
	}
	if session.ProfileConfigOverlayApplied != session.Config {
		t.Fatalf("applied view must be tracked for switch/restore")
	}
	if len(session.ProfileConfigOverlayKeys) != 1 || session.ProfileConfigOverlayKeys[0] != "providers.default_provider" {
		t.Fatalf("overlay keys = %#v", session.ProfileConfigOverlayKeys)
	}
	if session.ProfileConfigOverlayOrigins["providers.default_provider"] != "profile" {
		t.Fatalf("overlay origins = %#v", session.ProfileConfigOverlayOrigins)
	}

	// 幂等：重复应用同一覆盖不会叠加，也不会换基线。
	if err := applyProfileConfigOverlay(session, overlay); err != nil {
		t.Fatalf("re-apply overlay: %v", err)
	}
	if got := session.Config.Providers.DefaultProvider; got != "profile-provider" {
		t.Fatalf("re-apply drifted: %q", got)
	}
	if session.ProfileConfigBase != base {
		t.Fatalf("re-apply must keep the original baseline")
	}

	// 切换到无覆盖的 profile：精确还原基线。
	if err := applyProfileConfigOverlay(session, nil); err != nil {
		t.Fatalf("detach overlay: %v", err)
	}
	if session.Config != base {
		t.Fatalf("detach must restore the baseline pointer")
	}
	if session.ProfileConfigOverlayApplied != nil || session.ProfileConfigOverlayKeys != nil || session.ProfileConfigOverlayOrigins != nil {
		t.Fatalf("detach must clear overlay bookkeeping: %#v", session)
	}
}

func TestApplyProfileConfigOverlaySwitchDoesNotStackOverlays(t *testing.T) {
	session, base := overlayTestSession()
	first := overlayTestOverlay(t, "first-provider")
	second := overlayTestOverlay(t, "second-provider")

	if err := applyProfileConfigOverlay(session, first); err != nil {
		t.Fatalf("apply first: %v", err)
	}
	if err := applyProfileConfigOverlay(session, second); err != nil {
		t.Fatalf("apply second: %v", err)
	}
	if got := session.Config.Providers.DefaultProvider; got != "second-provider" {
		t.Fatalf("switch must apply the new overlay: %q", got)
	}
	if session.ProfileConfigBase != base {
		t.Fatalf("switch must keep the pre-overlay baseline")
	}
	// 还原后必须是基线，而不是第一个 profile 的视图。
	if err := applyProfileConfigOverlay(session, nil); err != nil {
		t.Fatalf("detach: %v", err)
	}
	if got := session.Config.Providers.DefaultProvider; got != "base-provider" {
		t.Fatalf("detach must not keep a stale overlay: %q", got)
	}
}

func TestApplyProfileConfigOverlayAdoptsExternallyReplacedConfig(t *testing.T) {
	session, _ := overlayTestSession()
	overlay := overlayTestOverlay(t, "profile-provider")
	if err := applyProfileConfigOverlay(session, overlay); err != nil {
		t.Fatalf("apply overlay: %v", err)
	}

	// 模拟 /config reload 之类的外部整体替换：旧基线作废，新配置成为基线。
	reloaded := &config.Config{Providers: config.ProvidersConfig{DefaultProvider: "reloaded-provider"}}
	session.Config = reloaded
	if err := applyProfileConfigOverlay(session, overlay); err != nil {
		t.Fatalf("apply after reload: %v", err)
	}
	if session.ProfileConfigBase != reloaded {
		t.Fatalf("reload must become the new baseline")
	}
	if got := session.Config.Providers.DefaultProvider; got != "profile-provider" {
		t.Fatalf("overlay must apply on the reloaded config: %q", got)
	}
}

func TestApplyProfileConfigOverlayFailureKeepsSessionConfig(t *testing.T) {
	session, base := overlayTestSession()
	broken := newChatProfileConfigOverlay(&profilesys.ResolvedAgent{Overrides: map[string]interface{}{
		// providers.timeout 是 time.Duration：解码失败，覆盖必须整体拒绝。
		"providers": map[string]interface{}{"timeout": "not-a-duration"},
	}})
	if broken == nil {
		t.Fatalf("broken overlay must be constructed (validation happens at resolve time)")
	}
	if err := applyProfileConfigOverlay(session, broken); err == nil {
		t.Fatalf("invalid overlay must fail")
	}
	if session.Config != base {
		t.Fatalf("failed overlay must leave session config untouched")
	}
	if session.ProfileConfigOverlayApplied != nil {
		t.Fatalf("failed overlay must not be recorded as applied")
	}
}

func TestProfileResolutionConfigPrefersBaseline(t *testing.T) {
	base := &config.Config{}
	view := &config.Config{}
	session := &ChatSession{Config: view, ProfileConfigBase: base}
	if got := profileResolutionConfig(session); got != base {
		t.Fatalf("resolution must use the overlay-free baseline")
	}
	if got := profileResolutionConfig(&ChatSession{Config: view}); got != view {
		t.Fatalf("resolution must fall back to the session config")
	}
	if got := profileResolutionConfig(nil); got != nil {
		t.Fatalf("nil session must resolve to nil config")
	}
}
