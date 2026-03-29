package functions

import (
	"context"
	"fmt"
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
		schemas = append(schemas, schema)
	}
	return schemas
}

// ExecuteFunction 执行指定的 Function
func (r *FunctionRegistry) ExecuteFunction(ctx context.Context, name string, args map[string]interface{}) (string, error) {
	fn, ok := r.Get(name)
	if !ok {
		return "", fmt.Errorf("function '%s' not found", name)
	}
	return fn.Execute(ctx, args)
}
