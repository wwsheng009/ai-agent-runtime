package providercompat

import (
	"github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	llmadapter "github.com/wwsheng009/ai-agent-runtime/internal/llm/adapter"
	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

// Adapter handles compatibility rules for a provider under a protocol.
type Adapter interface {
	Name() string
	Match(Context) bool
	DefaultRuntimeCapability(Context) (agentconfig.ModelCapabilitySpec, bool)
	DefaultLoginReasoningEfforts(Context) ([]string, bool)
	LoginModelHint(Context, string) (bool, bool)
	LoginModelUsesDefaultReasoningEfforts(Context, string) (bool, bool)
	LoginUsesWildcardReasoningEfforts(Context) (bool, bool)
	NormalizeOpenAICompatibleMessages(Context, []map[string]interface{}) ([]map[string]interface{}, bool)
	PrepareRequestBody(Context, map[string]interface{}) (map[string]interface{}, bool)
	NormalizeAssistantMessage(Context, map[string]interface{}) (map[string]interface{}, bool)
	NormalizeProcessResult(Context, *llmadapter.ProcessResult) bool
	NormalizeStreamChunk(Context, map[string]interface{}) (map[string]interface{}, bool)
	ReplayableOpenAIReasoningContent(Context, []map[string]interface{}, *types.ReasoningBlock) (string, bool)
	SupportsMaxOutputTokens(Context) (bool, bool)
}

// BaseAdapter provides no-op implementations so provider adapters can override
// only the compatibility points they own.
type BaseAdapter struct{}

func (BaseAdapter) DefaultRuntimeCapability(Context) (agentconfig.ModelCapabilitySpec, bool) {
	return agentconfig.ModelCapabilitySpec{}, false
}

func (BaseAdapter) DefaultLoginReasoningEfforts(Context) ([]string, bool) {
	return nil, false
}

func (BaseAdapter) LoginModelHint(Context, string) (bool, bool) {
	return false, false
}

func (BaseAdapter) LoginModelUsesDefaultReasoningEfforts(Context, string) (bool, bool) {
	return false, false
}

func (BaseAdapter) LoginUsesWildcardReasoningEfforts(Context) (bool, bool) {
	return false, false
}

func (BaseAdapter) NormalizeOpenAICompatibleMessages(_ Context, messages []map[string]interface{}) ([]map[string]interface{}, bool) {
	return messages, false
}

func (BaseAdapter) PrepareRequestBody(_ Context, body map[string]interface{}) (map[string]interface{}, bool) {
	return body, false
}

func (BaseAdapter) NormalizeAssistantMessage(_ Context, message map[string]interface{}) (map[string]interface{}, bool) {
	return message, false
}

func (BaseAdapter) NormalizeProcessResult(Context, *llmadapter.ProcessResult) bool {
	return false
}

func (BaseAdapter) NormalizeStreamChunk(_ Context, chunk map[string]interface{}) (map[string]interface{}, bool) {
	return chunk, false
}

func (BaseAdapter) ReplayableOpenAIReasoningContent(Context, []map[string]interface{}, *types.ReasoningBlock) (string, bool) {
	return "", false
}

func (BaseAdapter) SupportsMaxOutputTokens(Context) (bool, bool) {
	return false, false
}
