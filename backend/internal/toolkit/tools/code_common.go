package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
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
	// codeSourceSemantic 是语义通道（进程外 LSP）结果：编译器级口径，
	// 精度高于索引启发式（Phase 4 接线；默认关闭，见 knowledge.lsp.enabled）。
	codeSourceSemantic = "lsp"

	// ADR-0004 §4.1 的两档陈旧度边界（初始值：60s / 15min，Phase2-start 校准）。
	// D7 要求它们是常量而非字面量散布，因此只在此处定义。
	CodeStaleFreshSeconds int64 = 60
	CodeStaleMaxSeconds   int64 = 900

	// 分级注册 tier（ADR-0004 §4.1）：
	//   all         = 全开（writer / reader S ≤ S_fresh）
	//   definitions = 仅定义类（reader S_fresh < S ≤ S_max）
	//   none        = 不注册（reader S > S_max / 从未成功索引）
	CodeIndexTierAll         = "all"
	CodeIndexTierDefinitions = "definitions"
	CodeIndexTierNone        = "none"

	// completeness 取值（ADR-0004 §4.2）。
	codeCompletenessFull     = "full"
	codeCompletenessPartial  = "partial"
	codeCompletenessFallback = "fallback"

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
	// codeFallbackStaleIndex 是陈旧度守卫触发的降级原因（ADR-0004 §4.1：
	// 关系类在中等陈旧 / 定义类在过旧档都不得返回索引结果，只能实时兜底）。
	codeFallbackStaleIndex = "stale_index"
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
	// Root 是 workspace 根目录（绝对路径）；语义查询的路径锚点。
	Root string
	// Semantic 是可选语义通道（nil = 未启用/不支持，调用方降级到索引路径）。
	Semantic knowledge.SemanticAdapter
	// FilePaths 是 file_id → workspace 相对路径的映射（打开索引时构建一次）。
	FilePaths map[string]string
	// SnapshotTS 是索引最近成功写事务时间（unix 秒；0 = 无索引快照）。
	// ADR-0004 §4.2：所有 code.* 结果必须携带它。
	SnapshotTS int64
	// StalenessSeconds 是 (now - SnapshotTS) 的实际值（ADR-0004 D3）；
	// writer 恒为 0（索引本地即最新），reader 不得为 0 或省略。
	StalenessSeconds int64
	// Writer 表示本进程持有 store 写锁（本地索引即最新）。
	Writer bool
	// Tier 是 ADR-0004 §4.1 的分级结果（all|definitions|none）。
	Tier string
}

// CodeTierForSnapshot 计算 ADR-0004 §4.1 的分级：
// writer 或逃生舱关闭分级（gradingEnabled=false）→ all；
// reader 按 S 落三档（== 边界视为通过：≤ S_fresh 为 all，≤ S_max 为 definitions）。
// snapshotTS <= 0（从未成功索引）没有可用快照，按 none 处理（fail closed）。
func CodeTierForSnapshot(writer bool, snapshotTS, stalenessSeconds int64, gradingEnabled bool) string {
	if writer || !gradingEnabled {
		return CodeIndexTierAll
	}
	if snapshotTS <= 0 {
		return CodeIndexTierNone
	}
	switch {
	case stalenessSeconds <= CodeStaleFreshSeconds:
		return CodeIndexTierAll
	case stalenessSeconds <= CodeStaleMaxSeconds:
		return CodeIndexTierDefinitions
	default:
		return CodeIndexTierNone
	}
}

// CodeTierAllowsDefinition 报告定义类查询在当前 tier 下是否可用
// （中等陈旧仍可用；过旧档硬闸关闭）。
func CodeTierAllowsDefinition(tier string) bool { return tier != CodeIndexTierNone }

// CodeTierAllowsRelation 报告关系类查询在当前 tier 下是否可用
// （只有全开档可用；D1：陈旧时不得返回可能静默漏报的引用集合）。
// 空串 = 未分级句柄（既有测试/手工构造），按全开处理；生产句柄由
// newCodeIndexResolver 统一设置，不会是空串。
func CodeTierAllowsRelation(tier string) bool {
	return tier == CodeIndexTierAll || tier == ""
}

// definitionTierUsable / relationTierUsable 是运行时守卫：注册决策与执行
// 之间存在时间窗口（陈旧度会漂移），执行前必须按当前 handle 再判一次。
func definitionTierUsable(handle *CodeIndexHandle) bool {
	return handle == nil || CodeTierAllowsDefinition(handle.Tier)
}

