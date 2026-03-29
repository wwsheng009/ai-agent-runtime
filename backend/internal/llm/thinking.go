package llm

import runtimetypes "github.com/ai-gateway/ai-agent-runtime/internal/types"

type ThinkingConfig = runtimetypes.ThinkingConfig

func cloneThinkingConfig(thinking *ThinkingConfig) *ThinkingConfig {
	return runtimetypes.CloneThinkingConfig(thinking)
}

func resolveThinkingConfig(explicit *ThinkingConfig, containers ...map[string]interface{}) *ThinkingConfig {
	return runtimetypes.ResolveThinkingConfig(explicit, containers...)
}

func resolveReasoningEffort(explicit string, containers ...map[string]interface{}) string {
	return runtimetypes.ResolveReasoningEffort(explicit, containers...)
}
