package types

// TokenUsage Token 使用统计
type TokenUsage struct {
	UsageSource         string `json:"usage_source,omitempty" yaml:"usage_source,omitempty"`
	PromptTokens        int    `json:"prompt_tokens" yaml:"prompt_tokens"`
	CompletionTokens    int    `json:"completion_tokens" yaml:"completion_tokens"`
	TotalTokens         int    `json:"total_tokens" yaml:"total_tokens"`
	CachedTokens        int    `json:"cached_tokens,omitempty" yaml:"cached_tokens,omitempty"`
	CacheReadTokens     int    `json:"cache_read_tokens,omitempty" yaml:"cache_read_tokens,omitempty"`
	CacheCreationTokens int    `json:"cache_creation_tokens,omitempty" yaml:"cache_creation_tokens,omitempty"`
	// UncachedInputTokens 是本次输入中未命中缓存、真机处理的 token 数（前缀缓存
	// 之外的部分）。OpenAI 语义下 = 输入总量 - 缓存命中；Anthropic 语义下
	// input_tokens 本身就不含 cache read/creation，二者相等；DeepSeek 直接上报
	// prompt_cache_miss_tokens。0 表示未观测（旧记录/本地估算）。
	UncachedInputTokens int `json:"uncached_input_tokens,omitempty" yaml:"uncached_input_tokens,omitempty"`
	// InputTotalTokens 是完整输入总量（含缓存命中/写入）。包含式口径
	// （OpenAI/Responses/DeepSeek/Gemini）等于 PromptTokens；不含式口径
	// （Anthropic：input_tokens 不含 cache read/creation）等于
	// prompt + cache_read + cache_creation。命中率等比率的分母用它而不是
	// PromptTokens，否则不含式口径会算出 >100% 的命中率。0 表示未观测
	// （旧记录/本地估算），读取方应退化为 PromptTokens（见 InputTotal）。
	InputTotalTokens      int  `json:"input_total_tokens,omitempty" yaml:"input_total_tokens,omitempty"`
	CacheReadReported     bool `json:"cache_read_reported,omitempty" yaml:"cache_read_reported,omitempty"`
	CacheCreationReported bool `json:"cache_creation_reported,omitempty" yaml:"cache_creation_reported,omitempty"`
	ReasoningTokens       int  `json:"reasoning_tokens,omitempty" yaml:"reasoning_tokens,omitempty"`
}

// Clone 克隆 TokenUsage
func (u *TokenUsage) Clone() *TokenUsage {
	if u == nil {
		return nil
	}
	return &TokenUsage{
		UsageSource:           u.UsageSource,
		PromptTokens:          u.PromptTokens,
		CompletionTokens:      u.CompletionTokens,
		TotalTokens:           u.TotalTokens,
		CachedTokens:          u.CachedTokens,
		CacheReadTokens:       u.CacheReadTokens,
		CacheCreationTokens:   u.CacheCreationTokens,
		UncachedInputTokens:   u.UncachedInputTokens,
		InputTotalTokens:      u.InputTotalTokens,
		CacheReadReported:     u.CacheReadReported,
		CacheCreationReported: u.CacheCreationReported,
		ReasoningTokens:       u.ReasoningTokens,
	}
}

// Add 合并另一个 TokenUsage
func (u *TokenUsage) Add(other *TokenUsage) {
	if other == nil {
		return
	}
	u.PromptTokens += other.PromptTokens
	u.CompletionTokens += other.CompletionTokens
	u.TotalTokens += other.TotalTokens
	u.CachedTokens += other.CachedTokens
	u.CacheReadTokens += other.CacheReadTokens
	u.CacheCreationTokens += other.CacheCreationTokens
	u.UncachedInputTokens += other.UncachedInputTokens
	u.InputTotalTokens += other.InputTotalTokens
	u.CacheReadReported = u.CacheReadReported || other.CacheReadReported
	u.CacheCreationReported = u.CacheCreationReported || other.CacheCreationReported
	u.ReasoningTokens += other.ReasoningTokens
	if other.UsageSource != "" {
		switch {
		case u.UsageSource == "":
			u.UsageSource = other.UsageSource
		case u.UsageSource != other.UsageSource:
			u.UsageSource = "mixed"
		}
	}
}

// IsZero 检查是否为零值
func (u *TokenUsage) IsZero() bool {
	if u == nil {
		return true
	}
	return u.PromptTokens == 0 && u.CompletionTokens == 0 && u.TotalTokens == 0 && u.CachedTokens == 0 && u.CacheReadTokens == 0 && u.CacheCreationTokens == 0 && u.UncachedInputTokens == 0 && u.ReasoningTokens == 0
}

// InputTotal 返回输入总量（含缓存读写的完整口径），用于命中率等比率分母。
// 旧记录无 InputTotalTokens 时用 uncached + cache_read 推导（包含式口径下
// 恰好等于 PromptTokens；不含式口径下输入本身即未缓存，可得到正确的输入总量；
// 唯一低估场景是旧的不含式记录同时有 cache_creation，仅影响历史数据）。
func (u *TokenUsage) InputTotal() int {
	if u == nil {
		return 0
	}
	if u.InputTotalTokens > 0 {
		return u.InputTotalTokens
	}
	if derived := u.UncachedInputTokens + u.CacheReadTokens; derived > u.PromptTokens {
		return derived
	}
	return u.PromptTokens
}
