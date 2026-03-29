package llm

import (
	"fmt"
	"strings"
)

// TokenEstimator 接口定义了 Token 估算功能
type TokenEstimator interface {
	// EstimateTokens 估算文本的 Token 数量
	EstimateTokens(text string) int
	// EstimateTokensFromMessages 估算消息列表的 Token 数量
	EstimateTokensFromMessages(messages []map[string]string) int
}

// DefaultEstimator 默认的 Token 估算器（基于字符的粗略估算）
type DefaultEstimator struct{}

func NewDefaultEstimator() *DefaultEstimator {
	return &DefaultEstimator{}
}

// EstimateTokens 使用粗略估算：大约 3 个字符 = 1 token（英文）
func (t *DefaultEstimator) EstimateTokens(text string) int {
	if text == "" {
		return 0
	}
	return (len(text) + 2) / 3
}

// EstimateTokensFromMessages 估算消息列表的 Token 数量
func (t *DefaultEstimator) EstimateTokensFromMessages(messages []map[string]string) int {
	total := 0
	for _, msg := range messages {
		contentTokens := t.EstimateTokens(msg["content"])
		if contentTokens > 0 {
			total += maxInt(1, contentTokens/3)
		}
	}
	// 每个消息有少量 overhead
	total += len(messages) * 4
	return total
}

// AllocationStrategy 分配策略
type AllocationStrategy int

const (
	StrategyTruncate AllocationStrategy = iota // 截断策略
	StrategySummarize                          // 摘要策略
	StrategyPrioritize                         // 优先级策略
	StrategyWindow                             // 滑动窗口策略
)

// AllocationResult 分配结果
type AllocationResult struct {
	Allocated int            // 已经分配的 Token 数量
	Remaining int            // 剩余可用 Token 数量
	Content   []string       // 处理后的内容片段
	Metadata  map[string]any // 额外的元数据
}

// TokenBudgetConfig Token 预算配置
type TokenBudgetConfig struct {
	MaxTotalTokens      int                    // 最大 Token 总数
	ReservedTokens      int                    // 预留 Token 数（用于输出）
	Strategy            AllocationStrategy     // 分配策略
	Tokenizer           TokenEstimator         // Token 估算器
	WindowOverlapTokens int                    // 滑动窗口的 Token 重叠数（仅用于 Window 策略）
	SummaryThreshold    int                    // 摘要阈值，超过此值触发摘要（仅用于 Summarize 策略）
}

// TokenBudgetManager Token 预算管理器
type TokenBudgetManager struct {
	config *TokenBudgetConfig
}

// NewTokenBudgetManager 创建新的 Token 预算管理器
func NewTokenBudgetManager(config *TokenBudgetConfig) *TokenBudgetManager {
	if config == nil {
		config = &TokenBudgetConfig{
			MaxTotalTokens:      128000,
			ReservedTokens:      4000,
			Strategy:            StrategyPrioritize,
			Tokenizer:           NewDefaultEstimator(),
			WindowOverlapTokens: 100,
			SummaryThreshold:    10000,
		}
	}
	if config.Tokenizer == nil {
		config.Tokenizer = NewDefaultEstimator()
	}
	return &TokenBudgetManager{config: config}
}

// GetAvailableTokens 获取可用的 Token 数量
func (tbm *TokenBudgetManager) GetAvailableTokens() int {
	return tbm.config.MaxTotalTokens - tbm.config.ReservedTokens
}

// CanFit 检查给定内容是否可以放入预算
func (tbm *TokenBudgetManager) CanFit(messages []map[string]string) bool {
	tokens := tbm.estimateBudgetCost(messages)
	return tokens <= tbm.GetAvailableTokens()
}

// CheckCompatibility 检查内容与模型的兼容性
func (tbm *TokenBudgetManager) CheckCompatibility(messages []map[string]string) (bool, int, error) {
	tokens := tbm.estimateBudgetCost(messages)
	available := tbm.GetAvailableTokens()

	if tokens > available {
		return false, tokens - available, fmt.Errorf("content exceeds token budget: %d tokens required, %d available", tokens, available)
	}

	return true, tokens, nil
}

