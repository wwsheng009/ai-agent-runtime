package functions

import (
	"context"
	"fmt"

	"github.com/wwsheng009/ai-agent-runtime/internal/toolargs"
)

// Function 定义 Function Call 的接口
type Function interface {
	// Name 返回 Function 名称
	Name() string

	// Description 返回 Function 描述
	Description() string

	// Parameters 返回 Function 参数的 JSON Schema 描述
	Parameters() map[string]interface{}

	// Execute 执行 Function，返回结果
	Execute(ctx context.Context, args map[string]interface{}) (string, error)
}

// FunctionWithMetadata is an optional richer execution interface.
type FunctionWithMetadata interface {
	Function
	ExecuteWithMeta(ctx context.Context, args map[string]interface{}) (string, map[string]interface{}, error)
}

// FunctionDefinitionMetadataProvider allows functions to expose extra tool
// definition metadata while keeping the base Function interface stable.
type FunctionDefinitionMetadataProvider interface {
	DefinitionMetadata() map[string]interface{}
}

// FunctionRegistry Function 注册表
type FunctionRegistry struct {
	functions map[string]Function
}

// NewFunctionRegistry 创建新的 Function 注册表
func NewFunctionRegistry() *FunctionRegistry {
	return &FunctionRegistry{
		functions: make(map[string]Function),
	}
}

// Register 注册一个 Function
func (r *FunctionRegistry) Register(fn Function) {
	r.functions[fn.Name()] = fn
}

// Get 获取 Function
func (r *FunctionRegistry) Get(name string) (Function, bool) {
	fn, ok := r.functions[name]
	return fn, ok
}

// List 列出所有 Function
func (r *FunctionRegistry) List() []Function {
	result := make([]Function, 0, len(r.functions))
	for _, fn := range r.functions {
		result = append(result, fn)
	}
	return result
}

// GetFunctionSchemas 获取所有 Function 的 Schema（用于发送给 AI）
func (r *FunctionRegistry) GetFunctionSchemas() []map[string]interface{} {
	schemas := make([]map[string]interface{}, 0, len(r.functions))
	for _, fn := range r.functions {
		schema := map[string]interface{}{
			"name":        fn.Name(),
			"description": fn.Description(),
			"parameters":  fn.Parameters(),
		}
		if provider, ok := fn.(FunctionDefinitionMetadataProvider); ok {
			if metadata := provider.DefinitionMetadata(); len(metadata) > 0 {
				schema["metadata"] = metadata
			}
		}
		schemas = append(schemas, schema)
	}
	return schemas
}

// ExecuteFunction 执行指定的 Function
func (r *FunctionRegistry) ExecuteFunction(ctx context.Context, name string, args map[string]interface{}) (string, error) {
	output, _, err := r.ExecuteFunctionWithMeta(ctx, name, args)
	return output, err
}

// ExecuteFunctionWithMeta executes a function and returns optional metadata when supported.
func (r *FunctionRegistry) ExecuteFunctionWithMeta(ctx context.Context, name string, args map[string]interface{}) (string, map[string]interface{}, error) {
	fn, ok := r.Get(name)
	if !ok {
		return "", nil, fmt.Errorf("function '%s' not found", name)
	}
	args = toolargs.Normalize(args)
	if rich, ok := fn.(FunctionWithMetadata); ok {
		return rich.ExecuteWithMeta(ctx, args)
	}
	output, err := fn.Execute(ctx, args)
	return output, nil, err
}
