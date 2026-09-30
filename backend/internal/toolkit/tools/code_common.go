package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	runtimeexecutor "github.com/wwsheng009/ai-agent-runtime/internal/executor"
	"github.com/wwsheng009/ai-agent-runtime/internal/knowledge"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolkit"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolresult"
)

// ---- Phase 3（06 §4 Phase 3 / 04 §4.6）：code.* 工具面公共件 ----
//
// 决策：code.* 是增强前端，不替换 P0 工具；索引不可用 / 未命中 / 出错一律
// fallback 到既有 grep/view，并在统一返回结构的 source 字段标注。
//
// 统一返回结构（04 §5 Phase 3 交付 2）：source / confidence / version /
// range / truncated / next_cursor / explanation / degraded。
//
// confidence 是工具级初值（待 Phase 3 实测校准，04 §7.6；边界语义与 W3 一致，
// == 阈值视为通过）：
//   - 1.00 精确符号解析（大小写敏感、唯一命中）
//   - 0.90 FTS 命中 / 精确解析得到的引用集合
//   - 0.80 子串解析 / 模糊命中
//   - 0.00 无命中（on 档触发一次 grep 补量；shadow/off 直接 fallback）
const (
	codeSourceIndex     = "index"
	codeSourceFallback  = "fallback"
	codeSourceIndexGrep = "index+grep"

	codeConfidenceExact = 1.00
	codeConfidenceFTS   = 0.90
	codeConfidenceFuzzy = 0.80
	codeConfidenceNone  = 0.00

	// codeExploreBelow 是 on 档"索引结果不足 → 补一次 grep"的硬下限，
	// 与 04 §4.4 的探索下界同口径（0.50）。
	codeExploreBelow = 0.50

	// codeSearchDefaultLimit / codeSearchMaxLimit 是 code.search 的条数窗口。
	codeSearchDefaultLimit = 20
	codeSearchMaxLimit     = 100
	// codeRefsDefaultLimit / codeRefsMaxLimit 是引用类工具的条数窗口。
	codeRefsDefaultLimit = 50
	codeRefsMaxLimit     = 200
	// codeSymbolsDefaultLimit 是符号解析的候选上限。
	codeSymbolsDefaultLimit = 12

	// fallback 原因 token（稳定，供结果与日志消费）。
	codeFallbackIndexUnavailable = "index_unavailable"
	codeFallbackNoIndexHit       = "no_index_hit"
	codeFallbackShadowMode       = "shadow_mode"
	codeFallbackIndexError       = "index_error"
	codeFallbackByRequest        = "file_path_requested"
	// codeFallbackLowConfidence 是 on 档"低相关命中 → 补一次 grep"的补量标记
	// （04 §4.6 第 4 步；source=index+grep，索引命中仍是主结果）。
	codeFallbackLowConfidence = "low_confidence_supplement"
)

// CodeIndex 是 code.* 与 view --symbol 需要的只读索引句柄（knowledge.Store 的窄子集）。
type CodeIndex interface {
	FindWorkspace(ctx context.Context, rootPath string) (string, bool, error)
	FindSymbols(ctx context.Context, q knowledge.SymbolQuery) ([]knowledge.Symbol, error)
	FindRefs(ctx context.Context, q knowledge.RefQuery) ([]knowledge.Reference, error)
	Search(ctx context.Context, q knowledge.SearchQuery) ([]knowledge.SearchHit, error)
	ListActiveFiles(ctx context.Context, workspaceID string) ([]knowledge.FileRecord, error)
}

// CodeIndexHandle 是一次索引解析的结果。
type CodeIndexHandle struct {
	Index       CodeIndex
	Mode        knowledge.Mode
	WorkspaceID string
	// FilePaths 是 file_id → workspace 相对路径的映射（打开索引时构建一次）。
	FilePaths map[string]string
}

// PathForFile 返回 file_id 对应的 workspace 相对路径；未知返回空串。
func (h *CodeIndexHandle) PathForFile(fileID string) string {
	if h == nil || len(h.FilePaths) == 0 {
		return ""
	}
	return h.FilePaths[strings.TrimSpace(fileID)]
}

