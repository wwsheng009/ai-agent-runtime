package types

// TokenUsage Token 使用统计
type TokenUsage struct {
	PromptTokens     int `json:"prompt_tokens" yaml:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens" yaml:"completion_tokens"`
	TotalTokens      int `json:"total_tokens" yaml:"total_tokens"`
	CachedTokens     int `json:"cached_tokens,omitempty" yaml:"cached_tokens,omitempty"`
	ReasoningTokens  int `json:"reasoning_tokens,omitempty" yaml:"reasoning_tokens,omitempty"`
}

// Clone 克隆 TokenUsage
func (u *TokenUsage) Clone() *TokenUsage {
	if u == nil {
		return nil
	}
	return &TokenUsage{
		PromptTokens:     u.PromptTokens,
		CompletionTokens: u.CompletionTokens,
		TotalTokens:      u.TotalTokens,
		CachedTokens:     u.CachedTokens,
		ReasoningTokens:  u.ReasoningTokens,
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
	u.ReasoningTokens += other.ReasoningTokens
}

// IsZero 检查是否为零值
func (u *TokenUsage) IsZero() bool {
	if u == nil {
		return true
	}
	return u.PromptTokens == 0 && u.CompletionTokens == 0 && u.TotalTokens == 0 && u.CachedTokens == 0 && u.ReasoningTokens == 0
}