func relationTierUsable(handle *CodeIndexHandle) bool {
	return handle == nil || CodeTierAllowsRelation(handle.Tier)
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
	Tool       string  `json:"tool"`
	Source     string  `json:"source"`
	Confidence float64 `json:"confidence"`
	Version    string  `json:"version,omitempty"`
	// ADR-0004 §4.2：三字段在任意模式下都必须出现（无 omitempty）。
	// snapshot_ts = 索引最近成功写事务时间（unix 秒；0 = 无索引快照）；
	// staleness_seconds = reader 实际陈旧度（writer 恒 0）；
	// completeness ∈ {full, partial, fallback}。
	SnapshotTS       int64         `json:"snapshot_ts"`
	StalenessSeconds int64         `json:"staleness_seconds"`
	Completeness     string        `json:"completeness"`
	Range            *codeRange    `json:"range,omitempty"`
	Truncated        bool          `json:"truncated"`
	NextCursor       string        `json:"next_cursor,omitempty"`
	Explanation      string        `json:"explanation,omitempty"`
	Degraded         bool          `json:"degraded"`
	Results          interface{}   `json:"results,omitempty"`
	Fallback         *codeFallback `json:"fallback,omitempty"`
}

// newCodeEnvelope 创建带默认值的信封。
func newCodeEnvelope(tool string) codeEnvelope {
	return codeEnvelope{Tool: tool, Source: codeSourceIndex, Confidence: codeConfidenceNone}
}

