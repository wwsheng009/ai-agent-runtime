package tools

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/knowledge"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolkit"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolresult"
	runtimetypes "github.com/wwsheng009/ai-agent-runtime/internal/types"
)

// CodeSearchTool 实现 code_search（06 §4 Phase 3 的 `code.search`）：
// 符号/FTS 检索的增强前端，不替换 grep（04 §4.6 决策）。
type CodeSearchTool struct {
	*toolkit.BaseTool
	codeToolBase
}

// NewCodeSearchTool 创建 code_search 工具。
func NewCodeSearchTool() *CodeSearchTool {
	parameters := map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"query": map[string]interface{}{
				"type":        "string",
				"description": "查询串（符号名或关键字）。索引可用时在 symbols_fts 上检索；索引不可用时按字面量交给 grep。",
			},
			"lang": map[string]interface{}{
				"type":        "string",
				"description": "可选：语言标签过滤（如 go / python / typescript）；仅索引路径生效。",
			},
			"path_prefix": map[string]interface{}{
				"type":        "string",
				"description": "可选：限定 workspace 相对路径前缀（如 backend/internal/）。",
			},
			"limit": map[string]interface{}{
				"type":        "integer",
				"description": fmt.Sprintf("可选：返回条数上限（默认 %d，最大 %d）。", codeSearchDefaultLimit, codeSearchMaxLimit),
			},
		},
		"required": []string{"query"},
	}
	return &CodeSearchTool{BaseTool: toolkit.NewBaseTool(
		"code_search",
		"符号级代码检索（索引增强，设计文档中的 `code.search`）。"+
			"做符号级问题（“X 定义在哪”“有哪些同名符号”）时优先使用；文本/配置/日志检索用 grep。"+
			"索引不可用时自动降级为 grep，返回结构不变（source=fallback）。"+
			codeEnvelopeSemantics,
		"1.0.0",
		parameters,
		true,
	)}
}

// DefinitionMetadata 声明只读语义：只读子代理可用、可并行、重试安全。
func (t *CodeSearchTool) DefinitionMetadata() map[string]interface{} {
	return codeReadOnlyMetadata()
}

