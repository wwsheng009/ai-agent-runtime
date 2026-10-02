package tools

import (
	"context"
	"fmt"

	"github.com/wwsheng009/ai-agent-runtime/internal/toolkit"
)

// CodeCallersTool 实现 code_callers（06 §4 Phase 3 的 `code.callers`）：
// code_references 的调用点子集（kind=call）——"谁调用了 X"。
type CodeCallersTool struct {
	*toolkit.BaseTool
	codeToolBase
}

// NewCodeCallersTool 创建 code_callers 工具。
func NewCodeCallersTool() *CodeCallersTool {
	parameters := map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"symbol": map[string]interface{}{
				"type":        "string",
				"description": "被调用的函数/方法名（如 ObserveToolResult）。",
			},
			"path_prefix": map[string]interface{}{
				"type":        "string",
				"description": "可选：限定调用点所在文件的 workspace 相对路径前缀。",
			},
			"limit": map[string]interface{}{
				"type":        "integer",
				"description": fmt.Sprintf("可选：返回条数上限（默认 %d，最大 %d）。", codeRefsDefaultLimit, codeRefsMaxLimit),
			},
		},
		"required": []string{"symbol"},
	}
	return &CodeCallersTool{BaseTool: toolkit.NewBaseTool(
		"code_callers",
		"调用点查询（索引增强，设计文档中的 `code.callers`）：谁调用了符号 X（kind=call 的引用子集）。"+
			"影响面分析优先用它；索引不可用时自动降级为 grep（source=fallback）。"+
			"它是 code_references 的 kind=call 子集（两者命中相同时用本工具即可）。"+
			codeEnvelopeSemantics,
		"1.0.0",
		parameters,
		true,
	)}
}

// DefinitionMetadata 声明只读语义。
func (t *CodeCallersTool) DefinitionMetadata() map[string]interface{} {
	return codeReadOnlyMetadata()
}

// Execute 实现 Tool 接口。
func (t *CodeCallersTool) Execute(ctx context.Context, params map[string]interface{}) (*toolkit.ToolResult, error) {
	symbol := codeParamString(params, "symbol")
	if symbol == "" {
		return codeParamError("code_callers", "symbol 参数缺失或为空"), nil
	}
	limit, clamped := codeClampLimit(codeParamInt(params, "limit", 0), codeRefsDefaultLimit, codeRefsMaxLimit)
	return runCodeRefsQuery(
		ctx, &t.codeToolBase, "code_callers",
		symbol, "call", codeParamString(params, "path_prefix"), limit, clamped,
	), nil
}