// CodeIndexResolver 在调用时刻解析当前 workspace 的只读索引；ok=false 表示
// 索引不可用（mode=off / 库不存在 / 打开失败），调用方必须 fallback。
type CodeIndexResolver func(ctx context.Context) (*CodeIndexHandle, bool)

// codeRange 是统一返回结构里的行/列范围（1-based，与 symbols 表口径一致）。
type codeRange struct {
	Path      string `json:"path,omitempty"`
	StartLine int    `json:"start_line,omitempty"`
	StartCol  int    `json:"start_col,omitempty"`
	EndLine   int    `json:"end_line,omitempty"`
	EndCol    int    `json:"end_col,omitempty"`
}

// codeFallback 记录一次降级的去向与原因（source="fallback" 时必填）。
type codeFallback struct {
	Tool   string `json:"tool"`
	Reason string `json:"reason,omitempty"`
	// Output 是 grep/view 的原始输出（已按模型可见窗口裁剪），模型可直接消费。
	Output string `json:"output,omitempty"`
}

// codeEnvelope 是 code.* 的统一返回结构（04 §5 Phase 3 交付 2）。
type codeEnvelope struct {
	Tool        string        `json:"tool"`
	Source      string        `json:"source"`
	Confidence  float64       `json:"confidence"`
	Version     string        `json:"version,omitempty"`
	Range       *codeRange    `json:"range,omitempty"`
	Truncated   bool          `json:"truncated"`
	NextCursor  string        `json:"next_cursor,omitempty"`
	Explanation string        `json:"explanation,omitempty"`
	Degraded    bool          `json:"degraded"`
	Results     interface{}   `json:"results,omitempty"`
	Fallback    *codeFallback `json:"fallback,omitempty"`
}

// newCodeEnvelope 创建带默认值的信封。
func newCodeEnvelope(tool string) codeEnvelope {
	return codeEnvelope{Tool: tool, Source: codeSourceIndex, Confidence: codeConfidenceNone}
}

// codeResult 把信封编码为模型可见的工具结果（Content=JSON；Metadata 带关键字段）。
func codeResult(env codeEnvelope) *toolkit.ToolResult {
	payload, err := json.Marshal(env)
	if err != nil {
		return stampToolOwnsOutput(&toolkit.ToolResult{
			Success:    false,
			OutputKind: toolresult.KindText,
			Error:      fmt.Errorf("code.* 结果编码失败: %w", err),
		})
	}
	meta := map[string]interface{}{
		"code_tool":  env.Tool,
		"source":     env.Source,
		"confidence": env.Confidence,
		"degraded":   env.Degraded,
		"truncated":  env.Truncated,
	}
	if env.Fallback != nil {
		meta["fallback_tool"] = env.Fallback.Tool
		meta["fallback_reason"] = env.Fallback.Reason
	}
	return stampToolOwnsOutput(&toolkit.ToolResult{
		Success:    true,
		OutputKind: toolresult.KindText,
		Content:    string(payload),
		Metadata:   meta,
	})
}

// codeToolBase 提供 code.* 与 view --symbol 共用的依赖注入与 fallback 执行。
type codeToolBase struct {
	basePath string
	resolver CodeIndexResolver
	grep     *GrepTool
	view     *ViewTool
}

func newCodeToolBase() codeToolBase {
	return codeToolBase{grep: NewGrepTool(), view: NewViewTool()}
}

// SetBasePath 与既有工具一致：把 workspace 根同时交给 fallback 工具。
func (b *codeToolBase) SetBasePath(path string) {
	if b == nil {
		return
	}
	b.basePath = strings.TrimSpace(path)
	if b.grep != nil {
		b.grep.SetBasePath(b.basePath)
	}
	if b.view != nil {
		b.view.SetBasePath(b.basePath)
	}
}

// SetSandbox 让 fallback 工具继承同一沙箱策略。
func (b *codeToolBase) SetSandbox(sandbox *runtimeexecutor.Sandbox) {
	if b == nil {
		return
	}
	if b.grep != nil {
		b.grep.SetSandbox(sandbox)
	}
	if b.view != nil {
		b.view.SetSandbox(sandbox)
	}
}

// SetCodeIndexResolver 注入索引解析器；nil 表示索引永远不可用（全 fallback）。
func (b *codeToolBase) SetCodeIndexResolver(resolver CodeIndexResolver) {
	if b == nil {
		return
	}
	b.resolver = resolver
}

