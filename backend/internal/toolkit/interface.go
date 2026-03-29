package toolkit

import (
	"context"
	"encoding/json"
)

// Tool 统一工具接口
type Tool interface {
	// Name 工具名称
	Name() string

	// Description 工具描述
	Description() string

	// Version 工具版本
	Version() string

	// Parameters 返回参数的 JSON Schema
	Parameters() map[string]interface{}

	// Execute 执行工具
	Execute(ctx context.Context, params map[string]interface{}) (*ToolResult, error)

	// CanDirectCall 是否支持直接调用（绕过 MCP）
	CanDirectCall() bool
}

// ToolResult 工具执行结果
type ToolResult struct {
	// Success 是否执行成功
	Success bool

	// Content 文本内容
	Content string

	// Data 二进制数据（用于图片等）
	Data []byte

	// MIME Type
	MIMEType string

	// Error 错误信息（如果失败）
	Error error

	// Metadata 元数据
	Metadata map[string]interface{}
}

// ToJSON 转换为 JSON 格式
func (r *ToolResult) ToJSON() ([]byte, error) {
	result := map[string]interface{}{
		"success":  r.Success,
		"content":  r.Content,
		"mimeType": r.MIMEType,
		"metadata": r.Metadata,
	}
	if r.Data != nil {
		result["data"] = r.Data
	}
	if r.Error != nil {
		result["error"] = r.Error.Error()
	}
	return json.Marshal(result)
}

// FromJSON 从 JSON 解析
func FromJSON(data []byte) (*ToolResult, error) {
	var result ToolResult
	if err := json.Unmarshal(data, &result); err != nil {
		return nil, err
	}
	return &result, nil
}
