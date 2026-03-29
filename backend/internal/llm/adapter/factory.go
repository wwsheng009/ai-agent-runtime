package adapter

import (
	"fmt"
	"time"
)

// NewAdapter 创建对应类型的协议适配器
func NewAdapter(providerType string) (ProtocolAdapter, error) {
	switch providerType {
	case "openai":
		return &OpenAIAdapter{}, nil
	case "gemini":
		return &GeminiAdapter{}, nil
	case "anthropic":
		return &AnthropicAdapter{}, nil
	case "codex":
		return &CodexAdapter{}, nil
	default:
		return nil, fmt.Errorf("unsupported provider type: %s", providerType)
	}
}

// GetAdapterOrDefault 获取适配器，默认使用 OpenAI
func GetAdapterOrDefault(providerType string) ProtocolAdapter {
	adapter, err := NewAdapter(providerType)
	if err != nil {
		return &OpenAIAdapter{}
	}
	return adapter
}

// DefaultRequestConfig 默认请求配置
func DefaultRequestConfig() RequestConfig {
	return RequestConfig{
		MaxTokens:   2000,
		Temperature: 0.7,
		Timeout:     120 * time.Second,
	}
}