// resolveIndex 取当前 workspace 的只读索引；不可用时 ok=false。
func (b *codeToolBase) resolveIndex(ctx context.Context) (*CodeIndexHandle, bool) {
	if b == nil || b.resolver == nil {
		return nil, false
	}
	handle, ok := b.resolver(ctx)
	if !ok || handle == nil || handle.Index == nil || strings.TrimSpace(handle.WorkspaceID) == "" {
		return nil, false
	}
	return handle, true
}

// runGrep / runView 执行一次 fallback；错误原样返回（Degrade-Not-Fail 由调用方兜底）。
func (b *codeToolBase) runGrep(ctx context.Context, params map[string]interface{}) (*toolkit.ToolResult, error) {
	if b == nil {
		return nil, fmt.Errorf("code.* 未初始化")
	}
	if b.grep == nil {
		b.grep = NewGrepTool()
		b.grep.SetBasePath(b.basePath)
	}
	return b.grep.Execute(ctx, params)
}

func (b *codeToolBase) runView(ctx context.Context, params map[string]interface{}) (*toolkit.ToolResult, error) {
	if b == nil {
		return nil, fmt.Errorf("code.* 未初始化")
	}
	if b.view == nil {
		b.view = NewViewTool()
		b.view.SetBasePath(b.basePath)
	}
	return b.view.Execute(ctx, params)
}

// fallbackEnvelope 把一次 grep/view 结果包成统一返回结构（source="fallback"）。
func fallbackEnvelope(tool, reason string, confidence float64, fallbackTool string, result *toolkit.ToolResult) codeEnvelope {
	env := newCodeEnvelope(tool)
	env.Source = codeSourceFallback
	env.Confidence = confidence
	env.Degraded = true
	env.Fallback = &codeFallback{Tool: fallbackTool, Reason: reason}
	env.Explanation = codeFallbackExplanation(reason, fallbackTool)
	if result != nil {
		env.Fallback.Output = strings.TrimSpace(result.Content)
		// 透传 grep/view 自身的截断信号：丢弃它会让模型把被窗口裁剪的输出
		// 当作完整证据（与两个工具各自的 honest-truncation 契约不一致）。
		if codeResultTruncated(result) {
			env.Truncated = true
		}
		if !result.Success && result.Error != nil {
			env.Explanation += "；fallback 错误: " + result.Error.Error()
		}
	}
	return env
}

// codeFallbackExplanation 生成稳定、可读的降级说明。
func codeFallbackExplanation(reason, fallbackTool string) string {
	switch reason {
	case codeFallbackIndexUnavailable:
		return "索引不可用（knowledge.mode=off / 库不存在 / 打开失败）：本次由 " + fallbackTool + " 提供等价结果（source=fallback）。"
	case codeFallbackNoIndexHit:
		return "索引无命中：已补一次 " + fallbackTool + " 并返回其输出（04 §4.6 on 档补量口径）。"
	case codeFallbackShadowMode:
		return "shadow 档：索引候选已计算，但按口径返回 " + fallbackTool + " 结果（不改变模型可见输出）。"
	case codeFallbackIndexError:
		return "索引查询失败：已降级到 " + fallbackTool + "（Degrade-Not-Fail）。"
	case codeFallbackByRequest:
		return "按 file_path 读取（等价 view，未走索引）：结果为 " + fallbackTool + " 的原始输出。"
	default:
		return "已降级到 " + fallbackTool + "。"
	}
}

// codeResultTruncated 读取 grep/view 结果的截断元数据。
//
// grep 用 "truncated"（行窗口）与 "results_truncated"（字节预算），view 用
// "is_truncated"；任一为真都表示模型可见输出被裁剪。
func codeResultTruncated(result *toolkit.ToolResult) bool {
	if result == nil || result.Metadata == nil {
		return false
	}
	for _, key := range []string{"truncated", "results_truncated", "is_truncated"} {
		if v, ok := result.Metadata[key].(bool); ok && v {
			return true
		}
	}
	return false
}

