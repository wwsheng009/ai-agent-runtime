package agentconfig

import (
	"strings"
	"testing"
)

func overlayTestBaseConfig() *Config {
	cfg := &Config{
		Providers: ProvidersConfig{
			DefaultProvider: "base-provider",
			Headers:         map[string]string{"X-Base": "1"},
		},
		AICLI: &AICLIConfig{Theme: &AICLIThemeConfig{Name: "classic"}},
	}
	cfg.ConfigFilePath = "C:/tmp/aicli.yaml"
	cfg.ConfigLayers = []ConfigLayer{{Kind: LayerKindProject, Path: "C:/tmp/.aicli/aicli.yaml", Present: true}}
	cfg.ConfigOrigins = map[string]string{"providers.default_provider": "C:/tmp/aicli.yaml"}
	cfg.ConfigOriginFiles = map[string]string{"providers.default_provider": "C:/tmp/aicli.yaml"}
	cfg.ConfigMergeMode = MergeModeOn
	return cfg
}

func TestApplyConfigOverlayYAMLMergesSparseKeysWithoutTouchingBase(t *testing.T) {
	base := overlayTestBaseConfig()
	overlay := []byte("providers:\n  default_provider: profile-provider\naicli:\n  theme:\n    name: contrast\n")

	merged, changed, err := ApplyConfigOverlayYAML(base, overlay)
	if err != nil {
		t.Fatalf("apply overlay: %v", err)
	}
	if !changed || merged == nil {
		t.Fatalf("overlay must report a change: merged=%#v changed=%v", merged, changed)
	}
	if merged == base {
		t.Fatalf("overlay must return a fresh config, not the base pointer")
	}
	if got := merged.Providers.DefaultProvider; got != "profile-provider" {
		t.Fatalf("providers.default_provider = %q, want profile-provider", got)
	}
	if merged.AICLI == nil || merged.AICLI.Theme == nil || merged.AICLI.Theme.Name != "contrast" {
		t.Fatalf("aicli.theme.name not overlaid: %#v", merged.AICLI)
	}
	// 未写键必须保持基线值（稀疏合并，不整表替换）。
	if got := merged.Providers.Headers["X-Base"]; got != "1" {
		t.Fatalf("unwritten key lost: %#v", merged.Providers.Headers)
	}
	// 反证：基线不得被就地修改。
	if got := base.Providers.DefaultProvider; got != "base-provider" {
		t.Fatalf("base was mutated: %q", got)
	}
	if base.AICLI == nil || base.AICLI.Theme == nil || base.AICLI.Theme.Name != "classic" {
		t.Fatalf("base theme was mutated: %#v", base.AICLI)
	}
	// yaml:"-" 运行期字段必须随覆盖视图一起携带，否则写回路由会失效。
	if merged.ConfigFilePath != base.ConfigFilePath {
		t.Fatalf("ConfigFilePath = %q, want %q", merged.ConfigFilePath, base.ConfigFilePath)
	}
	if merged.ConfigMergeMode != MergeModeOn {
		t.Fatalf("ConfigMergeMode = %q, want %q", merged.ConfigMergeMode, MergeModeOn)
	}
	if len(merged.ConfigLayers) != len(base.ConfigLayers) {
		t.Fatalf("ConfigLayers = %#v, want %#v", merged.ConfigLayers, base.ConfigLayers)
	}
	if merged.ConfigOrigins["providers.default_provider"] != base.ConfigOrigins["providers.default_provider"] {
		t.Fatalf("ConfigOrigins = %#v", merged.ConfigOrigins)
	}
	if merged.ConfigOriginFiles["providers.default_provider"] != base.ConfigOriginFiles["providers.default_provider"] {
		t.Fatalf("ConfigOriginFiles = %#v", merged.ConfigOriginFiles)
	}
}

func TestApplyConfigOverlayYAMLNoOpKeepsBase(t *testing.T) {
	base := overlayTestBaseConfig()
	cases := map[string][]byte{
		"nil overlay":       nil,
		"empty overlay":     []byte("   \n"),
		"empty map overlay": []byte("{}\n"),
		"same value":        []byte("providers:\n  default_provider: base-provider\n"),
	}
	for name, overlay := range cases {
		merged, changed, err := ApplyConfigOverlayYAML(base, overlay)
		if err != nil {
			t.Fatalf("%s: apply overlay: %v", name, err)
		}
		if changed || merged != nil {
			t.Fatalf("%s: no-op overlay must return (nil,false): merged=%#v changed=%v", name, merged, changed)
		}
	}
	// nil base 是空操作，不能 panic。
	if merged, changed, err := ApplyConfigOverlayYAML(nil, []byte("providers:\n  default_provider: x\n")); err != nil || changed || merged != nil {
		t.Fatalf("nil base must be a no-op: merged=%#v changed=%v err=%v", merged, changed, err)
	}
}

func TestApplyConfigOverlayYAMLRejectsInvalidOverlay(t *testing.T) {
	base := overlayTestBaseConfig()

	// 解码失败：providers.timeout 是 time.Duration，字符串无法解码。
	if _, _, err := ApplyConfigOverlayYAML(base, []byte("providers:\n  timeout: not-a-duration\n")); err == nil {
		t.Fatalf("invalid scalar must fail the overlay merge")
	}
	// 校验失败：enabled 的路由配置带非法 compatibility_mode。
	invalidRouting := []byte("aicli:\n  subagents:\n    routing:\n      enabled: true\n      compatibility_mode: bogus\n")
	_, _, err := ApplyConfigOverlayYAML(base, invalidRouting)
	if err == nil {
		t.Fatalf("invalid routing overlay must fail validation")
	}
	if !strings.Contains(err.Error(), "compatibility_mode") {
		t.Fatalf("error must point at the offending key: %v", err)
	}
	// 失败路径不得污染基线。
	if got := base.Providers.DefaultProvider; got != "base-provider" {
		t.Fatalf("base was mutated by a failed overlay: %q", got)
	}
}
