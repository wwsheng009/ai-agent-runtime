package llm

import (
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/tokenestimate"
)

// Tokenizer Token 计数器
type Tokenizer struct {
	strategy string // "openai" | "anthropic" | "simple"
}

// NewTokenizer 创建 Token 计数器
func NewTokenizer(strategy string) *Tokenizer {
	if strategy == "" {
		strategy = "simple"
	}

	return &Tokenizer{
		strategy: strategy,
	}
}

// Count 计算文本的 Token 数量
func (t *Tokenizer) Count(text string) int {
	return tokenestimate.Estimate(text, profileForStrategy(t.strategy))
}

// MessagesTokenCount 计算消息的 Token 数量（包括元数据）
const (
	TokenPerMessage = 3 // 每条消息的元数据开销（OpenAI cookbook: tokensPerMessage=3）
	TokenPerName    = 1 // 每个名称字段的 Token 开销
	// ReplyPrimingTokens 是每次 chat completion 请求的回复引导开销
	// （OpenAI cookbook: every reply is primed with <|start|>assistant<|message|>）。
	ReplyPrimingTokens = 3
)

// CountMessages 计算消息列表的 Token 数量
func (t *Tokenizer) CountMessages(messages []interface{}) int {
	var total int

	for _, msg := range messages {
		// 添加消息元数据开销
		total += TokenPerMessage

		// 处理消息内容
		switch m := msg.(type) {
		case map[string]interface{}:
			if role, ok := m["role"].(string); ok {
				total += t.Count(role)
				total += TokenPerName
			}
			if content, ok := m["content"].(string); ok {
				total += t.Count(content)
			}
			if name, ok := m["name"].(string); ok {
				total += t.Count(name)
				total += TokenPerName
			}
		}
	}

	return total
}

// profileForStrategy maps a tokenizer strategy to the calibrated fallback
// profile. Unknown/OpenAI-compatible providers get the balanced generic
// profile; exact provider-reported usage always takes precedence upstream.
func profileForStrategy(strategy string) tokenestimate.Profile {
	switch strings.ToLower(strings.TrimSpace(strategy)) {
	case "anthropic":
		return tokenestimate.ProfileAnthropic
	default:
		return tokenestimate.ProfileGeneric
	}
}

// CountTokensWithStrategy 使用指定策略计算 Token 数
func (t *Tokenizer) CountTokensWithStrategy(text, strategy string) int {
	if strategy == "" {
		strategy = t.strategy
	}

	return tokenestimate.Estimate(text, profileForStrategy(strategy))
}

// EstimateTotalTokens 估算请求的总 Token 数
func (t *Tokenizer) EstimateTotalTokens(messages []interface{}, tools []interface{}, model string) int {
	total := t.CountMessages(messages)

	// 计算工具的 Token 数
	if len(tools) > 0 {
		for _, tool := range tools {
			switch toolMap := tool.(type) {
			case map[string]interface{}:
				if name, ok := toolMap["name"].(string); ok {
					total += t.Count(name)
				}
				if desc, ok := toolMap["description"].(string); ok {
					total += t.Count(desc)
				}
			}
		}
	}

	// 根据模型调整
	// 某些模型的 token 计算可能略有不同
	switch {
	case strings.Contains(model, "gpt-4"):
	case strings.Contains(model, "claude"):
	default:
	}

	return total
}

// ValidateTokenLimit 验证 Token 数是否在限制内
func (t *Tokenizer) ValidateTokenLimit(messages []interface{}, tools []interface{}, model string, maxTokens int) (bool, int, error) {
	total := t.EstimateTotalTokens(messages, tools, model)

	if total > maxTokens {
		return false, total, nil
	}

	return true, total, nil
}

// TruncateToTokenLimit 截断消息以适应 Token 限制
func (t *Tokenizer) TruncateToTokenLimit(messages []interface{}, tools []interface{}, model string, maxTokens int) ([]interface{}, int, error) {
	total := t.EstimateTotalTokens(messages, tools, model)

	if total <= maxTokens {
		return messages, total, nil
	}

	// 简单实现：移除最旧的消息
	// 实际应该更智能地截断
	if len(messages) <= 1 {
		return messages[:0], total, nil
	}

	for i := 0; i < len(messages); i++ {
		remaining := messages[i+1:]
		remainingTotal := t.EstimateTotalTokens(remaining, tools, model)

		if remainingTotal <= maxTokens {
			return remaining, remainingTotal, nil
		}
	}

	return messages[:0], 0, nil
}
