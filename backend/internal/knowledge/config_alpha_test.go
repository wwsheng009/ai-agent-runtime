package knowledge

import "testing"

// TestConfigAlphaDefaultsAndValidation 钉住 α 的配置语义（ADR-0003 §4.2 / 04 §7.6）：
// 缺省走 DefaultShadowAlpha，越界值显式失败，而不是静默夹取。
func TestConfigAlphaDefaultsAndValidation(t *testing.T) {
	if got := DefaultConfig().Normalize().Alpha; got != DefaultShadowAlpha {
		t.Fatalf("default alpha = %v, want %v", got, DefaultShadowAlpha)
	}

	shadow := DefaultConfig().WithWorkspace("ws")
	shadow.Mode = ModeShadow
	shadow.Alpha = 0.6
	if err := shadow.Validate(); err != nil {
		t.Fatalf("Validate(alpha=0.6): %v", err)
	}
	if got := shadow.Normalize().Alpha; got != 0.6 {
		t.Fatalf("normalized alpha = %v, want 0.6（显式配置不得被覆盖）", got)
	}

	invalid := DefaultConfig().WithWorkspace("ws")
	invalid.Alpha = 1.5
	if err := invalid.Validate(); err == nil {
		t.Fatal("alpha=1.5 必须被 Validate 拒绝")
	}

	if got := (*Activation)(nil).Config(); got != (Config{}) {
		t.Fatalf("nil activation Config() = %+v, want zero", got)
	}
}

// TestShadowObserverForUsesConfiguredAlpha 钉住 ShadowObserverFor 把配置里的 α
// 传进观察器（而不是永远用联调初值）。
func TestShadowObserverForUsesConfiguredAlpha(t *testing.T) {
	obs := NewShadowObserver(ShadowConfig{Alpha: 0.6, Mode: ModeShadow})
	if obs.cfg.Alpha != 0.6 {
		t.Fatalf("observer alpha = %v, want 0.6", obs.cfg.Alpha)
	}
	// <=0 时由 NewShadowObserver 兜底到 DefaultShadowAlpha。
	if obs := NewShadowObserver(ShadowConfig{Alpha: 0, Mode: ModeShadow}); obs.cfg.Alpha != DefaultShadowAlpha {
		t.Fatalf("observer default alpha = %v, want %v", obs.cfg.Alpha, DefaultShadowAlpha)
	}
}
