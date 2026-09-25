package keymap

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseChordNormalizesModifiersAndNames(t *testing.T) {
	cases := []struct {
		raw  string
		want string
	}{
		{"shift+tab", "shift+tab"},
		{"TAB+SHIFT", "shift+tab"},
		{"alt+m", "alt+m"},
		{"Option+M", "alt+m"},
		{"ctrl+t", "ctrl+t"},
		{"control+shift+Tab", "ctrl+shift+tab"},
		{"Esc", "escape"},
		{"return", "enter"},
		{"ctrl+left", "ctrl+left"},
	}
	for _, tc := range cases {
		chord, err := ParseChord(tc.raw)
		if err != nil {
			t.Fatalf("ParseChord(%q) 失败: %v", tc.raw, err)
		}
		if got := chord.String(); got != tc.want {
			t.Fatalf("ParseChord(%q).String() = %q, want %q", tc.raw, got, tc.want)
		}
	}
}

func TestParseChordRejectsMalformedInput(t *testing.T) {
	for _, raw := range []string{"", "ctrl+", "+a", "ctrl+shift", "f13", "super+x", "a+b"} {
		if _, err := ParseChord(raw); err == nil {
			t.Fatalf("ParseChord(%q) 应当报错", raw)
		}
	}
}

func TestDefaultRegistryResolvesBuiltinBindings(t *testing.T) {
	registry := Load("")
	for _, chord := range []string{"shift+tab", "alt+m"} {
		action, ok := registry.Resolve(chord)
		if !ok || action != ActionPermissionCycle {
			t.Fatalf("Resolve(%q) = %q, %v; want %q, true", chord, action, ok, ActionPermissionCycle)
		}
	}
	if action, ok := registry.Resolve("ctrl+t"); !ok || action != ActionTranscriptPager {
		t.Fatalf("Resolve(ctrl+t) = %q, %v; want %q, true", action, ok, ActionTranscriptPager)
	}
	if _, ok := registry.Resolve("ctrl+q"); ok {
		t.Fatal("未绑定按键不应解析出动作")
	}
}

func TestUserOverrideReplacesDefaultsAndDisablesAction(t *testing.T) {
	path := filepath.Join(t.TempDir(), "keybindings.json")
	content := `{
	  "app.permission.cycle": ["alt+c", "shift+tab"],
	  "app.transcript.pager": []
	}`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("写入配置失败: %v", err)
	}
	registry := Load(path)
	if warnings := registry.Warnings(); len(warnings) != 0 {
		t.Fatalf("合法配置不应产生 warning: %v", warnings)
	}
	if action, ok := registry.Resolve("alt+c"); !ok || action != ActionPermissionCycle {
		t.Fatalf("用户绑定 alt+c 未生效: %q, %v", action, ok)
	}
	if _, ok := registry.Resolve("ctrl+t"); ok {
		t.Fatal("空数组应禁用 app.transcript.pager")
	}
	bindings := registry.Effective()
	byAction := map[Action]Binding{}
	for _, binding := range bindings {
		byAction[binding.Action] = binding
	}
	if got := byAction[ActionTranscriptPager]; got.Source != "user" || len(got.Chords) != 0 {
		t.Fatalf("transcript 绑定 = %+v, want user 来源且无按键", got)
	}
	if got := byAction[ActionPermissionCycle]; len(got.Chords) != 2 || got.Source != "user" {
		t.Fatalf("permission 绑定 = %+v, want 用户来源两键", got)
	}
}

func TestLoadDegradesGracefullyOnBadEntries(t *testing.T) {
	path := filepath.Join(t.TempDir(), "keybindings.json")
	content := `{
	  "app.unknown.action": "ctrl+y",
	  "app.permission.cycle": "not-a-real-key-name",
	  "app.transcript.pager": 42
	}`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("写入配置失败: %v", err)
	}
	registry := Load(path)
	warnings := strings.Join(registry.Warnings(), "\n")
	for _, want := range []string{"app.unknown.action", "app.permission.cycle", "app.transcript.pager"} {
		if !strings.Contains(warnings, want) {
			t.Fatalf("warning 缺少 %q: \n%s", want, warnings)
		}
	}
	// 坏条目只影响自身：其余动作保持默认。
	if action, ok := registry.Resolve("shift+tab"); !ok || action != ActionPermissionCycle {
		t.Fatalf("坏条目不应影响默认绑定: %q, %v", action, ok)
	}
	if action, ok := registry.Resolve("ctrl+t"); !ok || action != ActionTranscriptPager {
		t.Fatalf("坏条目不应影响默认绑定: %q, %v", action, ok)
	}
}

func TestLoadFallsBackOnBrokenJSON(t *testing.T) {
	path := filepath.Join(t.TempDir(), "keybindings.json")
	if err := os.WriteFile(path, []byte(`{"app.permission.cycle": [`), 0o644); err != nil {
		t.Fatalf("写入配置失败: %v", err)
	}
	registry := Load(path)
	if len(registry.Warnings()) == 0 {
		t.Fatal("损坏 JSON 应产生 warning")
	}
	if action, ok := registry.Resolve("shift+tab"); !ok || action != ActionPermissionCycle {
		t.Fatalf("损坏 JSON 应回退默认绑定: %q, %v", action, ok)
	}
}

func TestMissingFileIsNotAWarning(t *testing.T) {
	registry := Load(filepath.Join(t.TempDir(), "missing.json"))
	if warnings := registry.Warnings(); len(warnings) != 0 {
		t.Fatalf("文件缺失是正常路径，不应 warning: %v", warnings)
	}
}