// Execute 实现 Tool 接口。
func (t *CodeSearchTool) Execute(ctx context.Context, params map[string]interface{}) (*toolkit.ToolResult, error) {
	query := codeParamString(params, "query")
	if query == "" {
		return codeParamError("code_search", "query 参数缺失或为空"), nil
	}
	lang := codeParamString(params, "lang")
	pathPrefix := codeParamString(params, "path_prefix")
	limit, clamped := codeClampLimit(codeParamInt(params, "limit", 0), codeSearchDefaultLimit, codeSearchMaxLimit)

	grepParams := func() map[string]interface{} {
		fallback := map[string]interface{}{"pattern": query, "literal": true}
		if pathPrefix != "" {
			fallback["path"] = pathPrefix
		}
		return fallback
	}

	handle, ok := t.resolveIndex(ctx)
	if !ok {
		result, _ := t.runGrep(ctx, grepParams())
		env := fallbackEnvelope("code_search", codeFallbackIndexUnavailable, codeConfidenceNone, "grep", result, nil)
		env.Truncated = env.Truncated || clamped
		env.Explanation += codeSearchLangFallbackNote(lang)
		return codeResult(env), nil
	}
	// 陈旧度守卫（ADR-0004 §4.1）：定义类在过旧档不返回索引结果，实时兜底。
	if !definitionTierUsable(handle) {
		result, _ := t.runGrep(ctx, grepParams())
		env := staleGuardEnvelope("code_search", handle, "grep", result)
		env.Truncated = env.Truncated || clamped
		env.Explanation += codeSearchLangFallbackNote(lang)
		return codeResult(env), nil
	}

	hits, err := handle.Index.Search(ctx, knowledge.SearchQuery{
		Text:       query,
		Lang:       lang,
		PathPrefix: pathPrefix,
		Limit:      limit,
	})
	if err != nil {
		result, _ := t.runGrep(ctx, grepParams())
		env := fallbackEnvelope("code_search", codeFallbackIndexError, codeConfidenceNone, "grep", result, handle)
		env.Truncated = env.Truncated || clamped
		env.Explanation += codeSearchLangFallbackNote(lang)
		return codeResult(env), nil
	}

	// 限定名（a.b / a.b.c）尾段精确解析：FTS 对这类查询常按 token 散列而漏掉
	// 意图符号；限定前缀匹配的符号会被前置（不匹配则不补，避免误报）。
	hits, qualifiedExact := withQualifiedTailHits(ctx, handle, query, pathPrefix, hits)
	// 精确名优先重排：让 code_search("PlanInput") 的类型本体排在
	// SessionSubscriptionPlanInput 这类子串命中之前。
	hits = orderCodeSearchHits(hits, query)

	// shadow 档（04 §4.6 第 3 步）：索引候选照算，但返回 grep 结果，不改变
	// 模型可见输出；候选数量写进 explanation 供对比观察。
	if handle.Mode == knowledge.ModeShadow {
		result, _ := t.runGrep(ctx, grepParams())
		env := fallbackEnvelope("code_search", codeFallbackShadowMode, codeConfidenceFTS, "grep", result, handle)
		env.Explanation += fmt.Sprintf(" 索引候选 %d 条（未返回）。", len(hits))
		env.Explanation += codeSearchLangFallbackNote(lang)
		env.Truncated = env.Truncated || clamped
		return codeResult(env), nil
	}

	// on 档无命中：补一次 grep 并合并（04 §4.6 第 4 步的"结果为空"分支）。
	if len(hits) == 0 {
		result, _ := t.runGrep(ctx, grepParams())
		env := fallbackEnvelope("code_search", codeFallbackNoIndexHit, codeConfidenceNone, "grep", result, handle)
		env.Truncated = env.Truncated || clamped
		env.Explanation += codeSearchLangFallbackNote(lang)
		return codeResult(env), nil
	}

	env := newCodeEnvelope("code_search")
	env.Source = codeSourceIndex
	env.Confidence = codeConfidenceFTS
	applySnapshot(&env, handle)
	if qualifiedExact {
		env.Confidence = codeConfidenceExact
	}
	env.Results = codeSearchHits(hits, handle)
	env.Truncated = clamped || len(hits) >= limit
	env.Explanation = fmt.Sprintf(
		"symbols_fts 返回 %d 条（精确名优先排序；score 仅在本结果集内可比，v1 不分页，next_cursor 为空）。",
		len(hits),
	)
	if qualifiedExact {
		env.Explanation += " 限定名尾段解析命中（confidence=1.0）。"
	}
	if env.Truncated {
		env.Explanation += fmt.Sprintf(" 受 limit=%d 窗口约束，命中总数可能多于返回数。", limit)
	}
	// 低置信补量（04 §4.6 第 4 步）：命中里没有任何与查询同名/包含的符号时，
	// 视为低相关，补一次 grep 作为补充证据（索引命中仍是主结果）。
	if !qualifiedExact && !codeSearchNameMatched(hits, query) {
		if result, _ := t.runGrep(ctx, grepParams()); result != nil && strings.TrimSpace(result.Content) != "" {
			env.Source = codeSourceIndexGrep
			env.Fallback = &codeFallback{
				Tool:   "grep",
				Reason: codeFallbackLowConfidence,
				Output: strings.TrimSpace(result.Content),
			}
			env.Explanation += " 低相关补量：补一次 grep 作为补充证据（source=index+grep，索引命中仍是主结果）。"
			if codeResultTruncated(result) {
				env.Truncated = true
			}
		}
	}
	// 语义消歧补充：workspace/symbol 是唯一能回答"有哪些精确同名符号"的通道
	//（definition/references 需要位置锚点，这里没有）。它只做补充——命中追加
	// 在索引结果之后，不替换主结果集，因此延迟有界、失败不影响主路径。
	if semEnv, ok := trySemanticWorkspaceSymbols(ctx, handle, query, limit); ok {
		// env.Results 是 interface{}（不同工具的结果形状不同），这里显式收回
	// 本工具已知的 []codeSymbolHit 再合并，避免依赖类型断言的静默失败。
		base, _ := env.Results.([]codeSymbolHit)
		extra, _ := semEnv.Results.([]codeSymbolHit)
		env.Results = append(base, extra...)
		if env.Source == codeSourceIndex {
			env.Source = codeSourceIndexSemantic
		}
		env.Explanation += semEnv.Explanation
	}
	return codeResult(env), nil
}

// orderCodeSearchHits 按"精确名 > 前缀 > 包含 > 其他"稳定重排命中：
// FTS 的相关性排序会把子串命中排在精确名之前，符号级检索应先给精确名。
func orderCodeSearchHits(hits []knowledge.SearchHit, query string) []knowledge.SearchHit {
	q := strings.ToLower(strings.TrimSpace(query))
	if q == "" || len(hits) < 2 {
		return hits
	}
	rank := func(hit knowledge.SearchHit) int {
		name := strings.ToLower(strings.TrimSpace(hit.Name))
		qualified := strings.ToLower(strings.TrimSpace(hit.QualifiedName))
		switch {
		case name == q || qualified == q:
			return 0
		case strings.HasPrefix(name, q) || strings.HasPrefix(qualified, q):
			return 1
		case strings.Contains(name, q) || strings.Contains(qualified, q):
			return 2
		default:
			return 3
		}
	}
	ordered := make([]knowledge.SearchHit, len(hits))
	copy(ordered, hits)
	sort.SliceStable(ordered, func(i, j int) bool { return rank(ordered[i]) < rank(ordered[j]) })
	return ordered
}