// normalizeCodePath 统一路径分隔符：Windows 调用方常传反斜杠，而索引存的是
// workspace 相对的正斜杠路径；不归一化会让精确匹配静默落空。
func normalizeCodePath(path string) string {
	normalized := strings.ReplaceAll(strings.TrimSpace(path), "\\", "/")
	return strings.TrimSuffix(normalized, "/")
}

// validCodeRefKind 校验 refs.kind 闭集（knowledge.RefKind）。
func validCodeRefKind(kind string) bool {
	switch knowledge.RefKind(strings.ToLower(strings.TrimSpace(kind))) {
	case knowledge.RefReference, knowledge.RefCall, knowledge.RefImport, knowledge.RefImplement:
		return true
	default:
		return false
	}
}

// annotateRefFallback 标注引用类降级丢失的 kind 语义（fallback 是文本近似）。
func annotateRefFallback(env *codeEnvelope, kind string) {
	if env == nil || strings.TrimSpace(kind) == "" {
		return
	}
	env.Explanation += "；fallback 为文本近似，不保证按 kind 过滤（含同名噪音）。"
}

// codeSearchNameMatched 报告 FTS 命中里是否存在与查询同名/包含关系的符号；
// 全部不匹配时按低相关处理（04 §4.6 第 4 步的补量分支）。
func codeSearchNameMatched(hits []knowledge.SearchHit, query string) bool {
	q := strings.ToLower(strings.TrimSpace(query))
	if q == "" {
		return true
	}
	for _, hit := range hits {
		if strings.Contains(strings.ToLower(hit.Name), q) ||
			strings.Contains(strings.ToLower(hit.QualifiedName), q) {
			return true
		}
	}
	return false
}

// codeRefHit 是引用类结果的统一形状（code.references / code.callers / navigate refs）。
type codeRefHit struct {
	Path       string  `json:"path"`
	Line       int     `json:"line"`
	Col        int     `json:"col,omitempty"`
	Kind       string  `json:"kind,omitempty"`
	Snippet    string  `json:"snippet,omitempty"`
	Confidence float64 `json:"confidence,omitempty"`
	Source     string  `json:"source,omitempty"`
	FromSymbol string  `json:"from_symbol,omitempty"`
}

// codeRefHitFrom 把索引行映射为结果形状；from_symbol 以 id 形式给出
// （名字解析需要额外查询，v1 不展开，保持单次查询的成本边界）。
func codeRefHitFrom(ref knowledge.Reference, handle *CodeIndexHandle) codeRefHit {
	path := ""
	if handle != nil {
		path = handle.PathForFile(ref.FileID)
	}
	return codeRefHit{
		Path:       path,
		Line:       ref.Line,
		Col:        ref.Col,
		Kind:       string(ref.Kind),
		Snippet:    ref.Snippet,
		Confidence: ref.Confidence,
		Source:     string(ref.Source),
		FromSymbol: ref.FromSymbolID,
	}
}

// codeRefsQuery 执行一次引用查询（symbol 身份 + 名字双条件，保持增量重建后的稳定性）。
func codeRefsQuery(ctx context.Context, idx CodeIndex, sym knowledge.Symbol, name string, kind knowledge.RefKind, pathPrefix string, limit int) ([]knowledge.Reference, error) {
	query := knowledge.RefQuery{
		ToSymbolName: strings.TrimSpace(name),
		Kind:         kind,
		PathPrefix:   pathPrefix,
		Limit:        limit,
	}
	if strings.TrimSpace(sym.ID) != "" {
		query.ToSymbolID = sym.ID
	}
	return idx.FindRefs(ctx, query)
}

// ---- 符号结果与解析 ----

// codeSymbolHit 是符号类结果的统一形状。
type codeSymbolHit struct {
	Path          string    `json:"path"`
	Name          string    `json:"name"`
	QualifiedName string    `json:"qualified_name,omitempty"`
	Kind          string    `json:"kind,omitempty"`
	Signature     string    `json:"signature,omitempty"`
	Range         codeRange `json:"range"`
	IsTest        bool      `json:"is_test,omitempty"`
}

