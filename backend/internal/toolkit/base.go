package toolkit

import (
	"context"
	"fmt"
)

// BaseTool 工具基类，提供通用功能
type BaseTool struct {
	name        string
	description string
	version     string
	parameters  map[string]interface{}
	directCall  bool
}

// NewBaseTool 创建基础工具
func NewBaseTool(name, description, version string, parameters map[string]interface{}, directCall bool) *BaseTool {
	if parameters == nil {
		parameters = map[string]interface{}{}
	}
	if _, ok := parameters["type"]; !ok {
		parameters["type"] = "object"
	}
	if paramType, ok := parameters["type"].(string); ok && paramType == "object" {
		if _, ok := parameters["additionalProperties"]; !ok {
			parameters["additionalProperties"] = false
		}
	}
	return &BaseTool{
		name:        name,
		description: description,
		version:     version,
		parameters:  parameters,
		directCall:  directCall,
	}
}

// Name 实现 Tool 接口
func (b *BaseTool) Name() string {
	return b.name
}

// Description 实现 Tool 接口
func (b *BaseTool) Description() string {
	return b.description
}

// Version 实现 Tool 接口
func (b *BaseTool) Version() string {
	return b.version
}

// Parameters 实现 Tool 接口
func (b *BaseTool) Parameters() map[string]interface{} {
	return b.parameters
}

// CanDirectCall 实现 Tool 接口
func (b *BaseTool) CanDirectCall() bool {
	return b.directCall
}

// Execute 抽象方法，由子类实现
func (b *BaseTool) Execute(ctx context.Context, params map[string]interface{}) (*ToolResult, error) {
	return nil, fmt.Errorf("method not implemented")
}
