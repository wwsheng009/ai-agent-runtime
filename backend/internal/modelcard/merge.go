package modelcard

import (
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
)

func MergeCapability(existing, remote, card, compat agentconfig.ModelCapabilitySpec) agentconfig.ModelCapabilitySpec {
	var merged agentconfig.ModelCapabilitySpec
	fillCapabilityMissing(&merged, existing)
	fillCapabilityMissing(&merged, remote)
	fillCapabilityMissing(&merged, card)
	fillCapabilityMissing(&merged, compat)
	return merged
}

// MergeCapabilityPreferCard makes card-managed fields authoritative, then keeps
// any remaining provider-local fields that the card does not set.
func MergeCapabilityPreferCard(card, existing agentconfig.ModelCapabilitySpec) agentconfig.ModelCapabilitySpec {
	return MergeCapability(card, existing, agentconfig.ModelCapabilitySpec{}, agentconfig.ModelCapabilitySpec{})
}

// MergeCapabilityPreferRemote makes endpoint-declared fields authoritative, then
// keeps any remaining provider-local fields the endpoint does not declare.
//
// 用于「刷新模型元数据」（aicli provider refresh-models / 重新拉取 /models）：
// 端点能声明的字段（input_modalities / reasoning_* / max_context_tokens /
// max_tokens）以端点为权威，可以覆盖配置里由兜底 model card 或旧端点写下的
// 过期值；端点没有的字段（native_tools / auto_compact_* /
// replay_reasoning_content / compact_reasoning_effort ...）保留本地配置。
//
// 与 login 语义（MergeCapability 的 existing 优先）相反：login 期间不动用户
// 已保存的显式配置，刷新命令才做覆盖。
func MergeCapabilityPreferRemote(remote, existing agentconfig.ModelCapabilitySpec) agentconfig.ModelCapabilitySpec {
	return MergeCapability(remote, existing, agentconfig.ModelCapabilitySpec{}, agentconfig.ModelCapabilitySpec{})
}

func CloneCapabilitySpec(input agentconfig.ModelCapabilitySpec) agentconfig.ModelCapabilitySpec {
	if len(input.InputModalities) > 0 {
		input.InputModalities = append([]string(nil), input.InputModalities...)
	}
	if len(input.ReasoningEfforts) > 0 {
		input.ReasoningEfforts = append([]string(nil), input.ReasoningEfforts...)
	}
	if len(input.ReasoningEffortBudgets) > 0 {
		budgets := make(map[string]int, len(input.ReasoningEffortBudgets))
		for key, value := range input.ReasoningEffortBudgets {
			budgets[key] = value
		}
		input.ReasoningEffortBudgets = budgets
	}
	return input
}

func fillCapabilityMissing(target *agentconfig.ModelCapabilitySpec, source agentconfig.ModelCapabilitySpec) {
	if target == nil || capabilityIsEmpty(source) {
		return
	}
	if len(target.InputModalities) == 0 && len(source.InputModalities) > 0 {
		target.InputModalities = append([]string(nil), source.InputModalities...)
	}
	if !target.NativeTools.ImageGeneration && source.NativeTools.ImageGeneration {
		target.NativeTools.ImageGeneration = true
	}
	if !target.NativeTools.ImagesGenerationsAPI && source.NativeTools.ImagesGenerationsAPI {
		target.NativeTools.ImagesGenerationsAPI = true
	}
	if !target.ReasoningModel && source.ReasoningModel {
		target.ReasoningModel = true
	}
	// replay_reasoning_content 是端点行为声明（*bool，nil = 未声明），只补空不覆盖，
	// 与其它字段一致：显式声明必须能在 merge 中存活，否则 login / 刷新会把用户
	// 写下的契约悄悄抹掉。
	if target.ReplayReasoningContent == nil && source.ReplayReasoningContent != nil {
		value := *source.ReplayReasoningContent
		target.ReplayReasoningContent = &value
	}
	if len(target.ReasoningEfforts) == 0 && len(source.ReasoningEfforts) > 0 {
		target.ReasoningEfforts = append([]string(nil), source.ReasoningEfforts...)
	}
	if len(target.ReasoningEffortBudgets) == 0 && len(source.ReasoningEffortBudgets) > 0 {
		target.ReasoningEffortBudgets = make(map[string]int, len(source.ReasoningEffortBudgets))
		for key, value := range source.ReasoningEffortBudgets {
			target.ReasoningEffortBudgets[key] = value
		}
	}
	if strings.TrimSpace(target.DefaultReasoningEffort) == "" && strings.TrimSpace(source.DefaultReasoningEffort) != "" {
		target.DefaultReasoningEffort = strings.TrimSpace(source.DefaultReasoningEffort)
	}
	if target.MaxContextTokens <= 0 && source.MaxContextTokens > 0 {
		target.MaxContextTokens = source.MaxContextTokens
	}
	if target.MaxTokens <= 0 && source.MaxTokens > 0 {
		target.MaxTokens = source.MaxTokens
	}
	if target.AutoCompactRatio <= 0 && source.AutoCompactRatio > 0 {
		target.AutoCompactRatio = source.AutoCompactRatio
	}
	if target.AutoCompactTokenLimit <= 0 && source.AutoCompactTokenLimit > 0 {
		target.AutoCompactTokenLimit = source.AutoCompactTokenLimit
	}
	if strings.TrimSpace(target.AutoCompactMode) == "" && strings.TrimSpace(source.AutoCompactMode) != "" {
		target.AutoCompactMode = strings.TrimSpace(source.AutoCompactMode)
	}
	if !target.SupportsRemoteCompact && source.SupportsRemoteCompact {
		target.SupportsRemoteCompact = true
	}
	if strings.TrimSpace(target.CompactReasoningEffort) == "" && strings.TrimSpace(source.CompactReasoningEffort) != "" {
		target.CompactReasoningEffort = strings.TrimSpace(source.CompactReasoningEffort)
	}
}

