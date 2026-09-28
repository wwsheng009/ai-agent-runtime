package commands

import "testing"

// T29：注册表必须覆盖 catalog 全集（主命令 + 别名归并）。
func TestRuntimeCommandRegistryCoversCatalog(t *testing.T) {
	var missing []string
	for _, spec := range chatSlashCommandCatalog() {
		canonical := canonicalRuntimeCommandName(spec.Name)
		entry, found := runtimeCommandRegistry[canonical]
		if !found {
			missing = append(missing, spec.Name)
			continue
		}
		if entry.Bare == nil && entry.Wildcard == nil && len(entry.Variants) == 0 {
			missing = append(missing, spec.Name+"(empty-entry)")
		}
		for _, alias := range spec.Aliases {
			if got := canonicalRuntimeCommandName(alias); got != canonical {
				missing = append(missing, "alias "+alias+" -> "+got)
			}
		}
	}
	if len(missing) > 0 {
		t.Fatalf("注册表未覆盖 catalog 条目：%v", missing)
	}
}

// T28：注册表不变式——session/process 生效域只允许 queue/block。
func TestRuntimeCommandRegistryInvariants(t *testing.T) {
	if violations := runtimeCommandRegistryViolations(); len(violations) > 0 {
		t.Fatalf("注册表违反 §3.8.1 不变式：%v", violations)
	}
}

func TestResolveRuntimeCommandSpecVariants(t *testing.T) {
	cases := []struct {
		line   string
		mode   runtimeInteractionMode
		effect runtimeEffectScope
		ok     bool
	}{
		// 批次 3：/help 迁入只读 ScreenDocument（副屏），不再内联直写。
		{"/help", runtimeModeScreen, runtimeEffectRead, true},
		{"/?", runtimeModeScreen, runtimeEffectRead, true},
		{"/exit", runtimeModeBlock, runtimeEffectProcess, true},
		{"/quit", runtimeModeBlock, runtimeEffectProcess, true},
		{"/model status", runtimeModeInline, runtimeEffectRead, true},
		{"/model", runtimeModeScreen, runtimeEffectNextTurn, true},
		{"/model gpt-x", runtimeModeScreen, runtimeEffectNextTurn, true},
		{"/debug display", runtimeModeScreen, runtimeEffectRead, true},
		{"/debug status", runtimeModeInline, runtimeEffectRead, true},
		{"/debug nope", runtimeModeQueue, runtimeEffectRead, false},
		{"/goal status", runtimeModeInline, runtimeEffectRead, true},
		{"/goal ship the release", runtimeModeQueue, runtimeEffectSession, true},
		{"/queue", runtimeModeInline, runtimeEffectRead, true},
		{"/queue clear", runtimeModePrompt, runtimeEffectNextTurn, true},
		{"/todos brief", runtimeModeScreen, runtimeEffectRead, true},
		{"/history", runtimeModeScreen, runtimeEffectRead, true},
		{"/h", runtimeModeScreen, runtimeEffectRead, true},
		{"/s", runtimeModePrompt, runtimeEffectLive, true},
		{"/mode:yolo", runtimeModePrompt, runtimeEffectNextCall, true},
		{"/mcp reload", runtimeModeQueue, runtimeEffectNextCall, true},
		{"/no-such-command", runtimeModeQueue, runtimeEffectRead, false},
		{"not-a-command", runtimeModeQueue, runtimeEffectRead, false},
	}
	for _, tc := range cases {
		spec, ok := resolveRuntimeCommandSpec(tc.line)
		if ok != tc.ok {
			t.Fatalf("%q ok=%v，期望 %v（spec=%+v）", tc.line, ok, tc.ok, spec)
		}
		if spec.Mode != tc.mode || spec.Effect != tc.effect {
			t.Fatalf("%q 解析为 mode=%s effect=%s，期望 mode=%s effect=%s",
				tc.line, spec.Mode, spec.Effect, tc.mode, tc.effect)
		}
		if spec.Mode == runtimeModeInline && spec.Effect == runtimeEffectSession {
			t.Fatalf("%q 出现 inline/session 组合（违反不变式）", tc.line)
		}
	}
}

func TestResolveRuntimeCommandSpecSwitchKey(t *testing.T) {
	spec, ok := resolveRuntimeCommandSpec("/model status")
	if !ok {
		t.Fatal("/model status 应登记")
	}
	if spec.SwitchKey != `chat.runtime_interaction.commands."/model"` {
		t.Fatalf("/model 开关键错误：%q", spec.SwitchKey)
	}
	if spec.Category != categoryModelRouting || spec.Category.String() != "C4" {
		t.Fatalf("/model 业务域错误：%v", spec.Category)
	}
	if spec.Confirm {
		t.Fatal("/model status 不应要求确认")
	}
	if bare, _ := resolveRuntimeCommandSpec("/model"); !bare.Confirm {
		t.Fatal("/model 裸命令应要求确认")
	}
}