// withQualifiedTailHits 对限定名查询做尾段精确解析，并把限定前缀匹配的符号
// 前置；返回是否命中完整限定名（决定 confidence 与是否需要低相关补量）。
func withQualifiedTailHits(ctx context.Context, handle *CodeIndexHandle, query, pathPrefix string, hits []knowledge.SearchHit) ([]knowledge.SearchHit, bool) {
	trimmed := strings.TrimSpace(query)
	dot := strings.LastIndex(trimmed, ".")
	if dot <= 0 || dot == len(trimmed)-1 {
		return hits, false
	}
	qualifier := strings.ToLower(strings.TrimSpace(trimmed[:dot]))
	tail := strings.TrimSpace(trimmed[dot+1:])
	if qualifier == "" || tail == "" || strings.ContainsAny(tail, " \t") {
		return hits, false
	}
	syms, err := handle.Index.FindSymbols(ctx, knowledge.SymbolQuery{
		Name: tail, Exact: true, PathPrefix: pathPrefix, Limit: codeSymbolsDefaultLimit,
	})
	if err != nil || len(syms) == 0 {
		return hits, false
	}
	seen := make(map[string]bool, len(hits))
	for _, hit := range hits {
		if hit.SymbolID != "" {
			seen[hit.SymbolID] = true
		}
	}
	prepended := make([]knowledge.SearchHit, 0, len(syms))
	exact := false
	for _, sym := range syms {
		if sym.DeletedAt != 0 {
			continue
		}
		qualified := strings.ToLower(strings.TrimSpace(sym.QualifiedName))
		switch {
		case strings.EqualFold(sym.QualifiedName, trimmed):
			exact = true
		case !strings.HasPrefix(qualified, qualifier+".") && !strings.Contains(qualified, qualifier):
			// 限定前缀不匹配的尾段同名符号不补（避免 "a.b" 类查询引入误报）。
			continue
		}
		if sym.ID != "" && seen[sym.ID] {
			continue
		}
		prepended = append(prepended, knowledge.SearchHit{
			SymbolID:      sym.ID,
			Name:          sym.Name,
			QualifiedName: sym.QualifiedName,
			Kind:          sym.Kind,
			Path:          handle.PathForFile(sym.FileID),
			Language:      sym.Language,
			Line:          sym.Range.Start.Line,
			Signature:     sym.Signature,
		})
	}
	if len(prepended) == 0 {
		return hits, exact
	}
	return append(prepended, hits...), exact
}

// codeSearchLangFallbackNote 说明 fallback 与索引路径的过滤差异（grep 无语言维度）。
func codeSearchLangFallbackNote(lang string) string {
	if strings.TrimSpace(lang) == "" {
		return ""
	}
	return "；fallback 未应用 lang 过滤（grep 无语言维度，按字面量检索）。"
}

// codeSearchHits 把 FTS 命中映射为稳定结果形状。
func codeSearchHits(hits []knowledge.SearchHit, handle *CodeIndexHandle) []codeSymbolHit {
	results := make([]codeSymbolHit, 0, len(hits))
	for _, hit := range hits {
		path := hit.Path
		if path == "" {
			path = handle.PathForFile(hit.SymbolID)
		}
		results = append(results, codeSymbolHit{
			Path:          path,
			Name:          hit.Name,
			QualifiedName: hit.QualifiedName,
			Kind:          string(hit.Kind),
			Signature:     hit.Signature,
			Range:         codeRange{Path: path, StartLine: hit.Line},
		})
	}
	return results
}

// codeReadOnlyMetadata 是 code.* 的只读语义声明（与 artifact_read 同口径）。
func codeReadOnlyMetadata() map[string]interface{} {
	return map[string]interface{}{
		runtimetypes.ToolMetadataKindKey:             runtimetypes.ToolKindRead,
		runtimetypes.ToolMetadataReadOnlyKey:         true,
		runtimetypes.ToolMetadataMutatesFSKey:        false,
		runtimetypes.ToolMetadataRequiresNetKey:      false,
		runtimetypes.ToolMetadataSupportsParallelKey: true,
		runtimetypes.ToolMetadataRetryClassKey:       runtimetypes.ToolRetryClassSafe,
	}
}

// codeParamError 返回参数错误的统一形状（调用方输入错误，不是降级）。
func codeParamError(tool, message string) *toolkit.ToolResult {
	return stampToolOwnsOutput(&toolkit.ToolResult{
		Success:    false,
		OutputKind: toolresult.KindText,
		Error:      fmt.Errorf("%s: %s", tool, message),
	})
}
