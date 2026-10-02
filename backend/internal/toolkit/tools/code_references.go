package tools

import (
	"context"
	"fmt"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/knowledge"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolkit"
)

// CodeReferencesTool 实现 code_references（06 §4 Phase 3 的 `code.references`）：
// 解析指向某个符号身份的引用点（无现有工具对应；索引不可用时降级 grep）。
type CodeReferencesTool struct {
	*toolkit.BaseTool
	codeToolBase
}

// NewCodeReferencesTool 创建 code_references 工具。
func NewCodeReferencesTool() *CodeReferencesTool {
	parameters := map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"symbol": map[string]interface{}{
				"type":        "string",
				"description": "被引用的符号名（如 ResolveStrategy）。",
			},
			"kind": map[string]interface{}{
				"type":        "string",
				"description": "可选：引用种类过滤（如 call / import / type）；空表示全部。",
			},
			"path_prefix": map[string]interface{}{
				"type":        "string",
				"description": "可选：限定引用所在文件的 workspace 相对路径前缀。",
			},
			"limit": map[string]interface{}{
				"type":        "integer",
				"description": fmt.Sprintf("可选：返回条数上限（默认 %d，最大 %d）。", codeRefsDefaultLimit, codeRefsMaxLimit),
			},
		},
		"required": []string{"symbol"},
	}
	return &CodeReferencesTool{BaseTool: toolkit.NewBaseTool(
		"code_references",
		"符号引用查询（索引增强，设计文档中的 `code.references`）：谁使用了符号 X。"+
			"按符号身份匹配、比纯文本更精确（引用为索引侧启发式，可能含同名噪音）；索引不可用时自动降级为 grep（source=fallback，按文本近似）。"+
			"kind 可选 reference|call|import|implement；省略则返回全部种类的引用。"+
			codeEnvelopeSemantics,
		"1.0.0",
		parameters,
		true,
	)}
}

// DefinitionMetadata 声明只读语义。
func (t *CodeReferencesTool) DefinitionMetadata() map[string]interface{} {
	return codeReadOnlyMetadata()
}

// Execute 实现 Tool 接口。
func (t *CodeReferencesTool) Execute(ctx context.Context, params map[string]interface{}) (*toolkit.ToolResult, error) {
	symbol := codeParamString(params, "symbol")
	if symbol == "" {
		return codeParamError("code_references", "symbol 参数缺失或为空"), nil
	}
	kind := strings.ToLower(codeParamString(params, "kind"))
	if kind != "" && !validCodeRefKind(kind) {
		return codeParamError("code_references", "kind 只能是 reference|call|import|implement"), nil
	}
	limit, clamped := codeClampLimit(codeParamInt(params, "limit", 0), codeRefsDefaultLimit, codeRefsMaxLimit)
	return runCodeRefsQuery(
		ctx, &t.codeToolBase, "code_references",
		symbol, kind, codeParamString(params, "path_prefix"), limit, clamped,
	), nil
}

