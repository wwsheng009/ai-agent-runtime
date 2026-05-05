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
