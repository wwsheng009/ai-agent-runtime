package providercompat

import "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"

type sensenovaOpenAIAdapter struct {
	BaseAdapter
}

func (sensenovaOpenAIAdapter) Name() string {
	return "openai-sensenova"
}

func (sensenovaOpenAIAdapter) Match(ctx Context) bool {
	return IsSensenova(ctx.ProviderName, ctx.BaseURL)
}

func (sensenovaOpenAIAdapter) DefaultRuntimeCapability(Context) (agentconfig.ModelCapabilitySpec, bool) {
	return agentconfig.ModelCapabilitySpec{
		ReasoningModel:   true,
		ReasoningEfforts: []string{"low", "medium", "high", "none"},
	}, true
}

func (sensenovaOpenAIAdapter) DefaultLoginReasoningEfforts(Context) ([]string, bool) {
	return []string{"low", "medium", "high", "none"}, true
}

func (sensenovaOpenAIAdapter) LoginModelUsesDefaultReasoningEfforts(Context, string) (bool, bool) {
	return true, true
}

func (sensenovaOpenAIAdapter) LoginUsesWildcardReasoningEfforts(Context) (bool, bool) {
	return true, true
}

func (sensenovaOpenAIAdapter) NormalizeOpenAICompatibleMessages(_ Context, messages []map[string]interface{}) ([]map[string]interface{}, bool) {
	if len(messages) == 0 {
		return messages, false
	}

	filtered := make([]map[string]interface{}, 0, len(messages))
	var pendingSystem map[string]interface{}
	flushSystem := func() {
		if pendingSystem == nil {
			return
		}
		filtered = append(filtered, pendingSystem)
		pendingSystem = nil
	}

	for _, msg := range messages {
		role, _ := msg["role"].(string)
		if isSystemRole(role) {
			if pendingSystem == nil {
				pendingSystem = cloneMapStringAny(msg)
				continue
			}
			mergeOpenAICompatibleStringContent(pendingSystem, msg)
			continue
		}
		flushSystem()
		filtered = append(filtered, msg)
	}
	flushSystem()

	return filtered, true
}
