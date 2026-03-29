package adapter

import (
	"strings"
)

// reasoningModels 推理模型列表
var reasoningModels = []string{
	"o1", "o1-preview", "o1-mini",
	"o3", "o3-mini",
	"gpt-5", "gpt-5-preview",
}

// getReasoningModels 获取推理模型列表
func getReasoningModels() []string {
	return reasoningModels
}

// isReasoningModelPrefix 检查是否为推理模型前缀
func isReasoningModelPrefix(model string) bool {
	for _, prefix := range reasoningModels {
		if strings.HasPrefix(model, prefix) {
			return true
		}
	}
	return false
}

// extractReasoningContent 从响应中提取推理内容
func extractReasoningContent(result map[string]interface{}) string {
	// 尝试从 message 中获取 reasoning_content
	if choices, ok := result["choices"].([]interface{}); ok && len(choices) > 0 {
		if choice, ok := choices[0].(map[string]interface{}); ok {
			if msg, ok := choice["message"].(map[string]interface{}); ok {
				if reasoning, ok := msg["reasoning_content"].(string); ok && reasoning != "" {
					return reasoning
				}
			}
		}
	}

	// 尝试从 delta 中获取推理内容（流式）
	if choices, ok := result["choices"].([]interface{}); ok && len(choices) > 0 {
		if choice, ok := choices[0].(map[string]interface{}); ok {
			if delta, ok := choice["delta"].(map[string]interface{}); ok {
				if reasoning, ok := delta["reasoning_content"].(string); ok && reasoning != "" {
					return reasoning
				}
			}
		}
	}

	return ""
}

// checkFinishReason 检查完成原因
func checkFinishReason(result map[string]interface{}) string {
	if choices, ok := result["choices"].([]interface{}); ok && len(choices) > 0 {
		if choice, ok := choices[0].(map[string]interface{}); ok {
			if finishReason, ok := choice["finish_reason"].(string); ok {
				return finishReason
			}
		}
	}
	return ""
}