// Allocate 分配 Token 给内容
func (tbm *TokenBudgetManager) Allocate(messages []map[string]string) (*AllocationResult, error) {
	totalTokens := tbm.estimateBudgetCost(messages)
	available := tbm.GetAvailableTokens()

	if totalTokens <= available {
		// 不需要特殊处理
		content := make([]string, 0, len(messages))
		for _, msg := range messages {
			if role, ok := msg["role"]; ok {
				content = append(content, fmt.Sprintf("%s: %s", role, msg["content"]))
			}
		}
		return &AllocationResult{
			Allocated: totalTokens,
			Remaining: available - totalTokens,
			Content:   content,
			Metadata:  map[string]any{"truncated": false},
		}, nil
	}

	// 根据策略处理超限内容
	switch tbm.config.Strategy {
	case StrategyTruncate:
		return tbm.allocateTruncate(messages, totalTokens, available)
	case StrategySummarize:
		return tbm.allocateSummarize(messages, totalTokens, available)
	case StrategyPrioritize:
		return tbm.allocatePrioritize(messages, totalTokens, available)
	case StrategyWindow:
		return tbm.allocateWindow(messages, totalTokens, available)
	default:
		return nil, fmt.Errorf("unknown allocation strategy: %d", tbm.config.Strategy)
	}
}

