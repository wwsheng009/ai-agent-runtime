package llm

import (
	"strings"
	"unicode"
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
	switch t.strategy {
	case "openai":
		return t.countOpenAITokens(text)
	case "anthropic":
		return t.countAnthropicTokens(text)
	default:
		return t.countSimple(text)
	}
}

// MessagesTokenCount 计算消息的 Token 数量（包括元数据）
const (
	TokenPerMessage = 4      // 每条消息的元数据开销
	TokenPerName    = 1      // 每个名称字段的 Token 开销
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

// countSimple 简单计数（按单词和字符的混合估算）
func (t *Tokenizer) countSimple(text string) int {
	if text == "" {
		return 0
	}

	// 首先按空格分词
	fields := strings.Fields(text)
	wordCount := len(fields)

	// 如果字符数远大于单词数*4（英文的平均 Token/Word 比），使用字符估算
	charCount := len(text)
	if charCount > wordCount*6 {
		return charCount / 3 // 粗略估计：3个字符约等于1个Token
	}

	// 否则使用单词数 + 其他符号
	return wordCount + charCount/10
}

// countOpenAITokens OpenAI 近似计数（基于 GPT-3/4）
func (t *Tokenizer) countOpenAITokens(text string) int {
	if text == "" {
		return 0
	}

	// OpenAI 的 Tokenization 比较复杂，这里使用近似算法
	// 1. 实际上应该使用 tiktoken 库
	// 2. 粗略估计：英文字符数 / 4
	// 3. 中文字符每个约等于 2 个 Tokens

	var tokens int
	var chineseChars int

	for _, r := range text {
		if r >= unicode.MaxASCII {
			// 非ASCII字符（包括中文）
			chineseChars++
		} else {
			// ASCII 字符
			tokens++
		}
	}

	// 中文每个字符约 2 tokens
	tokens += chineseChars * 2

	// 英文大约 4 字符 = 1 token
	if len(text) > chineseChars {
		tokens += (len(text) - chineseChars) / 4
	}

	return tokens
}

// countAnthropicTokens Anthropic 近似计数（基于 Claude）
func (t *Tokenizer) countAnthropicTokens(text string) int {
	if text == "" {
		return 0
	}

	// Anthropic 和 OpenAI 的 Tokenization 类似但略有不同
	// 1. Claude 的 tokenization 使用自己的算法
	// 2. 粗略估计：英文字符数 / 3.5
	// 3. 中文字符每个约等于 1.5-2 个 Tokens

	var tokens int
	var chineseChars int

	for _, r := range text {
		if r >= unicode.MaxASCII {
			chineseChars++
		} else {
			tokens++
		}
	}

	// 中文每个字符约 1.5 tokens
	tokens += chineseChars * 3 / 2

	// 英文大约 3.5 字符 = 1 token
	if len(text) > chineseChars {
		tokens += (len(text) - chineseChars) * 2 / 7
	}

	return tokens
}

// CountTokensWithStrategy 使用指定策略计算 Token 数
func (t *Tokenizer) CountTokensWithStrategy(text, strategy string) int {
	if strategy == "" {
		strategy = t.strategy
	}

	switch strategy {
	case "openai":
		return t.countOpenAITokens(text)
	case "anthropic":
		return t.countAnthropicTokens(text)
	default:
		return t.countSimple(text)
	}
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
