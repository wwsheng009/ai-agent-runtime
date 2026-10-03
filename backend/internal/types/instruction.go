package types

import "strings"

// 抽象指令层（abstract instruction layer）元数据。
//
// 注入点（技能正文、catalog、guide、reminder 等）只产出 canonical 指令消息并声明
// scope/source；最终 wire 角色（system/user/developer...）由协议适配器统一转换。
// 这样所有请求都从抽象统一层出发，新协议只需实现一次转换契约。
// 参见 docs/plan/codex-text-skill-mention-injection-plan-20261002.md §4.12。
const (
	MetaInstructionScope  = "instruction_scope"
	MetaInstructionSource = "instruction_source"

	InstructionScopeSession = "session"
	InstructionScopeTurn    = "turn"

	InstructionSourceSkillsCatalog     = "skills_catalog"
	InstructionSourceSkillInstructions = "skill_instructions"
	InstructionSourceProgramGuide      = "program_guide"
	// InstructionSourceSkillDependencies 承载"技能声明的工具/依赖不可用"的
	// 回合提示（Q11，不阻断、不安装）。
	InstructionSourceSkillDependencies = "skill_dependencies"
)

// NormalizeInstructionScope canonicalizes abstract instruction scope values.
// Unknown/empty values return "" so callers can treat the message as a plain
// (non-instruction-layer) message.
func NormalizeInstructionScope(scope string) string {
	switch strings.ToLower(strings.TrimSpace(scope)) {
	case InstructionScopeSession:
		return InstructionScopeSession
	case InstructionScopeTurn:
		return InstructionScopeTurn
	default:
		return ""
	}
}

// InstructionScopeOf reports the abstract instruction scope carried by a message.
func InstructionScopeOf(message Message) string {
	if message.Metadata == nil {
		return ""
	}
	return NormalizeInstructionScope(message.Metadata.GetString(MetaInstructionScope, ""))
}

// InstructionSourceOf reports the abstract instruction source carried by a message.
func InstructionSourceOf(message Message) string {
	if message.Metadata == nil {
		return ""
	}
	return strings.TrimSpace(message.Metadata.GetString(MetaInstructionSource, ""))
}

// IsInstructionMessage reports whether the message belongs to the abstract
// instruction layer (scope metadata present).
func IsInstructionMessage(message Message) bool {
	return InstructionScopeOf(message) != ""
}
