package tools

import (
	"context"
	"fmt"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/knowledge"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolkit"
)

// CodeNavigateTool 实现 code_navigate（06 §4 Phase 3 的 `code.navigate`）：
// 图遍历（定义位置 / 文件成员 / 一跳引用），不做文件列举（那是 glob/ls 的职责）。
type CodeNavigateTool struct {
	*toolkit.BaseTool
	codeToolBase
}

// NewCodeNavigateTool 创建 code_navigate 工具。
func NewCodeNavigateTool() *CodeNavigateTool {
	parameters := map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"symbol": map[string]interface{}{
				"type":        "string",
				"description": "符号名；direction=definition|refs 时必填。",
			},
			"file_path": map[string]interface{}{
				"type":        "string",
				"description": "文件路径；direction=members 时必填（列出该文件定义的符号）。",
			},
			"direction": map[string]interface{}{
				"type":        "string",
				"description": "遍历方向：definition（符号定义位置，默认）/ members（文件内符号）/ refs（一跳引用）。",
			},
			"path_prefix": map[string]interface{}{
				"type":        "string",
				"description": "可选：限定引用所在文件的 workspace 相对路径前缀（仅 direction=refs）。",
			},
			"limit": map[string]interface{}{
				"type":        "integer",
				"description": fmt.Sprintf("可选：返回条数上限（默认 %d，最大 %d）。", codeRefsDefaultLimit, codeRefsMaxLimit),
			},
		},
		"required": []string{},
	}
	return &CodeNavigateTool{BaseTool: toolkit.NewBaseTool(
		"code_navigate",
		"代码图遍历（索引增强，设计文档中的 `code.navigate`）：符号定义位置、文件内符号、一跳引用。"+
			"文件/目录列举请用 glob/ls；索引不可用时按方向降级到 grep / view（source=fallback）。",
		"1.0.0",
		parameters,
		true,
	)}
}

// DefinitionMetadata 声明只读语义。
func (t *CodeNavigateTool) DefinitionMetadata() map[string]interface{} {
	return codeReadOnlyMetadata()
}

// Execute 实现 Tool 接口。
func (t *CodeNavigateTool) Execute(ctx context.Context, params map[string]interface{}) (*toolkit.ToolResult, error) {
	symbol := codeParamString(params, "symbol")
	filePath := codeParamString(params, "file_path")
	direction := strings.ToLower(codeParamString(params, "direction"))
	if direction == "" {
		if symbol != "" {
			direction = "definition"
		} else {
			direction = "members"
		}
	}
	limit, clamped := codeClampLimit(codeParamInt(params, "limit", 0), codeRefsDefaultLimit, codeRefsMaxLimit)

	switch direction {
	case "definition":
		if symbol == "" {
			return codeParamError("code_navigate", "direction=definition 需要 symbol"), nil
		}
		return t.navigateDefinition(ctx, symbol, clamped), nil
	case "members":
		if filePath == "" {
			return codeParamError("code_navigate", "direction=members 需要 file_path"), nil
		}
		return t.navigateMembers(ctx, filePath, limit, clamped), nil
	case "refs":
		if symbol == "" {
			return codeParamError("code_navigate", "direction=refs 需要 symbol"), nil
		}
		return runCodeRefsQuery(
			ctx, &t.codeToolBase, "code_navigate",
			symbol, "", codeParamString(params, "path_prefix"), limit, clamped,
		), nil
	default:
		return codeParamError("code_navigate", "direction 只能是 definition|members|refs"), nil
	}
}

// navigateDefinition 返回符号定义位置（不含正文；正文由 code_inspect / view 读取）。
func (t *CodeNavigateTool) navigateDefinition(ctx context.Context, symbol string, clamped bool) *toolkit.ToolResult {
	grepParams := map[string]interface{}{"pattern": symbol, "literal": true}
	handle, ok := t.resolveIndex(ctx)
	if !ok {
		result, _ := t.runGrep(ctx, grepParams)
		env := fallbackEnvelope("code_navigate", codeFallbackIndexUnavailable, codeConfidenceNone, "grep", result)
		env.Truncated = clamped
		return codeResult(env)
	}
	sym, confidence, found, err := resolveCodeSymbol(ctx, handle.Index, symbol, "")
	if err != nil || !found {
		reason := codeFallbackNoIndexHit
		if err != nil {
			reason = codeFallbackIndexError
		}
		result, _ := t.runGrep(ctx, grepParams)
		env := fallbackEnvelope("code_navigate", reason, confidence, "grep", result)
		env.Truncated = clamped
		return codeResult(env)
	}
	path := handle.PathForFile(sym.FileID)
	hit := codeSymbolHitFrom(sym, path)
	env := newCodeEnvelope("code_navigate")
	env.Source = codeSourceIndex
	env.Confidence = confidence
	env.Range = &hit.Range
	env.Results = []codeSymbolHit{hit}
	env.Explanation = fmt.Sprintf(
		"定义位置：%s（%s，第 %d–%d 行）；正文用 code_inspect 或 view 读取。",
		sym.Name, path, sym.Range.Start.Line, sym.Range.End.Line,
	)
	return codeResult(env)
}

// navigateMembers 列出文件内定义的符号（只做符号枚举，不做文件列举）。
func (t *CodeNavigateTool) navigateMembers(ctx context.Context, filePath string, limit int, clamped bool) *toolkit.ToolResult {
	handle, ok := t.resolveIndex(ctx)
	if !ok {
		result, _ := t.runView(ctx, map[string]interface{}{"file_path": filePath, "limit": 200})
		env := fallbackEnvelope("code_navigate", codeFallbackIndexUnavailable, codeConfidenceNone, "view", result)
		env.Explanation += " members 需要索引；已降级为读取文件头部，请用 grep 继续定位具体成员。"
		env.Truncated = clamped
		return codeResult(env)
	}

	syms, err := handle.Index.FindSymbols(ctx, knowledge.SymbolQuery{PathPrefix: filePath, Limit: limit})
	if err != nil {
		result, _ := t.runView(ctx, map[string]interface{}{"file_path": filePath, "limit": 200})
		env := fallbackEnvelope("code_navigate", codeFallbackIndexError, codeConfidenceNone, "view", result)
		env.Truncated = clamped
		return codeResult(env)
	}

	// PathPrefix 是前缀语义（可能带出子目录文件）；members 只保留该文件本身。
	normalized := strings.TrimSuffix(strings.TrimSpace(filePath), "/")
	results := make([]codeSymbolHit, 0, len(syms))
	for _, sym := range syms {
		if sym.DeletedAt != 0 {
			continue
		}
		path := handle.PathForFile(sym.FileID)
		if path != normalized {
			continue
		}
		results = append(results, codeSymbolHitFrom(sym, path))
	}

	env := newCodeEnvelope("code_navigate")
	env.Source = codeSourceIndex
	env.Confidence = codeConfidenceExact
	env.Results = results
	env.Truncated = clamped || len(syms) >= limit
	env.Explanation = fmt.Sprintf("文件 %s 内定义 %d 个符号（索引口径，路径精确匹配）。", normalized, len(results))
	return codeResult(env)
}
