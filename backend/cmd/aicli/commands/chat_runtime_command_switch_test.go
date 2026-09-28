package commands

import "testing"

func allRegisteredRuntimeSpecs() []runtimeCommandSpec {
	var specs []runtimeCommandSpec
	for _, entry := range runtimeCommandRegistry {
		if entry.Bare != nil {
			specs = append(specs, *entry.Bare)
		}
		if entry.Wildcard != nil {
			specs = append(specs, *entry.Wildcard)
		}
		for _, spec := range entry.Variants {
			specs = append(specs, spec)
		}
	}
	return specs
}

// T31：off 时除 block 外全部降级 queue（等价 v1.0 的「不运行时执行」）。
func TestRuntimeCommandSwitchOffDegradesAllToQueue(t *testing.T) {
	table := runtimeSwitchTable{Global: runtimeInteractionOff}
	for _, spec := range allRegisteredRuntimeSpecs() {
		got := runtimeCommandWithSwitch(spec, table)
		if spec.Mode == runtimeModeBlock {
			if got.Mode != runtimeModeBlock {
				t.Fatalf("%s block 不可被开关放宽（实际 %s）", spec.Command, got.Mode)
			}
			continue
		}
		if got.Mode != runtimeModeQueue {
			t.Fatalf("off 时 %s 应降级 queue，实际 %s", spec.Command, got.Mode)
		}
	}
}

// readonly：仅 read 生效域可运行时执行。
func TestRuntimeCommandSwitchReadonlyKeepsReadEffects(t *testing.T) {
	table := runtimeSwitchTable{Global: runtimeInteractionReadonly}

	status, _ := resolveRuntimeCommandSpec("/status")
	// 批次 3：/status 已是只读 ScreenDocument；readonly 只约束生效域，不降级
	// 只读副屏（mode 保持注册表声明的 screen）。
	if got := runtimeCommandWithSwitch(status, table); got.Mode != runtimeModeScreen {
		t.Fatalf("/status(read) 在 readonly 下应保持 screen，实际 %s", got.Mode)
	}
	model, _ := resolveRuntimeCommandSpec("/model")
	if got := runtimeCommandWithSwitch(model, table); got.Mode != runtimeModeQueue {
		t.Fatalf("/model(next-turn) 在 readonly 下应降级 queue，实际 %s", got.Mode)
	}
	streamOn, _ := resolveRuntimeCommandSpec("/stream on")
	if got := runtimeCommandWithSwitch(streamOn, table); got.Mode != runtimeModeQueue {
		t.Fatalf("/stream on(live) 在 readonly 下应降级 queue，实际 %s", got.Mode)
	}
	exit, _ := resolveRuntimeCommandSpec("/exit")
	if got := runtimeCommandWithSwitch(exit, table); got.Mode != runtimeModeBlock {
		t.Fatalf("/exit 必须保持 block，实际 %s", got.Mode)
	}
}

// 优先级：命令级 > 分类级 > 全局。
func TestRuntimeCommandSwitchPrecedence(t *testing.T) {
	model, _ := resolveRuntimeCommandSpec("/model")
	status, _ := resolveRuntimeCommandSpec("/status")

	// 全局 off，分类 C4=auto → /model 恢复注册表声明；其它域仍 queue。
	table := runtimeSwitchTable{
		Global:     runtimeInteractionOff,
		Categories: map[string]runtimeInteractionGlobalMode{"c4": runtimeInteractionAuto},
	}
	if got := runtimeCommandWithSwitch(model, table); got.Mode != runtimeModeScreen {
		t.Fatalf("分类 C4=auto 应恢复 /model 为 screen，实际 %s", got.Mode)
	}
	if got := runtimeCommandWithSwitch(status, table); got.Mode != runtimeModeQueue {
		t.Fatalf("C9 未覆盖时应保持 off 降级，实际 %s", got.Mode)
	}

	// 命令级覆盖分类级：C4=auto 但 /model=readonly → 非 read 变体降级。
	table.Commands = map[string]runtimeInteractionGlobalMode{"/model": runtimeInteractionReadonly}
	if got := runtimeCommandWithSwitch(model, table); got.Mode != runtimeModeQueue {
		t.Fatalf("命令级 readonly 应覆盖分类级 auto，实际 %s", got.Mode)
	}
	modelStatus, _ := resolveRuntimeCommandSpec("/model status")
	if got := runtimeCommandWithSwitch(modelStatus, table); got.Mode != runtimeModeScreen {
		t.Fatalf("read 变体在命令级 readonly 下应保持其声明 Mode（screen），实际 %s", got.Mode)
	}
}

func TestRuntimeSwitchTableFromEnv(t *testing.T) {
	t.Setenv(runtimeInteractionEnv, "")
	t.Setenv(runtimeInteractionCategoriesEnv, "")
	t.Setenv(runtimeInteractionCommandsEnv, "")
	if table := runtimeSwitchTableFromEnv(); table.Global != runtimeInteractionAuto {
		t.Fatalf("默认应为 auto，实际 %s", table.Global)
	}

	t.Setenv(runtimeInteractionEnv, "  OFF ")
	t.Setenv(runtimeInteractionCategoriesEnv, "C4=auto, C11=readonly, garbage")
	t.Setenv(runtimeInteractionCommandsEnv, "/mode:yolo=readonly, /exit=off")
	table := runtimeSwitchTableFromEnv()
	if table.Global != runtimeInteractionOff {
		t.Fatalf("全局解析失败：%s", table.Global)
	}
	if table.Categories["c4"] != runtimeInteractionAuto || table.Categories["c11"] != runtimeInteractionReadonly {
		t.Fatalf("分类解析失败：%+v", table.Categories)
	}
	// 命令键先别名归并：/mode:yolo → /permission-mode；非法项被忽略。
	if table.Commands["/permission-mode"] != runtimeInteractionReadonly {
		t.Fatalf("命令别名归并失败：%+v", table.Commands)
	}
	if _, ok := table.Commands["garbage"]; ok {
		t.Fatalf("非法项不应进入开关表：%+v", table.Commands)
	}
}
