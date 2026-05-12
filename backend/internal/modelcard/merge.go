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

func capabilityIsEmpty(spec agentconfig.ModelCapabilitySpec) bool {
	return len(spec.InputModalities) == 0 &&
		!spec.NativeTools.ImageGeneration &&
		!spec.NativeTools.ImagesGenerationsAPI &&
		!spec.ReasoningModel &&
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
