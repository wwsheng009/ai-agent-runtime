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
				"description": "符号名；direction=definition（按名字）|refs 时必填。",
			},
			"file_path": map[string]interface{}{
				"type":        "string",
				"description": "文件路径；direction=members 时必填（列出该文件定义的符号）；direction=definition 时与 line 组合做**按位置查定义**。",
			},
			"line": map[string]interface{}{
				"type":        "integer",
				"description": "1-based 行号；direction=definition 按位置查询时与 file_path 一起给出（如光标所在行）。",
			},
			"col": map[string]interface{}{
				"type":        "integer",
				"description": "可选 1-based 列号；缺省时自动取该行的标识符逐个尝试（有界）。",
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
		"代码图遍历（索引增强，设计文档中的 `code.navigate`）：符号定义位置（按名字，或按 file_path+line 查该处引用目标的定义）、文件内符号、一跳引用。"+
			"参数映射：direction=definition 需 symbol，或 file_path+line（可选 col，按光标位置反查定义）；"+
			"direction=members 需 file_path（列出该文件定义的符号，可替代逐个 grep）；"+
			"direction=refs 需 symbol（一跳引用，等同 code_references 的无 kind 过滤形态）。"+
			"省略 direction 时按已给参数自动推断（有 symbol→definition，有 file_path→members），两者都没有则报参数错误。"+
			"文件/目录列举请用 glob/ls；索引不可用时按方向降级到 grep / view（source=fallback）。"+
			codeEnvelopeSemantics,
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
		switch {
		case symbol != "":
			direction = "definition"
		case filePath != "":
			direction = "members"
		default:
			return codeParamError("code_navigate", "direction 必填（definition|members|refs）"), nil
		}
	}
	limit, clamped := codeClampLimit(codeParamInt(params, "limit", 0), codeRefsDefaultLimit, codeRefsMaxLimit)

	switch direction {
	case "definition":
		switch {
		case symbol != "":
			return t.navigateDefinition(ctx, symbol), nil
		case filePath != "" && codeParamInt(params, "line", 0) > 0:
			return t.navigateDefinitionAtPosition(ctx, filePath, codeParamInt(params, "line", 0), codeParamInt(params, "col", 0), limit, clamped), nil
		default:
			return codeParamError("code_navigate", "direction=definition 需要 symbol，或 file_path+line（按位置查定义）"), nil
		}
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
func (t *CodeNavigateTool) navigateDefinition(ctx context.Context, symbol string) *toolkit.ToolResult {
	grepParams := map[string]interface{}{"pattern": symbol, "literal": true}
	handle, ok := t.resolveIndex(ctx)
	if !ok {
		result, _ := t.runGrep(ctx, grepParams)
		env := fallbackEnvelope("code_navigate", codeFallbackIndexUnavailable, codeConfidenceNone, "grep", result, nil)
		return codeResult(env)
	}
	// 陈旧度守卫（ADR-0004 §4.1）：definition 是定义类，仅过旧档硬闸。
	if !definitionTierUsable(handle) {
		result, _ := t.runGrep(ctx, grepParams)
		return codeResult(staleGuardEnvelope("code_navigate", handle, "grep", result))
	}
	sym, confidence, found, err := resolveCodeSymbol(ctx, handle.Index, symbol, "")
	if err != nil || !found {
		reason := codeFallbackNoIndexHit
		if err != nil {
			reason = codeFallbackIndexError
		}
		result, _ := t.runGrep(ctx, grepParams)
		env := fallbackEnvelope("code_navigate", reason, confidence, "grep", result, handle)
		return codeResult(env)
	}
	// shadow 档（04 §4.6 第 3 步）：候选照算，但返回 grep 结果。
	if handle.Mode == knowledge.ModeShadow {
		result, _ := t.runGrep(ctx, grepParams)
		env := fallbackEnvelope("code_navigate", codeFallbackShadowMode, confidence, "grep", result, handle)
		env.Explanation += " 索引候选 1 条（未返回）。"
		return codeResult(env)
	}
	path := handle.PathForFile(sym.FileID)
	// 文件级新鲜度守卫（定义类，见 code_inspect 同处注释）：行号范围来自索引
	// 快照，文件改写后它可能指向磁盘上完全不同的代码——而"定义在第 N 行"的
	// 答案看起来总是合理的，正是它最难被发现。
	if known, fresh := fileFresh(handle, sym.FileID); known && !fresh {
		result, _ := t.runGrep(ctx, grepParams)
		env := staleFileEnvelope("code_navigate", handle, "grep", result, []string{path})
		return codeResult(env)
	}
	hit := codeSymbolHitFrom(sym, path)
	env := newCodeEnvelope("code_navigate")
	env.Source = codeSourceIndex
	env.Confidence = confidence
	applySnapshot(&env, handle)
	env.Range = &hit.Range
	env.Results = []codeSymbolHit{hit}
	env.Explanation = fmt.Sprintf(
		"定义位置：%s（%s，第 %d–%d 行）；正文用 code_inspect 或 view 读取。%s",
		sym.Name, path, sym.Range.Start.Line, sym.Range.End.Line,
		freshnessNote(handle, 0),
	)
	return codeResult(env)
}

// navigateDefinitionAtPosition 按 (file, line[, col]) 查定义：语义通道优先
// （编译器级），索引反查兜底（该行引用 → 目标符号；否则所在符号），最后退化为
// 读取该行。语义查询需要"声明位置"，因此这是 definition 语义面唯一的按位置入口。
func (t *CodeNavigateTool) navigateDefinitionAtPosition(ctx context.Context, filePath string, line, col, limit int, clamped bool) *toolkit.ToolResult {
	normalized := normalizeCodePath(filePath)
	handle, ok := t.resolveIndex(ctx)
	if !ok {
		result, _ := t.runView(ctx, map[string]interface{}{"file_path": normalized, "offset": line - 1, "limit": 5})
		env := fallbackEnvelope("code_navigate", codeFallbackIndexUnavailable, codeConfidenceNone, "view", result, nil)
		env.Explanation += " 按位置查定义需要索引句柄（语义通道挂在索引句柄上）；已降级为读取该行。"
		return codeResult(env)
	}
	// 陈旧度守卫（ADR-0004 §4.1）：按位置查定义属定义类，仅过旧档硬闸。
	if !definitionTierUsable(handle) {
		result, _ := t.runView(ctx, map[string]interface{}{"file_path": normalized, "offset": line - 1, "limit": 5})
		return codeResult(staleGuardEnvelope("code_navigate", handle, "view", result))
	}
	// 1) 语义通道（编译器级口径）。
	if env, ok := semanticDefinitionAt(ctx, handle, normalized, line, col, limit, clamped); ok {
		return codeResult(*env)
	}
	// 2) 索引反查。
	if env, ok := indexDefinitionAt(ctx, handle, normalized, line, limit); ok {
		return codeResult(*env)
	}
	// 3) 退化：读取该行附近（至少让模型看到内容）。
	result, _ := t.runView(ctx, map[string]interface{}{"file_path": normalized, "offset": line - 1, "limit": 5})
	env := fallbackEnvelope("code_navigate", codeFallbackNoIndexHit, codeConfidenceNone, "view", result, handle)
	env.Explanation += fmt.Sprintf(" 未能在 %s:%d 解析出符号（语义通道不可用且索引无该行记录）；已返回该行附近内容。", normalized, line)
	return codeResult(env)
}

// semanticDefinitionAt 用语义通道解析 (file, line, col) 处的定义。
//
// col 缺省时按行内标识符逐个尝试（有界，最多 4 个候选）：模型常只知道"行"。
// 返回 (env, true) 表示已产出结果；失败/不可用返回 (nil, false) 由调用方降级。
func semanticDefinitionAt(ctx context.Context, handle *CodeIndexHandle, path string, line, col, limit int, clamped bool) (*codeEnvelope, bool) {
	if handle == nil || handle.Semantic == nil {
		return nil, false
	}
	line0 := line - 1
	lines := readWorkspaceLines(handle.Root, path)
	if line0 < 0 || line0 >= len(lines) {
		return nil, false
	}
	text := lines[line0]
	candidates := make([]int, 0, 4)
	if col > 0 {
		candidates = append(candidates, col-1)
	} else {
		candidates = identifierColumns(text, 4)
	}
	for _, candidate := range candidates {
		locs, err := handle.Semantic.Definition(ctx, path, line0, candidate)
		if err != nil || len(locs) == 0 {
			continue
		}
		results := make([]codeSymbolHit, 0, len(locs))
		for _, loc := range locs {
			name := identifierAt(readWorkspaceLines(handle.Root, loc.Path), loc.Line, loc.Col)
			results = append(results, codeSymbolHit{
				Path: loc.Path,
				Name: name,
				Range: codeRange{
					Path:      loc.Path,
					StartLine: loc.Line + 1, // canonical 0-based → 结果 1-based
					StartCol:  loc.Col,
					EndLine:   loc.Line + 1,
					EndCol:    loc.Col + len(name),
				},
			})
		}
		truncated := clamped
		if len(results) > limit {
			results = results[:limit]
			truncated = true
		}
		env := newCodeEnvelope("code_navigate")
		env.Source = codeSourceSemantic
		env.Confidence = codeConfidenceExact
		applySnapshot(&env, handle)
		env.Results = results
		env.Truncated = truncated
		env.Explanation = fmt.Sprintf(
			"语义通道（%s）在 %s:%d:%d 处的定义，共 %d 处；正文用 code_inspect 或 view 读取。",
			handle.Semantic.Version(), path, line, candidate+1, len(results),
		)
		return &env, true
	}
	return nil, false
}

// indexDefinitionAt 用索引反查 (file, line) 处的定义：
// 优先该行已解析的引用（目标符号），否则返回所在符号（明确标注，不假装是引用目标）。
func indexDefinitionAt(ctx context.Context, handle *CodeIndexHandle, path string, line, limit int) (*codeEnvelope, bool) {
	if handle == nil || handle.Index == nil {
		return nil, false
	}
	refs, err := handle.Index.FindRefs(ctx, knowledge.RefQuery{PathPrefix: path, Limit: 200})
	if err == nil {
		for _, ref := range refs {
			if handle.PathForFile(ref.FileID) != path || ref.Line != line || ref.ToSymbolID == "" {
				continue
			}
			sym, confidence, found := symbolByID(ctx, handle.Index, ref.ToSymbolID, ref.ToSymbolName)
			if !found {
				continue
			}
			hit := codeSymbolHitFrom(sym, handle.PathForFile(sym.FileID))
			env := newCodeEnvelope("code_navigate")
			env.Source = codeSourceIndex
			env.Confidence = confidence
			applySnapshot(&env, handle)
			env.Range = &hit.Range
			env.Results = []codeSymbolHit{hit}
			env.Explanation = fmt.Sprintf(
				"索引反查：%s:%d 的引用目标是 %s（%s，第 %d–%d 行）。",
				path, line, sym.Name, hit.Path, sym.Range.Start.Line, sym.Range.End.Line,
			)
			return &env, true
		}
	}
	symbols, err := handle.Index.FindSymbols(ctx, knowledge.SymbolQuery{PathPrefix: path, Limit: 200})
	if err != nil {
		return nil, false
	}
	for _, sym := range symbols {
		if handle.PathForFile(sym.FileID) != path {
			continue
		}
		if sym.Range.Start.Line <= line && line <= sym.Range.End.Line {
			hit := codeSymbolHitFrom(sym, path)
			env := newCodeEnvelope("code_navigate")
			env.Source = codeSourceIndex
			env.Confidence = codeConfidenceFTS
			applySnapshot(&env, handle)
			env.Range = &hit.Range
			env.Results = []codeSymbolHit{hit}
			env.Explanation = fmt.Sprintf(
				"该行没有可解析的引用；返回所在符号 %s（第 %d–%d 行）。引用目标的定义需要语义通道（knowledge.lsp.enabled=true + mode=self）。",
				sym.Name, sym.Range.Start.Line, sym.Range.End.Line,
			)
			return &env, true
		}
	}
	return nil, false
}

// symbolByID 按符号 id 取符号（索引查询面按名字/路径过滤，这里用名字+精确匹配
// 再按 id 校验，避免为单个用例扩展 store 接口）。
func symbolByID(ctx context.Context, idx CodeIndex, id, name string) (knowledge.Symbol, float64, bool) {
	if id == "" || name == "" {
		return knowledge.Symbol{}, codeConfidenceNone, false
	}
	symbols, err := idx.FindSymbols(ctx, knowledge.SymbolQuery{Name: name, Exact: true, Limit: 50})
	if err != nil {
		return knowledge.Symbol{}, codeConfidenceNone, false
	}
	for _, sym := range symbols {
		if sym.ID == id {
			return sym, codeConfidenceExact, true
		}
	}
	return knowledge.Symbol{}, codeConfidenceNone, false
}

// identifierColumns 返回行内标识符的起始列（字节偏移，0-based，最多 max 个）。
// 用于 col 缺省时逐个尝试语义查询；跳过 Go 关键字与单字符噪声。
func identifierColumns(line string, max int) []int {
	out := make([]int, 0, max)
	for i := 0; i < len(line) && len(out) < max; {
		c := line[i]
		if !isIdentStart(c) {
			i++
			continue
		}
		start := i
		for i < len(line) && isIdentPart(line[i]) {
			i++
		}
		name := line[start:i]
		if len(name) > 1 && !goKeywords[name] {
			out = append(out, start)
		}
	}
	return out
}

// identifierAt 读取 (line, col) 处的标识符文本；读不到返回空串。
func identifierAt(lines []string, line, col int) string {
	if line < 0 || line >= len(lines) {
		return ""
	}
	text := lines[line]
	if col < 0 || col >= len(text) {
		return ""
	}
	start := col
	for start > 0 && isIdentPart(text[start-1]) {
		start--
	}
	end := col
	for end < len(text) && isIdentPart(text[end]) {
		end++
	}
	return text[start:end]
}

func isIdentStart(c byte) bool {
	return c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

func isIdentPart(c byte) bool {
	return isIdentStart(c) || (c >= '0' && c <= '9')
}

// goKeywords 用于 identifierColumns 的候选过滤（不是完整词法分析）。
var goKeywords = map[string]bool{
	"break": true, "case": true, "chan": true, "const": true, "continue": true,
	"default": true, "defer": true, "else": true, "fallthrough": true, "for": true,
	"func": true, "go": true, "goto": true, "if": true, "import": true,
	"interface": true, "map": true, "package": true, "range": true, "return": true,
	"select": true, "struct": true, "switch": true, "type": true, "var": true,
}

// navigateMembers 列出文件内定义的符号（只做符号枚举，不做文件列举）。
func (t *CodeNavigateTool) navigateMembers(ctx context.Context, filePath string, limit int, clamped bool) *toolkit.ToolResult {
	normalized := normalizeCodePath(filePath)
	handle, ok := t.resolveIndex(ctx)
	if !ok {
		result, _ := t.runView(ctx, map[string]interface{}{"file_path": normalized, "limit": 200})
		env := fallbackEnvelope("code_navigate", codeFallbackIndexUnavailable, codeConfidenceNone, "view", result, nil)
		env.Explanation += " members 需要索引；已降级为读取文件头部，请用 grep 继续定位具体成员。"
		env.Truncated = env.Truncated || clamped
		return codeResult(env)
	}
	// 陈旧度守卫（ADR-0004 §4.1）：members 是定义类，仅过旧档硬闸。
	if !definitionTierUsable(handle) {
		result, _ := t.runView(ctx, map[string]interface{}{"file_path": normalized, "limit": 200})
		env := staleGuardEnvelope("code_navigate", handle, "view", result)
		env.Truncated = env.Truncated || clamped
		return codeResult(env)
	}

	// 文件级新鲜度守卫（定义类）：members 是"文件内符号 + 行号"的清单，文件
	// 在索引之后被改写时两者都不可信（可能少符号、也可能行号全偏）。
	if fileID := handle.FileIDForPath(normalized); fileID != "" {
		if known, fresh := fileFresh(handle, fileID); known && !fresh {
			result, _ := t.runView(ctx, map[string]interface{}{"file_path": normalized, "limit": 200})
			env := staleFileEnvelope("code_navigate", handle, "view", result, []string{normalized})
			env.Truncated = env.Truncated || clamped
			return codeResult(env)
		}
	}

	// 语义通道（编译器级符号表）：放在两道新鲜度守卫之后。
	//
	// 为什么放在守卫之后：守卫的语义是"索引快照不可信就别给行号"，而语义通道
	// 读的是实时磁盘、不依赖索引快照，因此守卫命中时它反而是最可信的答案。
	// 但为了不推翻 ADR-0004 已定的降级口径（陈旧即降级是显式契约），这里选择
	// 保守：不绕过守卫，只在守卫放行后把精度从索引提到语义。
	if env, ok := semanticMembers(ctx, handle, normalized, limit, clamped); ok {
		return codeResult(*env)
	}

	// PathPrefix 是前缀语义（可能带出子目录文件）：先放大候选窗口，过滤出
	// 目标文件本身后再按调用方 limit 截断，避免前缀噪音挤掉目标文件成员。
	queryLimit := limit
	if queryLimit < codeRefsMaxLimit {
		if queryLimit*4 > codeRefsMaxLimit {
			queryLimit = codeRefsMaxLimit
		} else {
			queryLimit *= 4
		}
	}
	syms, err := handle.Index.FindSymbols(ctx, knowledge.SymbolQuery{PathPrefix: normalized, Limit: queryLimit})
	if err != nil {
		result, _ := t.runView(ctx, map[string]interface{}{"file_path": normalized, "limit": 200})
		env := fallbackEnvelope("code_navigate", codeFallbackIndexError, codeConfidenceNone, "view", result, handle)
		env.Truncated = env.Truncated || clamped
		return codeResult(env)
	}

	// shadow 档（04 §4.6 第 3 步）：候选照算，但返回 view 结果。
	if handle.Mode == knowledge.ModeShadow {
		result, _ := t.runView(ctx, map[string]interface{}{"file_path": normalized, "limit": 200})
		env := fallbackEnvelope("code_navigate", codeFallbackShadowMode, codeConfidenceExact, "view", result, handle)
		env.Explanation += fmt.Sprintf(" 索引候选 %d 条（未返回）。", len(syms))
		env.Truncated = env.Truncated || clamped
		return codeResult(env)
	}

	// members 只保留该文件本身。
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

	truncated := clamped || len(syms) >= queryLimit
	if len(results) > limit {
		results = results[:limit]
		truncated = true
	}

	env := newCodeEnvelope("code_navigate")
	env.Source = codeSourceIndex
	env.Confidence = codeConfidenceExact
	applySnapshot(&env, handle)
	env.Results = results
	env.Truncated = truncated
	env.Explanation = fmt.Sprintf("文件 %s 内定义 %d 个符号（索引口径，路径精确匹配）。", normalized, len(results))
	return codeResult(env)
}
