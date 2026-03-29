package types

import (
	"encoding/json"
	"strings"

	anthropictypes "github.com/ai-gateway/ai-agent-runtime/internal/types/anthropic"
)

type ThinkingConfig = anthropictypes.Thinking

const (
	thinkingMetadataKey            = "thinking"
	reasoningEffortMetadataKey     = "reasoning_effort"
	reasoningEffortMetadataAltKey  = "reasoningEffort"
	reasoningConfigMetadataKey     = "reasoning"
	reasoningConfigEffortFieldName = "effort"
)

func CloneThinkingConfig(thinking *ThinkingConfig) *ThinkingConfig {
	if thinking == nil {
		return nil
	}

	cloned := *thinking
	if thinking.BudgetTokens != nil {
		budget := *thinking.BudgetTokens
		cloned.BudgetTokens = &budget
	}
	return &cloned
}

func NormalizeReasoningEffort(effort string) string {
	return strings.ToLower(strings.TrimSpace(effort))
}

func ResolveThinkingConfig(explicit *ThinkingConfig, containers ...map[string]interface{}) *ThinkingConfig {
	if explicit != nil {
		return CloneThinkingConfig(explicit)
	}
	for _, container := range containers {
		if len(container) == 0 {
			continue
		}
		if thinking := parseThinkingConfig(container[thinkingMetadataKey]); thinking != nil {
			return thinking
		}
	}
	return nil
}

func ResolveReasoningEffort(explicit string, containers ...map[string]interface{}) string {
	if effort := NormalizeReasoningEffort(explicit); effort != "" {
		return effort
	}
	for _, container := range containers {
		if len(container) == 0 {
			continue
		}
		for _, key := range []string{reasoningEffortMetadataKey, reasoningEffortMetadataAltKey} {
			if effort, ok := container[key].(string); ok {
				if normalized := NormalizeReasoningEffort(effort); normalized != "" {
					return normalized
				}
			}
		}

		reasoning, ok := container[reasoningConfigMetadataKey].(map[string]interface{})
		if !ok {
			continue
		}
		if effort, ok := reasoning[reasoningConfigEffortFieldName].(string); ok {
			if normalized := NormalizeReasoningEffort(effort); normalized != "" {
				return normalized
			}
		}
	}
	return ""
}

func parseThinkingConfig(raw interface{}) *ThinkingConfig {
	switch value := raw.(type) {
	case nil:
		return nil
	case ThinkingConfig:
		return CloneThinkingConfig(&value)
	case *ThinkingConfig:
		return CloneThinkingConfig(value)
	case map[string]interface{}:
		return thinkingConfigFromMap(value)
	case json.RawMessage:
		return thinkingConfigFromJSONBytes([]byte(value))
	case []byte:
		return thinkingConfigFromJSONBytes(value)
	case string:
		if strings.TrimSpace(value) == "" {
			return nil
		}
		return thinkingConfigFromJSONBytes([]byte(value))
	default:
		return nil
	}
}

func thinkingConfigFromJSONBytes(raw []byte) *ThinkingConfig {
	if len(raw) == 0 {
		return nil
	}
	var thinking ThinkingConfig
	if err := json.Unmarshal(raw, &thinking); err != nil {
		return nil
	}
	if thinking.Type == "" && thinking.Effort == "" && thinking.BudgetTokens == nil {
		return nil
	}
	return &thinking
}

func thinkingConfigFromMap(raw map[string]interface{}) *ThinkingConfig {
	if len(raw) == 0 {
		return nil
	}

	thinking := &ThinkingConfig{}
	if typ, ok := raw["type"].(string); ok {
		thinking.Type = typ
	}
	if effort, ok := raw["effort"].(string); ok {
		thinking.Effort = effort
	}
	if budget := intPtrFromAny(raw["budget_tokens"]); budget != nil {
		thinking.BudgetTokens = budget
	}

	if thinking.Type == "" && thinking.Effort == "" && thinking.BudgetTokens == nil {
		return nil
	}
	return thinking
}

func intPtrFromAny(raw interface{}) *int {
	switch value := raw.(type) {
	case int:
		v := value
		return &v
	case int32:
		v := int(value)
		return &v
	case int64:
		v := int(value)
		return &v
	case float64:
		v := int(value)
		return &v
	case json.Number:
		if parsed, err := value.Int64(); err == nil {
			v := int(parsed)
			return &v
		}
	}
	return nil
}
