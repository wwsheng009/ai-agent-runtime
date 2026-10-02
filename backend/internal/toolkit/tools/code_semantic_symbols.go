package tools

import (
	"context"
	"fmt"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/knowledge"
)

// code_semantic_symbols.go 是语义通道符号面的工具侧接线（Phase 4 补齐）：
// 把 knowledge 层的 DocumentSymbolAdapter / WorkspaceSymbolAdapter 接到
// code_navigate(members) 与 code_search 上。
//
// 三条通用口径（与 code_common.go 的引用类语义面一致）：
//   - 优先级：语义 → 索引 → grep/view；语义不可用/失败/零命中一律返回
//     (nil, false) 让调用方走索引路径，绝不让 LSP 故障变成工具失败；
//   - 位置口径：适配器出参已是 canonical 0-based，写入结果时 +1 转 1-based
//     （与 trySemanticRefsQuery / semanticDefinitionAt 同一次转换位置）；
//   - 信封：source=lsp、confidence=1.0（语义面是编译器级，不是启发式）。

// semanticMembers 用语义通道列出文件内定义的符号。
func semanticMembers(ctx context.Context, handle *CodeIndexHandle, path string, limit int, clamped bool) (*codeEnvelope, bool) {
	if handle == nil || handle.Semantic == nil {
		return nil, false
	}
	adapter, ok := handle.Semantic.(knowledge.DocumentSymbolAdapter)
	if !ok {
		return nil, false
	}
	syms, err := adapter.DocumentSymbols(ctx, path)
	if err != nil || len(syms) == 0 {
		return nil, false
	}
	// 只保留目标文件本身：workspace/symbol 形状的实现理论上会带出别的文件，
	// 且 path 归一化口径必须与索引侧一致（normalizeCodePath 已在前序做过）。
	filtered := make([]knowledge.SemanticSymbol, 0, len(syms))
	for _, sym := range syms {
		if normalizeCodePath(sym.Path) != path {
			continue
		}
		filtered = append(filtered, sym)
	}
	if len(filtered) == 0 {
		return nil, false
	}
	truncated := clamped
	if len(filtered) > limit {
		filtered = filtered[:limit]
		truncated = true
	}
	results := make([]codeSymbolHit, 0, len(filtered))
	for _, sym := range filtered {
		results = append(results, semanticSymbolHit(sym))
	}
	env := newCodeEnvelope("code_navigate")
	env.Source = codeSourceSemantic
	env.Confidence = codeConfidenceExact
	applySnapshot(&env, handle)
	env.Results = results
	env.Truncated = truncated
	env.Explanation = fmt.Sprintf(
		"语义通道（%s）按当前磁盘内容列出 %s 内 %d 个符号（编译器级口径，不依赖索引快照）。",
		handle.Semantic.Version(), path, len(results),
	)
	return &env, true
}

// semanticSymbolHits 把 workspace/symbol 结果映射为 code_search 的结果形状。
func semanticSymbolHits(syms []knowledge.SemanticSymbol, limit int, truncated bool) []codeSymbolHit {
	results := make([]codeSymbolHit, 0, len(syms))
	for _, sym := range syms {
		results = append(results, semanticSymbolHit(sym))
		if len(results) >= limit {
			break
		}
	}
	return results
}

func semanticSymbolHit(sym knowledge.SemanticSymbol) codeSymbolHit {
	// 位置口径：canonical 0-based → 结果 1-based。end 缺失（0）或早于 start 时
	// 夹紧到 start：真实 server 不会给这种范围，但输出一段倒挂的 range 会让
	// 模型当成真实范围去读（比只给 start 更糟）。
	startLine, startCol := sym.Line+1, sym.Col
	endLine, endCol := sym.EndLine+1, sym.EndCol
	if endLine < startLine || (endLine == startLine && endCol < startCol) {
		endLine, endCol = startLine, startCol
	}
	hit := codeSymbolHit{
		Path: normalizeCodePath(sym.Path),
		Name: sym.Name,
		Kind: string(sym.Kind),
		Range: codeRange{
			Path:      normalizeCodePath(sym.Path),
			StartLine: startLine,
			StartCol:  startCol,
			EndLine:   endLine,
			EndCol:    endCol,
		},
	}
	// QualifiedName 用容器链拼出（workspace/symbol 的扁平形状自带 containerName，
	// 层级形状由解码器展平填入）——与索引侧 QualifiedName 同口径，模型才能
	// 区分同名符号。
	if sym.Container != "" {
		hit.QualifiedName = sym.Container + "." + sym.Name
	} else {
		hit.QualifiedName = sym.Name
	}
	// LSP 的 detail 是签名/类型串，形态接近索引侧 signature；只在非空时给，
	// 避免用空串覆盖"无签名"这个事实。
	hit.Signature = strings.TrimSpace(sym.Detail)
	return hit
}

// trySemanticWorkspaceSymbols 用语义通道回答"按名字找符号"。
//
// 与引用类语义面的关键差异：这里**没有位置锚点**，所以它不能被索引结果替代，
// 只能作为索引之后的补充（去同名歧义），而不是索引命中为空时的兜底——
// workspace/symbol 在 gopls 上要求工作区已加载，冷启动时可能整体超时，
// 把它放在主路径会让 code_search 的延迟不可控。
//
// 返回 (env, true) 表示已产出语义结果；否则调用方保持索引路径不变。
func trySemanticWorkspaceSymbols(ctx context.Context, handle *CodeIndexHandle, query string, limit int) (*codeEnvelope, bool) {
	if handle == nil || handle.Semantic == nil {
		return nil, false
	}
	adapter, ok := handle.Semantic.(knowledge.WorkspaceSymbolAdapter)
	if !ok {
		return nil, false
	}
	trimmed := strings.TrimSpace(query)
	if trimmed == "" {
		return nil, false
	}
	syms, err := adapter.WorkspaceSymbols(ctx, trimmed, limit)
	if err != nil || len(syms) == 0 {
		return nil, false
	}
	// 只保留精确同名：workspace/symbol 是子串/模糊匹配，返回子串命中会让
	// code_search 的主结果集比 FTS 还噪，反而帮倒忙。精确同名才有消歧价值。
	exact := make([]knowledge.SemanticSymbol, 0, len(syms))
	for _, sym := range syms {
		if sym.Name == trimmed {
			exact = append(exact, sym)
		}
	}
	if len(exact) == 0 {
		return nil, false
	}
	// source 用 index+lsp：语义命中是**补充**而非主结果（FTS 仍是主结果集），
	// 直接标 lsp 会让模型误以为整份结果都是编译器给的。
	env := newCodeEnvelope("code_search")
	env.Source = codeSourceIndexSemantic
	env.Confidence = codeConfidenceExact
	applySnapshot(&env, handle)
	env.Results = semanticSymbolHits(exact, limit, false)
	env.Explanation = fmt.Sprintf(
		"语义通道（%s，编译器级）精确同名命中 %d 个，已与索引结果合并（source=index+lsp）。",
		handle.Semantic.Version(), len(exact),
	)
	return &env, true
}