// codeSymbolHitFrom 把索引行映射为结果形状（path 由 handle.FilePaths 解析）。
func codeSymbolHitFrom(sym knowledge.Symbol, path string) codeSymbolHit {
	return codeSymbolHit{
		Path:          path,
		Name:          sym.Name,
		QualifiedName: sym.QualifiedName,
		Kind:          string(sym.Kind),
		Signature:     sym.Signature,
		Range: codeRange{
			Path:      path,
			StartLine: sym.Range.Start.Line,
			StartCol:  sym.Range.Start.Column,
			EndLine:   sym.Range.End.Line,
			EndCol:    sym.Range.End.Column,
		},
		IsTest: sym.IsTest,
	}
}

// resolveCodeSymbol 解析符号名：先精确、再子串；返回 confidence ∈ {1.0, 0.8, 0.0}。
func resolveCodeSymbol(ctx context.Context, idx CodeIndex, name, pathPrefix string) (knowledge.Symbol, float64, bool, error) {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" || idx == nil {
		return knowledge.Symbol{}, codeConfidenceNone, false, nil
	}
	exact, err := idx.FindSymbols(ctx, knowledge.SymbolQuery{
		Name: trimmed, PathPrefix: pathPrefix, Exact: true, Limit: codeSymbolsDefaultLimit,
	})
	if err != nil {
		return knowledge.Symbol{}, codeConfidenceNone, false, err
	}
	if sym, ok := pickCodeSymbol(exact, trimmed); ok {
		return sym, codeConfidenceExact, true, nil
	}
	fuzzy, err := idx.FindSymbols(ctx, knowledge.SymbolQuery{
		Name: trimmed, PathPrefix: pathPrefix, Limit: codeSymbolsDefaultLimit,
	})
	if err != nil {
		return knowledge.Symbol{}, codeConfidenceNone, false, err
	}
	if sym, ok := pickCodeSymbol(fuzzy, trimmed); ok {
		return sym, codeConfidenceFuzzy, true, nil
	}
	return knowledge.Symbol{}, codeConfidenceNone, false, nil
}

// pickCodeSymbol 从候选中挑一个：排除软删除；精确名优先、非测试优先、
// qualified_name 短优先、行号小优先（确定性排序，与 W4 的候选排序口径一致）。
func pickCodeSymbol(syms []knowledge.Symbol, name string) (knowledge.Symbol, bool) {
	var best knowledge.Symbol
	found := false
	for _, sym := range syms {
		if sym.DeletedAt != 0 || strings.TrimSpace(sym.Name) == "" {
			continue
		}
		if !found {
			best, found = sym, true
			continue
		}
		if betterCodeSymbol(sym, best, name) {
			best = sym
		}
	}
	return best, found
}

func betterCodeSymbol(a, b knowledge.Symbol, name string) bool {
	aExact := strings.EqualFold(strings.TrimSpace(a.Name), name)
	bExact := strings.EqualFold(strings.TrimSpace(b.Name), name)
	if aExact != bExact {
		return aExact
	}
	if a.IsTest != b.IsTest {
		return !a.IsTest
	}
	if len(a.QualifiedName) != len(b.QualifiedName) {
		return len(a.QualifiedName) < len(b.QualifiedName)
	}
	return a.Range.Start.Line < b.Range.Start.Line
}

// ---- 参数读取（map[string]interface{} → 具体类型；非法值按缺省处理）----

func codeParamString(params map[string]interface{}, key string) string {
	if params == nil {
		return ""
	}
	value, ok := params[key]
	if !ok || value == nil {
		return ""
	}
	if s, ok := value.(string); ok {
		return strings.TrimSpace(s)
	}
	return ""
}

func codeParamInt(params map[string]interface{}, key string, fallback int) int {
	if params == nil {
		return fallback
	}
	switch value := params[key].(type) {
	case int:
		return value
	case int64:
		return int(value)
	case float64:
		return int(value)
	case json.Number:
		if parsed, err := value.Int64(); err == nil {
			return int(parsed)
		}
	}
	return fallback
}

func codeParamBool(params map[string]interface{}, key string) bool {
	if params == nil {
		return false
	}
	value, ok := params[key].(bool)
	return ok && value
}

// codeClampLimit 归一化条数窗口：<=0 取默认，超上限夹取并报告 truncated。
func codeClampLimit(requested, fallback, max int) (int, bool) {
	limit := requested
	if limit <= 0 {
		limit = fallback
	}
	if max > 0 && limit > max {
		return max, true
	}
	return limit, false
}
