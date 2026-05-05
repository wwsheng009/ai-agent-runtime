package providercompat

type chatGPTCodexBackendAdapter struct {
	BaseAdapter
}

func (chatGPTCodexBackendAdapter) Name() string {
	return "codex-chatgpt-backend"
}

func (chatGPTCodexBackendAdapter) Match(ctx Context) bool {
	return IsChatGPTCodexBackend(ctx.BaseURL)
}

func (chatGPTCodexBackendAdapter) SupportsMaxOutputTokens(Context) (bool, bool) {
	return false, true
}

func (chatGPTCodexBackendAdapter) PrepareRequestBody(_ Context, body map[string]interface{}) (map[string]interface{}, bool) {
	if len(body) == 0 {
		return body, false
	}
	if _, exists := body["max_output_tokens"]; !exists {
		return body, false
	}
	normalized := cloneMapStringAny(body)
	delete(normalized, "max_output_tokens")
	return normalized, true
}
