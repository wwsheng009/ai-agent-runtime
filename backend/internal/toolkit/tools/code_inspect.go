package tools

import (
	"context"
	"fmt"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/knowledge"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolkit"
)

// CodeInspectTool 实现 code_inspect（06 §4 Phase 3 的 `code.inspect`）：
// 按符号读取（view 的符号级增强）；无索引时退化为 view / grep。
type CodeInspectTool struct {
	*toolkit.BaseTool
	codeToolBase
}

// NewCodeInspectTool 创建 code_inspect 工具。
func NewCodeInspectTool() *CodeInspectTool {
	parameters := map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"symbol": map[string]interface{}{
				"type":        "string",
				"description": "符号名（如 planLookupQuery）。给出时按索引定位符号范围并读取其实现；索引不可用时退化为对符号名的一次 grep。",
			},
			"file_path": map[string]interface{}{
				"type":        "string",
				"description": "文件路径（workspace 相对或绝对）。不给 symbol 时等价 view（按行读取）；给出 symbol 时作为该符号读取的兜底路径。",
			},
			"offset": map[string]interface{}{
				"type":        "integer",
				"description": "0-based 起始行；仅 file_path 路径生效（symbol 路径按符号范围自动定位）。",
			},
			"limit": map[string]interface{}{
				"type":        "integer",
				"description": "读取行数上限；symbol 路径默认读取整个符号范围。",
			},
		},
		"required": []string{},
	}
	return &CodeInspectTool{BaseTool: toolkit.NewBaseTool(
		"code_inspect",
		"按符号读取实现（索引增强，设计文档中的 `code.inspect`）。"+
			"知道符号名但不知道行号时优先用它；普通按行读取仍用 view（view 也支持可选 symbol 参数）。"+
			"索引不可用时自动降级为 grep / view，返回结构不变（source=fallback）。",
		"1.0.0",
		parameters,
		true,
	)}
}

// DefinitionMetadata 声明只读语义。
func (t *CodeInspectTool) DefinitionMetadata() map[string]interface{} {
	return codeReadOnlyMetadata()
}

// Execute 实现 Tool 接口。
func (t *CodeInspectTool) Execute(ctx context.Context, params map[string]interface{}) (*toolkit.ToolResult, error) {
	symbol := codeParamString(params, "symbol")
	filePath := codeParamString(params, "file_path")
	offset := codeParamInt(params, "offset", 0)
	limit := codeParamInt(params, "limit", 0)

	if symbol == "" && filePath == "" {
		return codeParamError("code_inspect", "symbol 与 file_path 至少需要一个"), nil
	}

	// 纯文件读取：等价 view（索引不参与），仍走统一返回结构。
	if symbol == "" {
		result, _ := t.runView(ctx, map[string]interface{}{
			"file_path": filePath, "offset": offset, "limit": limit,
		})
		env := fallbackEnvelope("code_inspect", codeFallbackByRequest, codeConfidenceNone, "view", result)
		return codeResult(env), nil
	}

	handle, ok := t.resolveIndex(ctx)
	if !ok {
		env := t.inspectFallback(ctx, codeFallbackIndexUnavailable, codeConfidenceNone, symbol, filePath, offset, limit)
		return codeResult(env), nil
	}

	sym, confidence, found, err := resolveCodeSymbol(ctx, handle.Index, symbol, "")
	if err != nil {
		env := t.inspectFallback(ctx, codeFallbackIndexError, codeConfidenceNone, symbol, filePath, offset, limit)
		return codeResult(env), nil
	}
	if !found {
		env := t.inspectFallback(ctx, codeFallbackNoIndexHit, codeConfidenceNone, symbol, filePath, offset, limit)
		return codeResult(env), nil
	}

	// shadow 档（04 §4.6 第 3 步）：候选照算，但返回 view/grep 结果，
	// 不改变模型可见输出。
	if handle.Mode == knowledge.ModeShadow {
		env := t.inspectFallback(ctx, codeFallbackShadowMode, confidence, symbol, filePath, offset, limit)
		env.Explanation += " 索引候选 1 条（未返回）。"
		return codeResult(env), nil
	}

	path := handle.PathForFile(sym.FileID)
	if path == "" {
		env := t.inspectFallback(ctx, codeFallbackIndexError, confidence, symbol, filePath, offset, limit)
		return codeResult(env), nil
	}

	span := sym.Range.End.Line - sym.Range.Start.Line + 1
	if span <= 0 {
		span = 1
	}
	readLimit := span
	if limit > 0 && limit < readLimit {
		readLimit = limit
	}
	result, viewErr := t.runView(ctx, map[string]interface{}{
		"file_path": path,
		"offset":    sym.Range.Start.Line - 1,
		"limit":     readLimit,
	})

	env := newCodeEnvelope("code_inspect")
	env.Source = codeSourceIndex
	env.Confidence = confidence
	env.Range = &codeRange{
		Path:      path,
		StartLine: sym.Range.Start.Line,
		StartCol:  sym.Range.Start.Column,
		EndLine:   sym.Range.End.Line,
		EndCol:    sym.Range.End.Column,
	}
	content := ""
	if result != nil {
		content = strings.TrimSpace(result.Content)
	}
	env.Results = map[string]interface{}{
		"symbol":  codeSymbolHitFrom(sym, path),
		"content": content,
	}
	env.Explanation = fmt.Sprintf(
		"按符号读取：%s（%s，第 %d–%d 行）；正文经 view 读取（等价 view file_path=%s offset=%d limit=%d）。",
		sym.Name, path, sym.Range.Start.Line, sym.Range.End.Line, path, sym.Range.Start.Line-1, readLimit,
	)
	// limit 裁剪或 view 自身截断都意味着正文不完整；读取失败则显式降级标记。
	env.Truncated = (limit > 0 && limit < span) || codeResultTruncated(result)
	if viewErr != nil || result == nil || !result.Success {
		env.Degraded = true
		env.Explanation += "；正文读取失败"
		switch {
		case viewErr != nil:
			env.Explanation += "：" + viewErr.Error()
		case result != nil && result.Error != nil:
			env.Explanation += "：" + result.Error.Error()
		default:
			env.Explanation += "（view 未返回内容）"
		}
	}
	return codeResult(env), nil
}

// inspectFallback 处理符号路径的降级：优先按 file_path 走 view，否则按符号名走 grep。
func (t *CodeInspectTool) inspectFallback(ctx context.Context, reason string, confidence float64, symbol, filePath string, offset, limit int) codeEnvelope {
	if filePath != "" {
		result, _ := t.runView(ctx, map[string]interface{}{
			"file_path": filePath, "offset": offset, "limit": limit,
		})
		return fallbackEnvelope("code_inspect", reason, confidence, "view", result)
	}
	result, _ := t.runGrep(ctx, map[string]interface{}{"pattern": symbol, "literal": true})
	return fallbackEnvelope("code_inspect", reason, confidence, "grep", result)
}
