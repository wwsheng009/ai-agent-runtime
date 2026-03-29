package llm

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/ai-gateway/ai-agent-runtime/internal/types"
)

// MockProvider 模拟 LLM 提供者（用于测试和演示）
type MockProvider struct {
	name           string
	delay          time.Duration
	responses      map[string]string
	shouldFail    bool
	failureMessage string
}

// NewMockProvider 创建模拟提供者
func NewMockProvider(name string, delay time.Duration) *MockProvider {
	return &MockProvider{
		name:      name,
		delay:     delay,
		responses: make(map[string]string),
	}
}

// Name 返回提供者名称
func (p *MockProvider) Name() string {
	return p.name
}

// SetResponse 设置预设响应
func (p *MockProvider) SetResponse(key string, response string) {
	p.responses[key] = response
}

// SetFailure 设置失败模式
func (p *MockProvider) SetFailure(shouldFail bool, message string) {
	p.shouldFail = shouldFail
	p.failureMessage = message
}

// Call 调用 LLM（非流式）
func (p *MockProvider) Call(ctx context.Context, req *LLMRequest) (*LLMResponse, error) {
	if p.shouldFail {
		return nil, errors.New(p.failureMessage)
	}

	if p.delay > 0 {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(p.delay):
		}
	}

	// 生成响应内容
	response := p.generateResponse(req)

	return &LLMResponse{
		Content: response,
		Usage: &types.TokenUsage{
			PromptTokens:     100,
			CompletionTokens: 50,
			TotalTokens:      150,
		},
		Model: req.Model,
	}, nil
}

// Stream 流式调用 LLM
func (p *MockProvider) Stream(ctx context.Context, req *LLMRequest) (<-chan StreamChunk, error) {
	if p.shouldFail {
		return nil, errors.New(p.failureMessage)
	}

	ch := make(chan StreamChunk, 100)

	go func() {
		defer close(ch)

		response := p.generateResponse(req)

		// 简单模拟流式输出：按字符发送
		words := strings.Split(response, " ")
		for _, word := range words {
			select {
			case <-ctx.Done():
				return
			case ch <- StreamChunk{
				Type:    EventTypeText,
				Content: word + " ",
			}:
				time.Sleep(10 * time.Millisecond) // 模拟流式延迟
			}
		}

		ch <- StreamChunk{
			Type: EventTypeDone,
			Done: true,
		}
	}()

	return ch, nil
}

// CountTokens 统计 Token 数
func (p *MockProvider) CountTokens(text string) int {
	// 简化实现：按单词数
	return len(strings.Fields(text)) + len(text)/10
}

// GetCapabilities 获取模型能力
func (p *MockProvider) GetCapabilities() *ModelCapabilities {
	return &ModelCapabilities{
		MaxContextTokens:  128000,
		MaxOutputTokens:   4096,
		SupportsVision:    false,
		SupportsTools:     true,
		SupportsStreaming: true,
		SupportsJSONMode:  true,
	}
}

// CheckHealth 检查健康状况
func (p *MockProvider) CheckHealth(ctx context.Context) error {
	if p.shouldFail {
		return fmt.Errorf("provider is in failure mode")
	}
	return nil
}

// generateResponse 生成模拟响应
func (p *MockProvider) generateResponse(req *LLMRequest) string {
	// 根据请求内容生成响应
	var promptBuilder strings.Builder

	for _, msg := range req.Messages {
		if msg.Role == "user" {
			promptBuilder.WriteString(msg.Content)
			promptBuilder.WriteString(" ")
		}
	}

	prompt := strings.TrimSpace(promptBuilder.String())

	// 检查是否有预设响应
	if response, ok := p.responses[prompt]; ok {
		return response
	}

	// 根据关键词生成简单响应
	switch {
	case strings.Contains(strings.ToLower(prompt), "help"):
		return "I'm here to help! What would you like to know?"
	case strings.Contains(strings.ToLower(prompt), "hello"):
		return "Hello! How can I assist you today?"
	case strings.Contains(strings.ToLower(prompt), "weather"):
		return "I don't have access to real-time weather data, but you can check a weather service."
	case strings.Contains(strings.ToLower(prompt), "code"):
		return "I can help with coding tasks. What would you like me to write or explain?"
	case strings.Contains(strings.ToLower(prompt), "analyze"):
		return "I'll analyze the information you've provided. Let me think about it..."
	default:
		return fmt.Sprintf("I received your message: \"%s\". As a mock AI, I can demonstrate basic interactions but don't have real AI capabilities.", prompt)
	}
}

// ExampleFactory 示例工厂函数
func MockProvidersFactory() []LLMProvider {
	return []LLMProvider{
		NewMockProvider("gpt-4-turbo", 100*time.Millisecond),
		NewMockProvider("gpt-4o", 150*time.Millisecond),
		NewMockProvider("claude-3-5-sonnet", 120*time.Millisecond),
		NewMockProvider("claude-3-opus", 200*time.Millisecond),
	}
}

// SetupMockResponses 设置模拟响应
func SetupMockResponses(provider *MockProvider) {
	provider.responses["What can you do?"] = "I can help with various tasks including answering questions, writing code, analyzing text, and more. I can also use tools when available."
	provider.responses["Write a hello function"] = "Here's a simple hello function in Python:\n\ndef hello():\n    print('Hello, World!')\n\nhello()"
	provider.responses["Add two numbers"] = "To add two numbers, you can use the + operator. For example: 5 + 3 = 8"
	provider.responses["Create a todo"] = "Todo created successfully."
	provider.responses["List todos"] = "1. Complete project\n2. Write documentation\n3. Review code"
}
