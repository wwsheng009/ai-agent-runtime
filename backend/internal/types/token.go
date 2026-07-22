package types

// TokenUsage Token 使用统计
type TokenUsage struct {
	PromptTokens          int  `json:"prompt_tokens" yaml:"prompt_tokens"`
	CompletionTokens      int  `json:"completion_tokens" yaml:"completion_tokens"`
	TotalTokens           int  `json:"total_tokens" yaml:"total_tokens"`
	CachedTokens          int  `json:"cached_tokens,omitempty" yaml:"cached_tokens,omitempty"`
	CacheReadTokens       int  `json:"cache_read_tokens,omitempty" yaml:"cache_read_tokens,omitempty"`
	CacheCreationTokens   int  `json:"cache_creation_tokens,omitempty" yaml:"cache_creation_tokens,omitempty"`
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
		PromptTokens:          u.PromptTokens,
		CompletionTokens:      u.CompletionTokens,
		TotalTokens:           u.TotalTokens,
		CachedTokens:          u.CachedTokens,
		CacheReadTokens:       u.CacheReadTokens,
		CacheCreationTokens:   u.CacheCreationTokens,
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
	u.CacheReadReported = u.CacheReadReported || other.CacheReadReported
	u.CacheCreationReported = u.CacheCreationReported || other.CacheCreationReported
	u.ReasoningTokens += other.ReasoningTokens
}

// IsZero 检查是否为零值
func (u *TokenUsage) IsZero() bool {
	if u == nil {
		return true
	}
	return u.PromptTokens == 0 && u.CompletionTokens == 0 && u.TotalTokens == 0 && u.CachedTokens == 0 && u.CacheReadTokens == 0 && u.CacheCreationTokens == 0 && u.ReasoningTokens == 0
}
