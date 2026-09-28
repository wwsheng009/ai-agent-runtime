package commands

import (
	"os"
	"strings"
)

// P2-2（方案 §3.8.3）：运行时交互三级开关。解析优先级：命令级 > 分类级 > 全局。
//
//   - auto（默认）：按注册表执行；
//   - readonly：仅 effect=read 的声明可运行时执行，其余降级 queue；
//   - off：除 block 外全部降级 queue（等价 v1.0 的「不运行时执行」）。
//
// block 只增不减、不可被任何层级放宽。开关在本步骤只提供解析与降级函数，
// 运行时消费由 P2-3 宿主接入。

type runtimeInteractionGlobalMode uint8

const (
	runtimeInteractionAuto runtimeInteractionGlobalMode = iota
	runtimeInteractionReadonly
	runtimeInteractionOff
)

func (m runtimeInteractionGlobalMode) String() string {
	switch m {
	case runtimeInteractionReadonly:
		return "readonly"
	case runtimeInteractionOff:
		return "off"
	default:
		return "auto"
	}
}

const (
	runtimeInteractionEnv           = "AICLI_CHAT_RUNTIME_INTERACTION"
	runtimeInteractionCategoriesEnv = "AICLI_CHAT_RUNTIME_INTERACTION_CATEGORIES"
	runtimeInteractionCommandsEnv   = "AICLI_CHAT_RUNTIME_INTERACTION_COMMANDS"
)

// runtimeSwitchTable 是一次解析出的开关状态；测试可直接构造。
type runtimeSwitchTable struct {
	Global     runtimeInteractionGlobalMode
	Categories map[string]runtimeInteractionGlobalMode
	Commands   map[string]runtimeInteractionGlobalMode
}

func runtimeSwitchTableFromEnv() runtimeSwitchTable {
	return runtimeSwitchTable{
		Global:     parseRuntimeInteractionGlobalMode(os.Getenv(runtimeInteractionEnv)),
		Categories: parseRuntimeInteractionAssignments(os.Getenv(runtimeInteractionCategoriesEnv), false),
		Commands:   parseRuntimeInteractionAssignments(os.Getenv(runtimeInteractionCommandsEnv), true),
	}
}

func parseRuntimeInteractionGlobalMode(raw string) runtimeInteractionGlobalMode {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "readonly", "read-only", "read_only":
		return runtimeInteractionReadonly
	case "off", "disable", "disabled", "0", "false", "no":
		return runtimeInteractionOff
	default:
		return runtimeInteractionAuto
	}
}

// parseRuntimeInteractionAssignments 解析 `C5=off,C11=off`（分类）或
// `/model=off,/exit=auto`（命令）。键在 isCommand 时先做别名归并再小写；
// 非法项忽略（fail-safe：忽略即保持上层开关）。
func parseRuntimeInteractionAssignments(raw string, isCommand bool) map[string]runtimeInteractionGlobalMode {
	out := make(map[string]runtimeInteractionGlobalMode)
	for _, item := range strings.Split(raw, ",") {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		key, value, found := strings.Cut(item, "=")
		if !found {
			continue
		}
		key = strings.ToLower(strings.TrimSpace(key))
		value = strings.TrimSpace(value)
		if key == "" || value == "" {
			continue
		}
		if isCommand {
			key = strings.ToLower(canonicalRuntimeCommandName(key))
		}
		out[key] = parseRuntimeInteractionGlobalMode(value)
	}
	return out
}

// runtimeCommandWithSwitch 返回按三级开关降级后的声明。
// block 不可被放宽；off/readonly 只把 Mode 收敛为 queue，不改变 Effect/Category。
func runtimeCommandWithSwitch(spec runtimeCommandSpec, table runtimeSwitchTable) runtimeCommandSpec {
	if spec.Mode == runtimeModeBlock {
		return spec
	}
	mode := table.Global
	if override, ok := table.Categories[strings.ToLower(spec.Category.String())]; ok {
		mode = override
	}
	if override, ok := table.Commands[strings.ToLower(spec.Command)]; ok {
		mode = override
	}
	switch mode {
	case runtimeInteractionOff:
		spec.Mode = runtimeModeQueue
	case runtimeInteractionReadonly:
		if spec.Effect != runtimeEffectRead {
			spec.Mode = runtimeModeQueue
		}
	}
	return spec
}