// codeResult 把信封编码为模型可见的工具结果（Content=JSON；Metadata 带关键字段）。
func codeResult(env codeEnvelope) *toolkit.ToolResult {
	if env.Completeness == "" {
		env.Completeness = codeCompletenessFor(env)
	}
	payload, err := json.Marshal(env)
	if err != nil {
		return stampToolOwnsOutput(&toolkit.ToolResult{
			Success:    false,
			OutputKind: toolresult.KindText,
			Error:      fmt.Errorf("code.* 结果编码失败: %w", err),
		})
	}
	meta := map[string]interface{}{
		"code_tool":         env.Tool,
		"source":            env.Source,
		"confidence":        env.Confidence,
		"degraded":          env.Degraded,
		"truncated":         env.Truncated,
		"snapshot_ts":       env.SnapshotTS,
		"staleness_seconds": env.StalenessSeconds,
		"completeness":      env.Completeness,
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

// codeCompletenessFor 从来源与截断推导 completeness（ADR-0004 §4.2）：
// fallback = 实时 grep/view 兜底；index+grep / 截断 = partial；纯索引完整 = full。
func codeCompletenessFor(env codeEnvelope) string {
	switch env.Source {
	case codeSourceFallback:
		return codeCompletenessFallback
	case codeSourceIndexGrep:
		return codeCompletenessPartial
	}
	if env.Truncated {
		return codeCompletenessPartial
	}
	return codeCompletenessFull
}

// applySnapshot 把索引快照的陈旧度写入信封；handle=nil 时保持零值
// （字段仍必须出现，ADR-0004 §4.2 要求三字段不省略）。
func applySnapshot(env *codeEnvelope, handle *CodeIndexHandle) {
	if env == nil || handle == nil {
		return
	}
	env.SnapshotTS = handle.SnapshotTS
	env.StalenessSeconds = handle.StalenessSeconds
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
// handle 非 nil 时同时携带索引快照的陈旧度（D3：reader 实际值不得省略）。
func fallbackEnvelope(tool, reason string, confidence float64, fallbackTool string, result *toolkit.ToolResult, handle *CodeIndexHandle) codeEnvelope {
	env := newCodeEnvelope(tool)
	env.Source = codeSourceFallback
	env.Confidence = confidence
	env.Degraded = true
	applySnapshot(&env, handle)
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

// staleGuardEnvelope 是陈旧度守卫触发的降级信封：不返回索引结果，改用实时
// fallback（ADR-0004 §5 D1 的硬约束），并显式解释触发档位与实际陈旧度。
func staleGuardEnvelope(tool string, handle *CodeIndexHandle, fallbackTool string, result *toolkit.ToolResult) codeEnvelope {
	env := fallbackEnvelope(tool, codeFallbackStaleIndex, codeConfidenceNone, fallbackTool, result, handle)
	if handle != nil {
		env.Explanation = fmt.Sprintf(
			"索引快照已落后约 %d 秒（%s）：为避免静默漏报，本次未返回索引结果，改用 %s 实时输出（ADR-0004 §4.1/§5 D1）。",
			handle.StalenessSeconds, staleTierName(handle.Tier), fallbackTool,
		)
	}
	return env
}

// staleTierName 把 tier 映射为可读档位名（降级说明用）。
func staleTierName(tier string) string {
	switch tier {
	case CodeIndexTierAll:
		return "全开档"
	case CodeIndexTierDefinitions:
		return "中等陈旧（仅定义类）"
	default:
		return "过旧档（不注册）"
	}
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
	case codeFallbackStaleIndex:
		return "索引快照陈旧度超过安全窗口：已按硬约束降级到 " + fallbackTool + " 实时结果（ADR-0004 §4.1/§5 D1）。"
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

// ---- 语义通道（Phase 4 接线：ADR-0006 位置边界 + ADR-0002 §4.1 门控）----

// trySemanticRefsQuery 尝试用语义通道回答引用查询。
//
// 返回 (env, true) 表示已产出结果；返回 (nil, false) 表示调用方应走索引路径
// （未启用 / 不可用 / 查询失败 / 零命中 / kind 不支持）。语义查询需要**声明
// 位置**，因此符号必须先在索引中解析成功——索引仍是位置来源，语义通道只提升
// 引用集合的精度。
//
// kind 口径：LSP 不区分调用/类型使用。"" 与 "reference" 直接返回全部引用点；
// "call" 用行内 `name(` 启发式过滤（与索引侧同口径，但候选集是编译器级）；
// import/implement 交给索引通道（索引有 kind 信息，语义没有）。
func trySemanticRefsQuery(ctx context.Context, toolName string, handle *CodeIndexHandle, sym knowledge.Symbol, found bool, symbol, kind string, limit int, clamped bool) (*codeEnvelope, bool) {
	if handle == nil || handle.Semantic == nil || !found {
		return nil, false
	}
	switch kind {
	case "", "reference", "call":
	default:
		return nil, false
	}
	path := handle.PathForFile(sym.FileID)
	if path == "" || sym.Range.Start.Line <= 0 {
		return nil, false
	}
	// 位置口径转换只在这里发生：索引 1-based（adapter_builtin.go:276）↔
	// canonical 0-based（ADR-0006 §4.4）。列是行内 UTF-8 字节偏移，两边一致。
	locs, err := handle.Semantic.References(ctx, path, sym.Range.Start.Line-1, sym.Range.Start.Column)
	if err != nil || len(locs) == 0 {
		return nil, false
	}
	// 声明自身不算"引用点"（与索引通道口径一致；LSP 的 includeDeclaration
	// 由适配器固定为 true，因此这里按位置剔除）。
	declLine := sym.Range.Start.Line - 1
	refs := make([]knowledge.SemanticLocation, 0, len(locs))
	for _, loc := range locs {
		if loc.Path == path && loc.Line == declLine {
			continue
		}
		refs = append(refs, loc)
	}
	if kind == "call" {
		refs = filterCallLocations(handle, refs, symbol)
	}
	if len(refs) == 0 {
		return nil, false
	}

	results := make([]codeRefHit, 0, len(refs))
	for _, loc := range refs {
		hit := codeRefHit{
			Path:       loc.Path,
			Line:       loc.Line + 1, // canonical 0-based → 结果口径 1-based
			Col:        loc.Col,
			Kind:       string(knowledge.RefReference),
			Confidence: codeConfidenceExact,
			Source:     string(handle.Semantic.Name()),
		}
		if kind == "call" {
			// 调用点由启发式过滤得到：置信度回到索引侧同口径（0.90）。
			hit.Kind = string(knowledge.RefCall)
			hit.Confidence = codeConfidenceFTS
		}
		results = append(results, hit)
	}
	truncated := clamped
	if len(results) > limit {
		results = results[:limit]
		truncated = true
	}
	env := newCodeEnvelope(toolName)
	env.Source = codeSourceSemantic
	env.Confidence = codeConfidenceExact
	applySnapshot(&env, handle)
	env.Results = results
	env.Truncated = truncated
	env.Explanation = fmt.Sprintf(
		"语义通道（%s，编译器级口径）命中 %d 条引用；LSP 不区分调用/类型使用%s。",
		handle.Semantic.Version(), len(results), semanticKindNote(kind),
	)
	return &env, true
}

// semanticKindNote 说明语义通道下 kind 的近似口径。
func semanticKindNote(kind string) string {
	if kind == "call" {
		return "，调用点由行内 `name(` 启发式过滤（候选集来自语义通道）"
	}
	return ""
}

// filterCallLocations 用行内 `name(` 启发式从引用点中筛出调用点。
// 文件读取失败的行保守丢弃（宁可少报，不假装精确）。
func filterCallLocations(handle *CodeIndexHandle, locs []knowledge.SemanticLocation, symbol string) []knowledge.SemanticLocation {
	out := make([]knowledge.SemanticLocation, 0, len(locs))
	cache := map[string][]string{}
	for _, loc := range locs {
		lines, ok := cache[loc.Path]
		if !ok {
			lines = readWorkspaceLines(handle.Root, loc.Path)
			cache[loc.Path] = lines
		}
		if loc.Line < 0 || loc.Line >= len(lines) {
			continue
		}
		line := lines[loc.Line]
		if strings.Contains(line, symbol+"(") || strings.Contains(line, symbol+" (") {
			out = append(out, loc)
		}
	}
	return out
}

// readWorkspaceLines 读取 workspace 相对路径的文本行；失败返回 nil。
func readWorkspaceLines(root, rel string) []string {
	root = strings.TrimSpace(root)
	if root == "" || strings.TrimSpace(rel) == "" {
		return nil
	}
	content, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
	if err != nil {
		return nil
	}
	return strings.Split(string(content), "\n")
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