// runCodeRefsQuery 是 code_references / code_callers / code_navigate(refs) 的共享执行体。
//
// 口径：索引不可用 / 查询失败 / 零命中 → fallback 到 grep（04 §4.6）；
// 符号不在索引中时仍按名字查引用（引用可能指向未索引的符号，名字是唯一线索）。
func runCodeRefsQuery(ctx context.Context, base *codeToolBase, toolName, symbol, kind, pathPrefix string, limit int, clamped bool) *toolkit.ToolResult {
	grepParams := map[string]interface{}{"pattern": symbol, "literal": true}
	if pathPrefix != "" {
		grepParams["path"] = pathPrefix
	}

	handle, ok := base.resolveIndex(ctx)
	if !ok {
		result, _ := base.runGrep(ctx, grepParams)
		env := fallbackEnvelope(toolName, codeFallbackIndexUnavailable, codeConfidenceNone, "grep", result, nil)
		env.Truncated = env.Truncated || clamped
		annotateRefFallback(&env, kind)
		return codeResult(env)
	}
	// 陈旧度守卫（ADR-0004 §4.1/§5 D1）：关系类只有全开档可用——陈旧快照下
	// "10 秒前新增的调用者不存在"会变成静默错误，必须硬约束为实时兜底。
	if !relationTierUsable(handle) {
		result, _ := base.runGrep(ctx, grepParams)
		env := staleGuardEnvelope(toolName, handle, "grep", result)
		env.Truncated = env.Truncated || clamped
		annotateRefFallback(&env, kind)
		return codeResult(env)
	}

	sym, confidence, found, err := resolveCodeSymbol(ctx, handle.Index, symbol, pathPrefix)
	if err != nil {
		result, _ := base.runGrep(ctx, grepParams)
		env := fallbackEnvelope(toolName, codeFallbackIndexError, codeConfidenceNone, "grep", result, handle)
		env.Truncated = env.Truncated || clamped
		annotateRefFallback(&env, kind)
		return codeResult(env)
	}
	if !found {
		// 名字是唯一线索：按 ToSymbolName 查（conf 0.80，低于精确解析）。
		confidence = codeConfidenceFuzzy
	}

	refs, err := codeRefsQuery(ctx, handle.Index, sym, symbol, knowledge.RefKind(kind), pathPrefix, limit)
	if err != nil {
		result, _ := base.runGrep(ctx, grepParams)
		env := fallbackEnvelope(toolName, codeFallbackIndexError, confidence, "grep", result, handle)
		env.Truncated = env.Truncated || clamped
		annotateRefFallback(&env, kind)
		return codeResult(env)
	}
	// 部分解析的引用（ToSymbolID 为空）在按 id 查询时会落空：再按名字查一次，
	// 把"名字是唯一线索"的引用点捞回来（confidence 降到 0.80 口径）。
	if len(refs) == 0 && strings.TrimSpace(sym.ID) != "" {
		if nameOnly, nameErr := codeRefsQuery(ctx, handle.Index, knowledge.Symbol{}, symbol, knowledge.RefKind(kind), pathPrefix, limit); nameErr == nil && len(nameOnly) > 0 {
			refs = nameOnly
			confidence = codeConfidenceFuzzy
		}
	}
	// shadow 档（04 §4.6 第 3 步）：候选照算，但返回 grep 结果。
	if handle.Mode == knowledge.ModeShadow {
		result, _ := base.runGrep(ctx, grepParams)
		env := fallbackEnvelope(toolName, codeFallbackShadowMode, confidence, "grep", result, handle)
		env.Explanation += fmt.Sprintf(" 索引候选 %d 条（未返回）。", len(refs))
		env.Truncated = env.Truncated || clamped
		annotateRefFallback(&env, kind)
		return codeResult(env)
	}
	// Phase 4 语义通道：启用且可用时优先返回编译器级引用集合（不可用/失败/
	// 零命中自动回落到索引路径，语义通道不改变"永不失败"契约）。
	if env, ok := trySemanticRefsQuery(ctx, toolName, handle, sym, found, symbol, kind, limit, clamped); ok {
		return codeResult(*env)
	}
	if len(refs) == 0 {
		// 零命中：补一次 grep（索引引用是正则启发式，可能漏；grep 是兜底真值）。
		result, _ := base.runGrep(ctx, grepParams)
		env := fallbackEnvelope(toolName, codeFallbackNoIndexHit, confidence, "grep", result, handle)
		env.Truncated = env.Truncated || clamped
		annotateRefFallback(&env, kind)
		return codeResult(env)
	}

	// 文件级新鲜度守卫（ADR-0004 §4.1/§5 D1 的粒度补齐）。
	//
	// 上面的 relationTierUsable 看的是**全局**快照陈旧度。只要索引还在持续给
	// 别的文件写入，staleness_seconds 就一直停在新鲜档，而本次查询涉及的这些
	// 文件可能几小时没有被重新索引——"刚新增的调用者不存在"正是它挡不住的
	// 失效模式，且漏报完全静默（返回的那几条引用看起来正常）。
	//
	// 覆盖两个方向：符号定义文件（决定符号身份/范围是否还成立）与各引用所在
	// 文件（决定调用集合是否完整）。文件数受 limit 约束，stat/读盘代价有界。
	fileIDs := make([]string, 0, len(refs)+1)
	fileIDs = append(fileIDs, sym.FileID)
	for _, ref := range refs {
		fileIDs = append(fileIDs, ref.FileID)
	}
	stale, unverified := staleRelFiles(handle, fileIDs)
	if len(stale) > 0 {
		result, _ := base.runGrep(ctx, grepParams)
		env := staleFileEnvelope(toolName, handle, "grep", result, stale)
		env.Truncated = env.Truncated || clamped
		annotateRefFallback(&env, kind)
		return codeResult(env)
	}

	results := make([]codeRefHit, 0, len(refs))
	for _, ref := range refs {
		results = append(results, codeRefHitFrom(ref, handle))
	}
	env := newCodeEnvelope(toolName)
	env.Source = codeSourceIndex
	env.Confidence = confidence
	applySnapshot(&env, handle)
	env.Results = results
	env.Truncated = clamped || len(refs) >= limit
	env.Explanation = fmt.Sprintf(
		"按符号身份命中 %d 条引用（to_symbol_name=%q；引用为索引侧正则启发式，confidence 字段是引用自身置信度）。%s",
		len(refs), symbol, freshnessNote(handle, unverified),
	)
	return codeResult(env)
}