func CapabilityFieldNames(spec agentconfig.ModelCapabilitySpec) []string {
	fields := make([]string, 0, 12)
	if len(spec.InputModalities) > 0 {
		fields = append(fields, "input_modalities")
	}
	if spec.NativeTools.ImageGeneration {
		fields = append(fields, "native_tools.image_generation")
	}
	if spec.NativeTools.ImagesGenerationsAPI {
		fields = append(fields, "native_tools.images_generations_api")
	}
	if spec.ReasoningModel {
		fields = append(fields, "reasoning_model")
	}
	if spec.ReplayReasoningContent != nil {
		fields = append(fields, "replay_reasoning_content")
	}
	if len(spec.ReasoningEfforts) > 0 {
		fields = append(fields, "reasoning_efforts")
	}
	if len(spec.ReasoningEffortBudgets) > 0 {
		fields = append(fields, "reasoning_effort_budgets")
	}
	if strings.TrimSpace(spec.DefaultReasoningEffort) != "" {
		fields = append(fields, "default_reasoning_effort")
	}
	if spec.MaxContextTokens > 0 {
		fields = append(fields, "max_context_tokens")
	}
	if spec.MaxTokens > 0 {
		fields = append(fields, "max_tokens")
	}
	if spec.AutoCompactRatio > 0 {
		fields = append(fields, "auto_compact_ratio")
	}
	if spec.AutoCompactTokenLimit > 0 {
		fields = append(fields, "auto_compact_token_limit")
	}
	if strings.TrimSpace(spec.AutoCompactMode) != "" {
		fields = append(fields, "auto_compact_mode")
	}
	if spec.SupportsRemoteCompact {
		fields = append(fields, "supports_remote_compact")
	}
	if strings.TrimSpace(spec.CompactReasoningEffort) != "" {
		fields = append(fields, "compact_reasoning_effort")
	}
	return fields
}

// CapabilitySpecsEqual reports whether two capability specs are semantically equal.
func CapabilitySpecsEqual(a, b agentconfig.ModelCapabilitySpec) bool {
	if !stringSlicesEqualFoldOrder(a.InputModalities, b.InputModalities) {
		return false
	}
	if a.NativeTools.ImageGeneration != b.NativeTools.ImageGeneration ||
		a.NativeTools.ImagesGenerationsAPI != b.NativeTools.ImagesGenerationsAPI {
		return false
	}
	if a.ReasoningModel != b.ReasoningModel {
		return false
	}
	if !boolPointersEqual(a.ReplayReasoningContent, b.ReplayReasoningContent) {
		return false
	}
	if !stringSlicesEqualFoldOrder(a.ReasoningEfforts, b.ReasoningEfforts) {
		return false
	}
	if !intMapsEqual(a.ReasoningEffortBudgets, b.ReasoningEffortBudgets) {
		return false
	}
	if strings.TrimSpace(a.DefaultReasoningEffort) != strings.TrimSpace(b.DefaultReasoningEffort) {
		return false
	}
	if a.MaxContextTokens != b.MaxContextTokens ||
		a.MaxTokens != b.MaxTokens ||
		a.AutoCompactRatio != b.AutoCompactRatio ||
		a.AutoCompactTokenLimit != b.AutoCompactTokenLimit ||
		a.SupportsRemoteCompact != b.SupportsRemoteCompact {
		return false
	}
	if strings.TrimSpace(a.AutoCompactMode) != strings.TrimSpace(b.AutoCompactMode) {
		return false
	}
	if strings.TrimSpace(a.CompactReasoningEffort) != strings.TrimSpace(b.CompactReasoningEffort) {
		return false
	}
	return true
}

func stringSlicesEqualFoldOrder(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if !strings.EqualFold(strings.TrimSpace(a[i]), strings.TrimSpace(b[i])) {
			return false
		}
	}
	return true
}

func boolPointersEqual(a, b *bool) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

func intMapsEqual(a, b map[string]int) bool {
	if len(a) != len(b) {
		return false
	}
	for key, value := range a {
		if b[key] != value {
			return false
		}
	}
	return true
}

func capabilityIsEmpty(spec agentconfig.ModelCapabilitySpec) bool {
	return len(spec.InputModalities) == 0 &&
		!spec.NativeTools.ImageGeneration &&
		!spec.NativeTools.ImagesGenerationsAPI &&
		!spec.ReasoningModel &&
		spec.ReplayReasoningContent == nil &&
		len(spec.ReasoningEfforts) == 0 &&
		len(spec.ReasoningEffortBudgets) == 0 &&
		strings.TrimSpace(spec.DefaultReasoningEffort) == "" &&
		spec.MaxContextTokens == 0 &&
		spec.MaxTokens == 0 &&
		spec.AutoCompactRatio == 0 &&
		spec.AutoCompactTokenLimit == 0 &&
		strings.TrimSpace(spec.AutoCompactMode) == "" &&
		!spec.SupportsRemoteCompact &&
		strings.TrimSpace(spec.CompactReasoningEffort) == ""
}
