package toolkit

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/toolresult"
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

// ToolDefinitionMetadataProvider allows tools to expose extra definition
// metadata without changing the base Tool interface.
type ToolDefinitionMetadataProvider interface {
	DefinitionMetadata() map[string]interface{}
}

// ToolResult 工具执行结果
type ToolResult struct {
	// Success 是否执行成功
	Success bool

	// OutputKind 输出类型：text / structured / binary / empty
	OutputKind string

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

func (r *ToolResult) NormalizedOutputKind() string {
	if r == nil {
		return ""
	}
	if kind := toolresult.NormalizeKind(r.OutputKind); kind != "" {
		return kind
	}
	if kind := toolresult.KindFromMetadata(r.Metadata); kind != "" {
		return kind
	}
	if len(r.Data) > 0 || strings.TrimSpace(r.MIMEType) != "" {
		return toolresult.KindBinary
	}
	if strings.TrimSpace(r.Content) == "" {
		return toolresult.KindEmpty
	}
	return toolresult.KindText
}

func (r *ToolResult) MetadataWithOutputKind() map[string]interface{} {
	if r == nil {
		return nil
	}
	return toolresult.WithKind(r.Metadata, r.NormalizedOutputKind())
}

// ToJSON 转换为 JSON 格式
func (r *ToolResult) ToJSON() ([]byte, error) {
	kind := ""
	metadata := map[string]interface{}(nil)
	if r != nil {
		kind = r.NormalizedOutputKind()
		metadata = r.MetadataWithOutputKind()
	}
	result := map[string]interface{}{
		"success":    r.Success,
		"content":    r.Content,
		"mimeType":   r.MIMEType,
		"outputKind": kind,
		"metadata":   metadata,
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