func (tbm *TokenBudgetManager) estimateBudgetCost(messages []map[string]string) int {
	rawTokens := 0
	for _, msg := range messages {
		if role, ok := msg["role"]; ok && role != "" {
			rawTokens += tbm.config.Tokenizer.EstimateTokens(role)
		}
		if content, ok := msg["content"]; ok && content != "" {
			rawTokens += tbm.config.Tokenizer.EstimateTokens(content)
		}
		rawTokens += 4
	}

	if rawTokens <= 0 {
		return 0
	}

	return rawTokens * rawTokens
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// allocateTruncate 截断策略：保留最近的 N 个 Token
func (tbm *TokenBudgetManager) allocateTruncate(messages []map[string]string, totalTokens, available int) (*AllocationResult, error) {
	tokens := make([]string, 0, len(messages))
	for _, msg := range messages {
		if role, ok := msg["role"]; ok {
			tokens = append(tokens, fmt.Sprintf("%s: %s", role, msg["content"]))
		}
	}

	// 从后往前保留
	reversed := make([]string, 0, len(tokens))
	for i := len(tokens) - 1; i >= 0; i-- {
		reversed = append(reversed, tokens[i])
	}

	resultTokens := make([]string, 0)
	current := 0
	for _, token := range reversed {
		tokCount := tbm.config.Tokenizer.EstimateTokens(token)
		if current+tokCount > available {
			break
		}
		resultTokens = append(resultTokens, token)
		current += tokCount
	}

	// 反转回来
	final := make([]string, 0, len(resultTokens))
	for i := len(resultTokens) - 1; i >= 0; i-- {
		final = append(final, resultTokens[i])
	}

	return &AllocationResult{
		Allocated: current,
		Remaining: available - current,
		Content:   final,
		Metadata: map[string]any{
			"truncated":      true,
			"original_tokens": totalTokens,
		},
	}, nil
}

// allocateSummarize 摘要策略：对超限内容进行摘要（简化实现）
func (tbm *TokenBudgetManager) allocateSummarize(messages []map[string]string, totalTokens, available int) (*AllocationResult, error) {
	// 简化实现：保留关键消息，合并其他消息
	priorityMessages := make([]string, 0)
	otherMessages := make([]string, 0)

	for _, msg := range messages {
		role := msg["role"]
		if role == "system" || role == "user" {
			priorityMessages = append(priorityMessages, fmt.Sprintf("%s: %s", role, msg["content"]))
		} else {
			otherMessages = append(otherMessages, fmt.Sprintf("%s: %s", role, msg["content"]))
		}
	}

	// 先尝试保留优先级消息
	result := make([]string, 0)
	current := 0

	for _, msg := range priorityMessages {
		tokCount := tbm.config.Tokenizer.EstimateTokens(msg)
		if current+tokCount > available {
			// 如果连优先级消息都放不下，则使用截断
			return tbm.allocateTruncate(messages, totalTokens, available)
		}
		result = append(result, msg)
		current += tokCount
	}

	// 简化：只保留最近的一些其他消息
	reversed := make([]string, 0, len(otherMessages))
	for i := len(otherMessages) - 1; i >= 0; i-- {
		reversed = append(reversed, otherMessages[i])
	}

	for _, msg := range reversed {
		tokCount := tbm.config.Tokenizer.EstimateTokens(msg)
		if current+tokCount > available {
			break
		}
		result = append(result, msg)
		current += tokCount
	}

	return &AllocationResult{
		Allocated: current,
		Remaining: available - current,
		Content:   result,
		Metadata: map[string]any{
			"summarized":      true,
			"original_tokens": totalTokens,
		},
	}, nil
}

// allocatePrioritize 优先级策略：基于消息角色优先级
func (tbm *TokenBudgetManager) allocatePrioritize(messages []map[string]string, totalTokens, available int) (*AllocationResult, error) {
	// 定义优先级顺序
	priority := map[string]int{
		"system":  4,
		"user":    3,
		"tool":    2,
		"assistant": 1,
	}

	// 按优先级分组
	grouped := make(map[int][]string)
	for p := 1; p <= 4; p++ {
		grouped[p] = make([]string, 0)
	}

	for _, msg := range messages {
		role := msg["role"]
		p := priority[role]
		if p == 0 {
			p = 1 // 默认最低优先级
		}
		grouped[p] = append(grouped[p], fmt.Sprintf("%s: %s", role, msg["content"]))
	}

	// 按优先级分配
	result := make([]string, 0)
	current := 0

	for p := 4; p >= 1; p-- {
		for _, msg := range grouped[p] {
			tokCount := tbm.config.Tokenizer.EstimateTokens(msg)
			if current+tokCount > available {
				// 继续下一个优先级
				continue
			}
			result = append(result, msg)
			current += tokCount
		}
	}

	return &AllocationResult{
		Allocated: current,
		Remaining: available - current,
		Content:   result,
		Metadata: map[string]any{
			"prioritized":     true,
			"original_tokens": totalTokens,
		},
	}, nil
}

// allocateWindow 滑动窗口策略：保留最近的 Token，保持一定的重叠
func (tbm *TokenBudgetManager) allocateWindow(messages []map[string]string, totalTokens, available int) (*AllocationResult, error) {
	tokens := make([]string, 0, len(messages))
	for _, msg := range messages {
		if role, ok := msg["role"]; ok {
			tokens = append(tokens, fmt.Sprintf("%s: %s", role, msg["content"]))
		}
	}

	// 计算需要保留的 Token 数（包括重叠）
	targetTokens := available - tbm.config.WindowOverlapTokens
	if targetTokens < 0 {
		targetTokens = 0
	}

	// 从后往前保留
	reversed := make([]string, 0, len(tokens))
	for i := len(tokens) - 1; i >= 0; i-- {
		reversed = append(reversed, tokens[i])
	}

	resultTokens := make([]string, 0)
	current := 0
	for _, token := range reversed {
		tokCount := tbm.config.Tokenizer.EstimateTokens(token)
		if current >= targetTokens {
			break
		}
		resultTokens = append(resultTokens, token)
		current += tokCount
	}

	// 反转回来
	final := make([]string, 0, len(resultTokens))
	for i := len(resultTokens) - 1; i >= 0; i-- {
		final = append(final, resultTokens[i])
	}

	return &AllocationResult{
		Allocated: current,
		Remaining: available - current,
		Content:   final,
		Metadata: map[string]any{
			"window":          true,
			"overlap_tokens":  tbm.config.WindowOverlapTokens,
			"original_tokens": totalTokens,
		},
	}, nil
}

// OptimizeContext 优化上下文以最大化信息密度
func (tbm *TokenBudgetManager) OptimizeContext(messages []map[string]string, keywords []string) (*AllocationResult, error) {
	// 简单实现：优先包含包含关键词的消息
	priorityMessages := make([]string, 0)
	otherMessages := make([]string, 0)

	keywordSet := make(map[string]bool)
	for _, kw := range keywords {
		keywordSet[strings.ToLower(kw)] = true
	}

	for _, msg := range messages {
		content := msg["content"]
		formatted := fmt.Sprintf("%s: %s", msg["role"], content)
		lowerContent := strings.ToLower(content)

		hasKeyword := false
		for kw := range keywordSet {
			if strings.Contains(lowerContent, kw) {
				hasKeyword = true
				break
			}
		}

		if hasKeyword {
			priorityMessages = append(priorityMessages, formatted)
		} else {
			otherMessages = append(otherMessages, formatted)
		}
	}

	// 先添加优先级消息
	result := make([]string, 0)
	current := 0
	available := tbm.GetAvailableTokens()

	for _, msg := range priorityMessages {
		tokCount := tbm.config.Tokenizer.EstimateTokens(msg)
		if current+tokCount > available {
			continue
		}
		result = append(result, msg)
		current += tokCount
	}

	// 再添加其他消息
	for _, msg := range otherMessages {
		tokCount := tbm.config.Tokenizer.EstimateTokens(msg)
		if current+tokCount > available {
			continue
		}
		result = append(result, msg)
		current += tokCount
	}

	return &AllocationResult{
		Allocated: current,
		Remaining: available - current,
		Content:   result,
		Metadata: map[string]any{
			"optimized":      true,
			"keywords":       keywords,
			"matched_count":  len(priorityMessages),
		},
	}, nil
}
