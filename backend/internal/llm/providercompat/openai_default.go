package providercompat

type openAIDefaultAdapter struct {
	BaseAdapter
}

func (openAIDefaultAdapter) Name() string {
	return "openai-default"
}

func (openAIDefaultAdapter) Match(ctx Context) bool {
	return ctx.Protocol == "openai"
}

func (openAIDefaultAdapter) DefaultLoginReasoningEfforts(Context) ([]string, bool) {
	return []string{"low", "medium", "high", "xhigh", "none"}, true
}

func (openAIDefaultAdapter) LoginModelUsesDefaultReasoningEfforts(_ Context, modelID string) (bool, bool) {
	return LooksLikeOpenAIReasoningModel(modelID), true
}
